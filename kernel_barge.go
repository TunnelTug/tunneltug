package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/0TrustCloud/ultimate_db"
	"github.com/0TrustCloud/ultimate_keystore"
	"github.com/0TrustCloud/ultimate_sync"
)

// Kernel data-replication barges (ultimate_db / ultimate_keystore).
//
// Scale-out model: global / multi-PoP ingresses each serve from local embeds,
// while this plane keeps service data in sync in real time across regions.
// Products open local DBs for serving and AddPeer these barges (NetworkTransport
// /kernel/* scatter-gather, keystore kernel RPC) — replicate, never prefer-remote.
//
// Distinct from TunnelTug mesh/SDF and from a single central primary store.

const (
	defaultUDBListen = ":8480"
	defaultUKSListen = ":8481"
)

func runUltimateDBBarge() {
	ctx, stop := notifyShutdownContext()
	defer stop()

	listen := strings.TrimSpace(*udbListen)
	if listen == "" {
		listen = defaultUDBListen
	}
	dataDir := strings.TrimSpace(*udbDataDir)
	if dataDir == "" {
		dataDir = filepath.Join(defaultKernelDataRoot(), "ultimate_db")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("ultimate_db barge data dir: %v", err)
	}
	nodeID := strings.TrimSpace(*udbNodeID)
	if nodeID == "" {
		nodeID = "ultimate-db"
	}

	// Separate instance — never open mesh/sdf paths used by TunnelTug core.
	db, store, closer, err := ultimate_db.OpenKernelDB(dataDir, 512)
	if err != nil {
		log.Fatalf("ultimate_db barge open: %v", err)
	}
	defer closer()

	transport := ultimate_db.NewHTTPNetworkTransport(nodeID)
	// Optional static peers (other ultimate_db kernel barges) for scatter-gather + KV pull.
	for _, p := range splitKernelPeers(*udbPeers) {
		transport.AddPeer(p.id, p.addr)
	}
	engine, err := ultimate_db.NewIntegratedEngine(db, transport, ultimate_db.AllowAllInterceptor{})
	if err != nil {
		log.Fatalf("ultimate_db kernel engine: %v", err)
	}
	for _, p := range splitKernelPeers(*udbPeers) {
		engine.AddClusterNode(p.id, p.addr)
	}

	token := strings.TrimSpace(*authToken)
	if token == "" {
		_ = ensureAuthToken()
		token = strings.TrimSpace(*authToken)
	}
	srv := ultimate_db.NewKernelServer(engine, store, nodeID, token)

	// ultimate_sync: state machine + KV pull catch-up between kernel barges.
	syncMach := ultimate_sync.NewMachine(nodeID, ultimate_sync.KindBoth)
	syncMach.OnMessage(func(msg ultimate_sync.Message) {
		log.Print(msg.String())
	})
	for _, p := range splitKernelPeers(*udbPeers) {
		syncMach.AddPeer(p.id, p.addr)
	}
	srv.AttachSync(syncMach)

	pull := ultimate_db.NewKVPullReplicator(store, nodeID, token)
	health := &ultimate_sync.HTTPHealthTransport{
		Token:     token,
		LocalNode: nodeID,
	}
	syncEng := ultimate_sync.NewEngine(syncMach, health, pull)
	go func() {
		if err := syncEng.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("ultimate_db sync engine: %v", err)
		}
	}()

	log.Printf("ultimate_db kernel barge node=%s listen=%s data=%s peers=%d (ultimate_sync on)",
		nodeID, listen, dataDir, len(splitKernelPeers(*udbPeers)))
	if err := srv.ListenAndServe(ctx, listen); err != nil && ctx.Err() == nil {
		log.Fatalf("ultimate_db barge: %v", err)
	}
	log.Println("ultimate_db kernel barge stopped")
}

