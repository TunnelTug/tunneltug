package main

// Host side of a running TunnelTug VM: the yamux control session with tug-init,
// the guest network, the kube-API forward, and the local API that the other
// verbs (vm status/down/exec, run, ps, …) talk to.

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
	"github.com/hashicorp/yamux"

	"tunneltug/vmproto"
)

// newTugVirtualNetwork is the VM's userspace network: NAT egress through the
// Windows host's sockets, DNS from the host resolver, and 192.168.127.254 → the
// host's 127.0.0.1 (so pods can reach a TunnelTug hub running on the host).
func newTugVirtualNetwork(o tugVMOptions) (*virtualnetwork.VirtualNetwork, error) {
	cfg := &types.Configuration{
		MTU:               1500,
		Subnet:            vmproto.Subnet,
		GatewayIP:         vmproto.GatewayIP,
		GatewayMacAddress: vmproto.GatewayMAC,
		DHCPStaticLeases:  map[string]string{vmproto.GuestIP: vmproto.GuestMAC},
		NAT:               map[string]string{vmproto.HostAlias: "127.0.0.1"},
		GatewayVirtualIPs: []string{vmproto.HostAlias},
		DNS: []types.Zone{{
			Name: "tunneltug.internal.",
			Records: []types.Record{
				{Name: "gateway", IP: net.ParseIP(vmproto.GatewayIP)},
				{Name: "host", IP: net.ParseIP(vmproto.HostAlias)},
			},
		}},
		Forwards: map[string]string{},
		Protocol: types.HyperKitProtocol,
	}
	return virtualnetwork.New(cfg)
}

type tugVMHost struct {
	name    string
	session *yamux.Session
	hello   vmproto.Hello
	stopped chan struct{}
	shares  []tugVMShare
	local   []io.Closer
}

func newTugVMHost(o tugVMOptions, ctl net.Conn) (*tugVMHost, error) {
	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard
	ycfg.KeepAliveInterval = 10 * time.Second
	sess, err := yamux.Server(ctl, ycfg)
	if err != nil {
		return nil, err
	}
	type res struct {
		h   vmproto.Hello
		err error
	}
	ch := make(chan res, 1)
	go func() {
		st, err := sess.Accept()
		if err != nil {
			ch <- res{err: err}
			return
		}
		defer st.Close()
		h, _, err := vmproto.ReadHeader(st)
		if err == nil && (h.Op != vmproto.OpHello || h.Hello == nil) {
			err = fmt.Errorf("first stream was %q, want hello", h.Op)
		}
		if err != nil {
			ch <- res{err: err}
			return
		}
		ch <- res{h: *h.Hello}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			sess.Close()
			return nil, r.err
		}
		h := &tugVMHost{name: o.Name, session: sess, hello: r.h, stopped: make(chan struct{}), shares: o.Shares}
		go h.serveGuestStreams()
		return h, nil
	case <-time.After(30 * time.Second):
		sess.Close()
		return nil, errors.New("guest connected but sent no hello")
	}
}