func runUltimateKeystoreBarge() {
	ctx, stop := notifyShutdownContext()
	defer stop()

	listen := strings.TrimSpace(*uksListen)
	if listen == "" {
		listen = defaultUKSListen
	}
	dataDir := strings.TrimSpace(*uksDataDir)
	if dataDir == "" {
		dataDir = filepath.Join(defaultKernelDataRoot(), "ultimate_keystore")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("ultimate_keystore barge data dir: %v", err)
	}
	nodeID := strings.TrimSpace(*uksNodeID)
	if nodeID == "" {
		nodeID = "ultimate-keystore"
	}

	// Own ultimate_db files under keystore data dir — not shared with ultimate_db barge
	// and not shared with TunnelTug mesh/SDF.
	_, store, closer, err := ultimate_db.OpenKernelDB(dataDir, 512)
	if err != nil {
		log.Fatalf("ultimate_keystore barge open: %v", err)
	}
	defer closer()

	ks := ultimate_keystore.NewKeystore(store)
	token := strings.TrimSpace(*authToken)
	if token == "" {
		_ = ensureAuthToken()
		token = strings.TrimSpace(*authToken)
	}
	srv := ultimate_keystore.NewKernelServer(ks, nodeID, token)

	// Peers: reuse -udb-peers string shape via -uks-peers when set; fall back empty.
	// Note: uksPeers flag exists on flags.go as udb-style peers for keystore plane.
	peers := splitKernelPeers(*uksPeers)
	syncMach := ultimate_sync.NewMachine(nodeID, ultimate_sync.KindKV)
	syncMach.OnMessage(func(msg ultimate_sync.Message) {
		log.Print(msg.String())
	})
	for _, p := range peers {
		syncMach.AddPeer(p.id, p.addr)
	}
	srv.AttachSync(syncMach)

	// Keystore data is typed KV on the same BTree — full-store pull catch-up.
	pull := ultimate_db.NewKVPullReplicator(store, nodeID, token)
	health := &ultimate_sync.HTTPHealthTransport{
		Token:     token,
		LocalNode: nodeID,
	}
	syncEng := ultimate_sync.NewEngine(syncMach, health, pull)
	go func() {
		if err := syncEng.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("ultimate_keystore sync engine: %v", err)
		}
	}()

	log.Printf("ultimate_keystore kernel barge node=%s listen=%s data=%s peers=%d (ultimate_sync on)",
		nodeID, listen, dataDir, len(peers))
	if err := srv.ListenAndServe(ctx, listen); err != nil && ctx.Err() == nil {
		log.Fatalf("ultimate_keystore barge: %v", err)
	}
	log.Println("ultimate_keystore kernel barge stopped")
}

func defaultKernelDataRoot() string {
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_KERNEL_DATA")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "data", "kernel")
	}
	return filepath.Join(home, ".tunneltug", "kernel")
}

type kernelPeer struct {
	id, addr string
}

// splitKernelPeers parses "id=http://host:port,id2=host:port" or bare URLs.
func splitKernelPeers(raw string) []kernelPeer {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []kernelPeer
	for i, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if eq := strings.IndexByte(part, '='); eq > 0 {
			out = append(out, kernelPeer{
				id:   strings.TrimSpace(part[:eq]),
				addr: strings.TrimSpace(part[eq+1:]),
			})
			continue
		}
		out = append(out, kernelPeer{
			id:   fmt.Sprintf("peer-%d", i+1),
			addr: part,
		})
	}
	return out
}

// stackKernelEnv injects kernel *replication peer* URLs for sibling product pods.
// Ports are zero-config defaults (8480/8481) unless YAML port: overrides those barges.
// Products keep local embeds and add these peers for NetworkTransport replication —
// never as a "prefer remote over local" store switch.
func stackKernelEnv(ns string, apps ...stackApp) map[string]string {
	ns = strings.TrimSpace(ns)
	if ns == "" {
		ns = "0trust-stack"
	}
	udbPort, uksPort := int32(8480), int32(8481)
	for _, a := range apps {
		switch a.Name {
		case "ultimate-db":
			if a.Port > 0 {
				udbPort = a.Port
			}
		case "ultimate-keystore":
			if a.Port > 0 {
				uksPort = a.Port
			}
		}
	}
	// HTTPS + mesh/cluster DNS names for HTTP/3 SNI (never plain http://).
	udb := fmt.Sprintf("https://ultimate-db.%s.svc:%d", ns, udbPort)
	uks := fmt.Sprintf("https://ultimate-keystore.%s.svc:%d", ns, uksPort)
	return map[string]string{
		// Replication peer endpoints (add via NetworkTransport.AddPeer — do not replace local DB).
		"ULTIMATE_DB_URL":           udb,
		"ULTIMATE_KEYSTORE_URL":     uks,
		"ULTIMATE_DB_KERNEL":        udb,
		"ULTIMATE_KEYSTORE_KERNEL":  uks,
		"KERNEL_DB_REPLICATION_URL": udb,
		"KERNEL_KEYSTORE_URL":       uks,
	}
}