func (h *tugVMHost) open(hdr vmproto.Header) (net.Conn, error) {
	st, err := h.session.Open()
	if err != nil {
		return nil, err
	}
	if err := vmproto.WriteHeader(st, hdr); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

// exec runs argv in the guest root namespace, copying output to out.
func (h *tugVMHost) exec(argv []string, out io.Writer) (int, error) {
	st, err := h.open(vmproto.Header{Op: vmproto.OpExec, Argv: argv})
	if err != nil {
		return 1, err
	}
	defer st.Close()
	tr, err := vmproto.CopyExecOutput(out, st)
	if err != nil {
		return 1, err
	}
	if tr.Error != "" {
		return tr.Exit, errors.New(tr.Error)
	}
	return tr.Exit, nil
}

func (h *tugVMHost) status() (vmproto.Status, error) {
	var s vmproto.Status
	st, err := h.open(vmproto.Header{Op: vmproto.OpStatus})
	if err != nil {
		return s, err
	}
	defer st.Close()
	line, err := bufio.NewReader(st).ReadBytes('\n')
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(line, &s)
}

// stopGuest asks tug-init to stop k3s, sync and power off, then waits for the session to end.
func (h *tugVMHost) stopGuest(wait time.Duration) {
	if st, err := h.open(vmproto.Header{Op: vmproto.OpStop}); err == nil {
		_, _ = vmproto.CopyExecOutput(io.Discard, st)
		st.Close()
	}
	select {
	case <-h.session.CloseChan():
	case <-time.After(wait):
		log.Printf("vm %s: guest did not power off within %s; terminating", h.name, wait)
	}
}

// forward accepts local TCP connections and splices each into a guest address.
func (h *tugVMHost) forward(l net.Listener, guestAddr string) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			st, err := h.open(vmproto.Header{Op: vmproto.OpDial, Addr: guestAddr})
			if err != nil {
				return
			}
			defer st.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(st, c); _ = st.Close(); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, st); closeWriteConn(c); done <- struct{}{} }()
			<-done
			<-done
		}()
	}
}

func closeWriteConn(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// serveLocal starts the kube-API forward and the token-protected local API.
func (h *tugVMHost) serveLocal(o tugVMOptions, vmID string) (*tugVMState, error) {
	kubeL, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", o.KubePort))
	if err != nil {
		return nil, fmt.Errorf("kube-api forward: %w", err)
	}
	go h.forward(kubeL, "127.0.0.1:6443")

	apiL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	st := &tugVMState{
		Name:       o.Name,
		PID:        os.Getpid(),
		VMID:       vmID,
		API:        "http://" + apiL.Addr().String(),
		Token:      hex.EncodeToString(tok),
		Kubeconfig: filepath.Join(o.Home, "kubeconfig.yaml"),
		KubeAPI:    "https://" + kubeL.Addr().String(),
		Started:    time.Now().UTC(),
	}
	h.local = append(h.local, kubeL, apiL)
	go func() { _ = http.Serve(apiL, h.apiHandler(st)) }()
	go h.syncKubeconfig(st)
	return st, nil
}

// closeLocal releases the host forwards so the next VM in this process can bind them.
func (h *tugVMHost) closeLocal() {
	for _, c := range h.local {
		_ = c.Close()
	}
	h.local = nil
}

// serveGuestStreams accepts streams the guest opens (the Windows-binary bridge).
func (h *tugVMHost) serveGuestStreams() {
	for {
		st, err := h.session.Accept()
		if err != nil {
			return
		}
		go h.handleGuestStream(st)
	}
}

func (h *tugVMHost) handleGuestStream(st net.Conn) {
	defer st.Close()
	hdr, _, err := vmproto.ReadHeader(st)
	if err != nil {
		return
	}
	if hdr.Op != vmproto.OpHost || len(hdr.Argv) == 0 {
		_ = vmproto.WriteTrailer(st, vmproto.Trailer{Exit: 127, Error: "host bridge wants argv"})
		return
	}
	if !hostExecAllowed(hdr.Argv[0], h.shares) {
		_ = vmproto.WriteTrailer(st, vmproto.Trailer{Exit: 127, Error: "host refused " + hdr.Argv[0]})
		return
	}
	cmd := exec.Command(hdr.Argv[0], hdr.Argv[1:]...)
	cmd.Dir = filepath.Dir(hdr.Argv[0])
	cmd.Stdout = st
	cmd.Stderr = st
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		} else {
			_ = vmproto.WriteTrailer(st, vmproto.Trailer{Exit: 127, Error: runErr.Error()})
			return
		}
	}
	_ = vmproto.WriteTrailer(st, vmproto.Trailer{Exit: code})
}

// syncKubeconfig copies k3s's kubeconfig out of the guest once k3s writes it,
// pointing it at the host-side forward.
func (h *tugVMHost) syncKubeconfig(st *tugVMState) {
	src := "/var/lib/tug/k3s/kubeconfig.yaml"
	for i := 0; i < 600; i++ {
		var buf bytes.Buffer
		code, err := h.exec([]string{"/bin/cat", src}, &buf)
		if err == nil && code == 0 && strings.Contains(buf.String(), "server:") {
			cfg := strings.ReplaceAll(buf.String(), "https://127.0.0.1:6443", st.KubeAPI)
			cfg = strings.ReplaceAll(cfg, ": default", ": tunneltug-"+h.name)
			if err := os.WriteFile(st.Kubeconfig, []byte(cfg), 0o600); err != nil {
				log.Printf("vm %s: kubeconfig: %v", h.name, err)
				return
			}
			log.Printf("vm %s: k3s API ready, kubeconfig written", h.name)
			return
		}
		select {
		case <-h.session.CloseChan():
			return
		case <-time.After(time.Second):
		}
	}
	log.Printf("vm %s: k3s wrote no kubeconfig in 10 minutes (see /var/lib/tug/log/k3s.log)", h.name)
}

func (h *tugVMHost) apiHandler(st *tugVMState) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		gs, err := h.status()
		out := map[string]any{"vm": st.Name, "vm_id": st.VMID, "hello": h.hello, "kube_api": st.KubeAPI,
			"kubeconfig": st.Kubeconfig, "started": st.Started}
		if err != nil {
			out["guest_error"] = err.Error()
		} else {
			out["guest"] = gs
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	})
	mux.HandleFunc("/exec", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Argv []string `json:"argv"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Argv) == 0 {
			http.Error(w, "POST {\"argv\":[...]}", http.StatusBadRequest)
			return
		}
		fw := flushWriter{w: w}
		code, err := h.exec(req.Argv, fw)
		t := vmproto.Trailer{Exit: code}
		if err != nil {
			t.Error = err.Error()
		}
		_ = vmproto.WriteTrailer(fw, t)
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST", http.StatusMethodNotAllowed)
			return
		}
		fmt.Fprintf(w, "vm %s: stopping\n", st.Name)
		select {
		case <-h.stopped:
		default:
			close(h.stopped)
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tug-Token") != st.Token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

// ---- state file + client ----

// tugVMState is written to <home>/vm.json while `vm up` runs; the other verbs read it.
type tugVMState struct {
	Name       string    `json:"name"`
	PID        int       `json:"pid"`
	VMID       string    `json:"vm_id"`
	API        string    `json:"api"`
	Token      string    `json:"token"`
	Kubeconfig string    `json:"kubeconfig"`
	KubeAPI    string    `json:"kube_api"`
	Started    time.Time `json:"started"`
}

func readTugVMState(home string) (*tugVMState, error) {
	b, err := os.ReadFile(filepath.Join(home, "vm.json"))
	if err != nil {
		return nil, err
	}
	var st tugVMState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeTugVMState(home string, st *tugVMState) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := filepath.Join(home, "vm.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(home, "vm.json"))
}

func tugVMAPICall(st *tugVMState, method, path string, body io.Reader, out io.Writer) error {
	req, err := http.NewRequest(method, st.API+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Tug-Token", st.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("vm %s api: %w (is `tunneltug vm up` still running?)", st.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("vm %s api %s: %s %s", st.Name, path, resp.Status, strings.TrimSpace(string(b)))
	}
	_, err = io.Copy(out, resp.Body)
	return err
}

func tugVMAPIExec(st *tugVMState, argv []string, out io.Writer) (int, error) {
	body, _ := json.Marshal(map[string]any{"argv": argv})
	req, err := http.NewRequest(http.MethodPost, st.API+"/exec", bytes.NewReader(body))
	if err != nil {
		return 1, err
	}
	req.Header.Set("X-Tug-Token", st.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1, fmt.Errorf("vm %s api: %w", st.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return 1, fmt.Errorf("vm exec: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	t, err := vmproto.CopyExecOutput(out, resp.Body)
	if err != nil {
		return 1, err
	}
	if t.Error != "" {
		return t.Exit, errors.New(t.Error)
	}
	return t.Exit, nil
}
