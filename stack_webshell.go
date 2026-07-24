package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// Stack webshell targets the 0TrustOS container in product stack k8s images
// (FROM hub.tunneltug.com/0trust/gonode — product + gonode sidecar).
//
// Default session is the gonode Junos-style CLI (set/show/commit), not /bin/sh.
// Pass cmd=sh for a plain shell escape. Not busybox debug; not platform /shell UI.

// stackShellUpgrader: shell is meshTLS HTTPS only — allow mesh FQDN origins, not bare http localhost.
var stackShellUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		o := strings.TrimSpace(r.Header.Get("Origin"))
		if o == "" {
			// Same-origin mesh HTTPS often omits Origin on some clients.
			return requestIsMeshAuthFace(r)
		}
		u, err := url.Parse(o)
		if err != nil || u.Scheme != "https" {
			return false
		}
		host := normalizeHost(u.Host)
		if host == "127.0.0.1" || host == "localhost" || host == "::1" {
			return false
		}
		return isMeshTLSHostname(host) || requestIsMeshAuthFace(r)
	},
}

// stackShellPage is a Teleport-like browser terminal into the 0TrustOS k8s image.
const stackShellPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>TunnelTug · 0TrustOS shell · {{APP}}</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.min.css"/>
<style>
:root {
  --bg-app: #1e1f22;
  --bg-panel: #313338;
  --bg-elevated: #2b2d31;
  --text: #dbdee1;
  --text-muted: #949ba4;
  --text-heading: #f2f3f5;
  --brand: #5865f2;
  --link: #00a8fc;
  --online: #23a559;
  --danger: #f23f43;
  --radius: 8px;
}
* { box-sizing: border-box; }
html, body { height: 100%; margin: 0; }
body {
  background: var(--bg-app);
  color: var(--text);
  font-family: 'Segoe UI', system-ui, -apple-system, sans-serif;
  display: flex;
  flex-direction: column;
  min-height: 100vh;
}
.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  flex-wrap: wrap;
  padding: 0.75rem 1rem;
  background: var(--bg-panel);
  border-bottom: 1px solid #3f4147;
}
.brand {
  display: flex;
  align-items: center;
  gap: 0.65rem;
  color: var(--text-heading);
  font-weight: 600;
}
.mark {
  width: 32px; height: 32px; border-radius: var(--radius);
  background: linear-gradient(135deg, #5865f2, #00a8fc);
  display: inline-flex; align-items: center; justify-content: center;
  color: #fff; font-weight: 700; font-size: 0.85rem;
}
.meta { color: var(--text-muted); font-size: 0.9rem; }
.meta code {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  background: var(--bg-elevated);
  padding: 0.1em 0.35em;
  border-radius: 4px;
  color: var(--text);
}
.actions { display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; }
.chip {
  display: inline-flex; align-items: center; gap: 0.35rem;
  padding: 0.3rem 0.65rem; border-radius: 999px;
  background: var(--bg-elevated); color: var(--text); font-size: 0.85rem;
  border: none; cursor: pointer; text-decoration: none;
}
.chip:hover { filter: brightness(1.08); }
.chip a, a.chip { color: var(--link); text-decoration: none; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--text-muted); }
.dot.ok { background: var(--online); }
.dot.bad { background: var(--danger); }
#term-wrap {
  flex: 1;
  min-height: 0;
  padding: 0.5rem;
  background: #0c0d0e;
}
#terminal { width: 100%; height: 100%; }
.hint {
  padding: 0.4rem 1rem;
  color: var(--text-muted);
  font-size: 0.8rem;
  border-top: 1px solid #3f4147;
  background: var(--bg-panel);
}
</style>
</head>
<body>
  <div class="bar">
    <div class="brand">
      <span class="mark">TT</span>
      <span>gonode CLI</span>
    </div>
    <div class="meta">
      app <code id="app-name">{{APP}}</code>
      · pod <code id="pod-name">…</code>
      · ns <code>{{NS}}</code>
      · <span class="chip"><span class="dot" id="conn-dot"></span><span id="conn-label">connecting</span></span>
    </div>
    <div class="actions">
      <a class="chip" href="/">← stack</a>
      <button class="chip" type="button" id="btn-reconnect">Reconnect</button>
    </div>
  </div>
  <div id="term-wrap"><div id="terminal"></div></div>
  <div class="hint">Mesh DNS + meshTLS HTTPS only (WebAuthn cannot use http/localhost). Passkey+DBSC via /auth on this mesh host. Default: gonode Junos CLI. ?cmd=sh for /bin/sh.</div>
  <script src="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.min.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/lib/addon-fit.min.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/@xterm/addon-web-links@0.11.0/lib/addon-web-links.min.js"></script>
  <script>
(function () {
  const app = {{APP_JSON}};
  const cmd = {{CMD_JSON}};
  const term = new Terminal({
    cursorBlink: true,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
    fontSize: 14,
    theme: {
      background: '#0c0d0e',
      foreground: '#dbdee1',
      cursor: '#00a8fc',
      selectionBackground: '#5865f244',
    },
    allowProposedApi: true,
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.loadAddon(new WebLinksAddon.WebLinksAddon());
  term.open(document.getElementById('terminal'));
  fit.fit();

  const dot = document.getElementById('conn-dot');
  const label = document.getElementById('conn-label');
  const podEl = document.getElementById('pod-name');
  let ws = null;
  let disposed = false;

  function setStatus(ok, text) {
    dot.className = 'dot ' + (ok === true ? 'ok' : ok === false ? 'bad' : '');
    label.textContent = text;
  }

  function sendResize() {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    fit.fit();
    ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
  }

  function connect() {
    if (disposed) return;
    if (ws) {
      try { ws.close(); } catch (_) {}
      ws = null;
    }
    setStatus(null, 'connecting');
    term.writeln('\r\n\x1b[90m[tunneltug] connecting to ' + app + ' ΓÇª\x1b[0m');
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const q = new URLSearchParams({ app: app, cols: String(term.cols), rows: String(term.rows) });
    if (cmd) q.set('cmd', cmd);
    ws = new WebSocket(proto + '//' + location.host + '/shell/ws?' + q.toString());
    ws.binaryType = 'arraybuffer';

    ws.onopen = function () {
      setStatus(true, 'connected');
      sendResize();
      term.focus();
    };
    ws.onmessage = function (ev) {
      if (typeof ev.data === 'string') {
        try {
          const msg = JSON.parse(ev.data);
          if (msg && msg.type === 'meta') {
            if (msg.pod) podEl.textContent = msg.pod;
            if (msg.info) term.writeln('\x1b[90m[tunneltug] ' + msg.info + '\x1b[0m');
            return;
          }
          if (msg && msg.type === 'error') {
            setStatus(false, 'error');
            term.writeln('\r\n\x1b[31m[tunneltug] ' + (msg.message || 'error') + '\x1b[0m');
            return;
          }
        } catch (_) {
          term.write(ev.data);
          return;
        }
        term.write(ev.data);
        return;
      }
      term.write(new Uint8Array(ev.data));
    };
    ws.onclose = function (ev) {
      setStatus(false, 'disconnected');
      term.writeln('\r\n\x1b[90m[tunneltug] session closed' + (ev.reason ? ' ┬╖ ' + ev.reason : '') + '\x1b[0m');
    };
    ws.onerror = function () {
      setStatus(false, 'error');
    };
  }

  term.onData(function (data) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'stdin', data: data }));
    }
  });
  window.addEventListener('resize', function () { sendResize(); });
  document.getElementById('btn-reconnect').addEventListener('click', function () {
    term.clear();
    connect();
  });
  connect();
})();
  </script>
</body>
</html>
`

type shellClientMsg struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

type termSizeQueue struct {
	ch   chan remotecommand.TerminalSize
	once sync.Once
}

func newTermSizeQueue() *termSizeQueue {
	return &termSizeQueue{ch: make(chan remotecommand.TerminalSize, 8)}
}

func (q *termSizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

func (q *termSizeQueue) Push(cols, rows uint16) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	select {
	case q.ch <- remotecommand.TerminalSize{Width: cols, Height: rows}:
	default:
		// Drop oldest-style: non-blocking replace by draining one then push.
		select {
		case <-q.ch:
		default:
		}
		select {
		case q.ch <- remotecommand.TerminalSize{Width: cols, Height: rows}:
		default:
		}
	}
}

func (q *termSizeQueue) Close() {
	q.once.Do(func() { close(q.ch) })
}

// sanitizeShellApp allows k8s DNS-label app names only.
func sanitizeShellApp(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" || len(name) > 63 {
		return ""
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return ""
	}
	return strings.ReplaceAll(name, "_", "-")
}

// defaultGonodeCLI is the main operator shell: Junos-style CLI against the
// pod's gonode data dir. -no-zt avoids fighting the already-running daemon
// sidecar for mesh/DNS binds; conf/CLI still load from -data.
var defaultGonodeCLI = []string{
	"/usr/bin/gonode",
	"-cli",
	"-no-tui",
	"-data", "/var/lib/gonode",
	"-no-zt",
}

// resolveShellCmd maps ?cmd= to an argv for k8s exec.
// Empty / gonode / cli / junos → gonode Junos CLI (default).
// sh / shell / bash / /bin/sh → plain shell escape.
func resolveShellCmd(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	switch strings.ToLower(cmd) {
	case "", "gonode", "cli", "junos", "sidecar":
		return append([]string(nil), defaultGonodeCLI...)
	case "sh", "shell", "/bin/sh":
		return []string{"/bin/sh"}
	case "bash", "/bin/bash":
		return []string{"/bin/bash"}
	case "tui":
		// Full TUI when the browser term can host it; still shared data dir.
		return []string{"/usr/bin/gonode", "-tui", "-data", "/var/lib/gonode", "-no-zt"}
	}
	// Absolute path only — no shell metachar injection via argv0.
	if strings.ContainsAny(cmd, " \t\n\r;&|<>$`\\\"'") {
		return append([]string(nil), defaultGonodeCLI...)
	}
	if strings.HasPrefix(cmd, "/") {
		return []string{cmd}
	}
	return append([]string(nil), defaultGonodeCLI...)
}

// sanitizeShellCmd is the legacy name used by tests; prefers gonode CLI.
func sanitizeShellCmd(cmd string) []string {
	return resolveShellCmd(cmd)
}

func parseShellSize(r *http.Request) (cols, rows uint16) {
	cols, rows = 80, 24
	if v := r.URL.Query().Get("cols"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 && n < 1000 {
			cols = uint16(n)
		}
	}
	if v := r.URL.Query().Get("rows"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 && n < 500 {
			rows = uint16(n)
		}
	}
	return cols, rows
}

func stackShellHTML(app, ns, cmd string) string {
	page := stackShellPage
	page = strings.ReplaceAll(page, "{{APP}}", htmlEscapeText(app))
	page = strings.ReplaceAll(page, "{{NS}}", htmlEscapeText(ns))
	page = strings.ReplaceAll(page, "{{APP_JSON}}", mustJSONString(app))
	page = strings.ReplaceAll(page, "{{CMD_JSON}}", mustJSONString(cmd))
	return page
}

func htmlEscapeText(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(s)
}

func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func handleStackShellPage(status *stackStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		app := sanitizeShellApp(r.URL.Query().Get("app"))
		if app == "" {
			// Landing: pick first ready app or show chooser via query.
			status.mu.RLock()
			ns := status.ns
			apps := append([]map[string]any(nil), status.apps...)
			status.mu.RUnlock()
			if len(apps) == 0 {
				http.Error(w, "no stack apps; pass ?app=<name>", http.StatusBadRequest)
				return
			}
			// Prefer platform, then first Ready.
			for _, a := range apps {
				if fmt.Sprint(a["name"]) == "platform" && fmt.Sprint(a["phase"]) == "Ready" {
					app = "platform"
					break
				}
			}
			if app == "" {
				for _, a := range apps {
					if fmt.Sprint(a["phase"]) == "Ready" {
						app = sanitizeShellApp(fmt.Sprint(a["name"]))
						if app != "" {
							break
						}
					}
				}
			}
			if app == "" {
				app = sanitizeShellApp(fmt.Sprint(apps[0]["name"]))
			}
			_ = ns
		}
		if app == "" {
			http.Error(w, "invalid app name", http.StatusBadRequest)
			return
		}
		status.mu.RLock()
		ns := status.ns
		status.mu.RUnlock()
		cmd := strings.TrimSpace(r.URL.Query().Get("cmd"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, stackShellHTML(app, ns, cmd))
	}
}

func handleStackShellWS(status *stackStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app := sanitizeShellApp(r.URL.Query().Get("app"))
		if app == "" {
			http.Error(w, "app query required", http.StatusBadRequest)
			return
		}
		status.mu.RLock()
		cfg := status.restCfg
		client := status.client
		ns := status.ns
		known := false
		for i := range status.appsList {
			n := status.appsList[i].Name
			if n == app || strings.ReplaceAll(n, "_", "-") == app {
				known = true
				app = strings.ReplaceAll(n, "_", "-")
				break
			}
		}
		// Also allow names present in live status rows (already hyphenated).
		if !known {
			for _, row := range status.apps {
				if sanitizeShellApp(fmt.Sprint(row["name"])) == app {
					known = true
					break
				}
			}
		}
		status.mu.RUnlock()
		if !known {
			http.Error(w, "unknown stack app", http.StatusNotFound)
			return
		}
		if cfg == nil || client == nil {
			http.Error(w, "k8s client unavailable", http.StatusServiceUnavailable)
			return
		}

		conn, err := stackShellUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("webshell upgrade: %v", err)
			return
		}
		defer conn.Close()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		podName, err := findStackRunningPod(ctx, client, ns, app)
		if err != nil {
			_ = conn.WriteJSON(map[string]any{"type": "error", "message": err.Error()})
			return
		}

		// Only the 0TrustOS product container (gonode base image + product).
		container, info, err := ensureStackShellContainer(ctx, client, ns, podName)
		if err != nil {
			_ = conn.WriteJSON(map[string]any{"type": "error", "message": err.Error()})
			return
		}
		_ = conn.WriteJSON(map[string]any{
			"type":      "meta",
			"pod":       podName,
			"info":      info,
			"container": container,
			"target":    "0trustos",
		})

		cols, rows := parseShellSize(r)
		sizes := newTermSizeQueue()
		defer sizes.Close()
		sizes.Push(cols, rows)

		// Default: gonode Junos CLI. cmd=sh for /bin/sh escape.
		cmd := resolveShellCmd(r.URL.Query().Get("cmd"))
		_ = conn.WriteJSON(map[string]any{
			"type": "meta",
			"exec": cmd,
			"note": "gonode Junos CLI (default); ?cmd=sh for /bin/sh",
		})

		// Bridge WebSocket ↔ 0TrustOS container TTY (gonode CLI).
		stdinR, stdinW := io.Pipe()
		defer stdinR.Close()
		defer stdinW.Close()

		var writeMu sync.Mutex
		stdout := &wsWriter{conn: conn, mu: &writeMu}

		// Read client messages ΓåÆ stdin / resize.
		go func() {
			defer cancel()
			defer stdinW.Close()
			for {
				_, data, err := conn.ReadMessage()
				if err != nil {
					return
				}
				var msg shellClientMsg
				if err := json.Unmarshal(data, &msg); err != nil {
					// Raw bytes as stdin fallback.
					if _, err := stdinW.Write(data); err != nil {
						return
					}
					continue
				}
				switch strings.ToLower(msg.Type) {
				case "stdin", "input":
					if msg.Data == "" {
						continue
					}
					if _, err := stdinW.Write([]byte(msg.Data)); err != nil {
						return
					}
				case "resize":
					sizes.Push(msg.Cols, msg.Rows)
				case "ping":
					writeMu.Lock()
					_ = conn.WriteJSON(map[string]string{"type": "pong"})
					writeMu.Unlock()
				}
			}
		}()

		subject := stackAuthSubjectHeader(r)
		if err := execStackPod(ctx, cfg, client, ns, podName, container, cmd, stdinR, stdout, stdout, sizes); err != nil {
			writeMu.Lock()
			_ = conn.WriteJSON(map[string]any{"type": "error", "message": err.Error()})
			writeMu.Unlock()
			log.Printf("webshell exec %s/%s/%s subject=%s: %v", ns, podName, container, subject, err)
		} else if subject != "" {
			log.Printf("webshell session end %s/%s/%s subject=%s", ns, podName, container, subject)
		}
	}
}

type wsWriter struct {
	conn *websocket.Conn
	mu   *sync.Mutex
}

func (w *wsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Binary frames keep TTY control sequences intact.
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func findStackRunningPod(ctx context.Context, client kubernetes.Interface, ns, app string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("no kubernetes client")
	}
	app = sanitizeShellApp(app)
	if app == "" {
		return "", fmt.Errorf("invalid app")
	}
	list, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=" + app,
	})
	if err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}
	var fallback string
	for _, p := range list.Items {
		if p.DeletionTimestamp != nil {
			continue
		}
		if p.Status.Phase == corev1.PodRunning {
			// Prefer Ready.
			ready := false
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					ready = true
					break
				}
			}
			if ready {
				return p.Name, nil
			}
			if fallback == "" {
				fallback = p.Name
			}
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("no running pod for app %s in %s", app, ns)
}

// ensureStackShellContainer returns the 0TrustOS product container to exec into.
// Stack images are FROM hub.tunneltug.com/0trust/gonode (product + gonode sidecar
// in one container via 0trustos-entry). No busybox ephemeral debug — wrong target.
func ensureStackShellContainer(ctx context.Context, client kubernetes.Interface, ns, podName string) (container, info string, err error) {
	if client == nil {
		return "", "", fmt.Errorf("no kubernetes client")
	}
	pod, err := client.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("get pod: %w", err)
	}
	if len(pod.Spec.Containers) == 0 {
		return "", "", fmt.Errorf("pod %s has no containers", podName)
	}
	target := pod.Spec.Containers[0].Name
	// Prefer a container that looks like the 0TrustOS product face (name match / env).
	for _, c := range pod.Spec.Containers {
		if is0TrustOSContainer(c) {
			target = c.Name
			break
		}
	}
	if !is0TrustOSPod(pod, target) {
		return "", "", fmt.Errorf(
			"pod %s/%s is not a 0TrustOS k8s image (need FROM hub.tunneltug.com/0trust/gonode + OTRUST_* inject); shell only targets the gonode sidecar container",
			ns, podName,
		)
	}
	return target, fmt.Sprintf("0TrustOS %s/%s container=%s (gonode sidecar image)", ns, podName, target), nil
}

// is0TrustOSContainer reports product containers from the 0TrustOS (gonode) base image.
func is0TrustOSContainer(c corev1.Container) bool {
	img := strings.ToLower(c.Image)
	if strings.Contains(img, "tunneltug/engine") || strings.Contains(img, "tunneltug/barge") {
		return false
	}
	for _, e := range c.Env {
		switch e.Name {
		case "OTRUST_PRODUCT", "PRODUCT_BIN", "GONODE_DATA":
			return true
		}
	}
	// Base image or product layered FROM gonode.
	return strings.Contains(img, "0trust/gonode") ||
		(strings.Contains(img, "/0trust/") && !strings.Contains(img, "tunneltug/"))
}

// is0TrustOSPod true when the target container is our 0TrustOS product image.
func is0TrustOSPod(pod *corev1.Pod, container string) bool {
	if pod == nil {
		return false
	}
	for _, c := range pod.Spec.Containers {
		if c.Name != container {
			continue
		}
		if is0TrustOSContainer(c) {
			return true
		}
	}
	for _, st := range pod.Status.ContainerStatuses {
		if st.Name != container {
			continue
		}
		img := strings.ToLower(st.Image + " " + st.ImageID)
		if strings.Contains(img, "0trust/gonode") || strings.Contains(img, "0trustos") {
			return true
		}
		// Product tags FROM gonode still report product image name (e.g. 0trust/platform).
		if strings.Contains(img, "/0trust/") && !strings.Contains(img, "tunneltug/engine") {
			return true
		}
	}
	return false
}

func execStackPod(
	ctx context.Context,
	cfg *rest.Config,
	client kubernetes.Interface,
	ns, pod, container string,
	command []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	sizes remotecommand.TerminalSizeQueue,
) error {
	if cfg == nil {
		return fmt.Errorf("no rest config")
	}
	if client == nil {
		return fmt.Errorf("no kubernetes client")
	}
	// Streaming: do not set a short overall HTTP timeout on the rest config copy.
	cfg = rest.CopyConfig(cfg)
	cfg.Timeout = 0

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(cfg, http.MethodPost, req.URL())
	if err != nil {
		return fmt.Errorf("spdy executor: %w", err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Cap idle shell sessions (Teleport-like safety for lab console).
	go func() {
		t := time.NewTimer(2 * time.Hour)
		defer t.Stop()
		select {
		case <-streamCtx.Done():
		case <-t.C:
			cancel()
		}
	}()

	return executor.StreamWithContext(streamCtx, remotecommand.StreamOptions{
		Stdin:             stdin,
		Stdout:            stdout,
		Stderr:            stderr,
		Tty:               true,
		TerminalSizeQueue: sizes,
	})
}

func mountStackWebshell(mux *http.ServeMux, status *stackStatus) {
	// Exact paths so /shell/ws is not captured by a /shell/ subtree handler.
	// Both require 0Trust auth_proxy (passkey + DBSC) when -stack-auth-proxy (default).
	mux.HandleFunc("GET /shell", withStackAuth(handleStackShellPage(status)))
	mux.HandleFunc("GET /shell/ws", withStackAuth(handleStackShellWS(status)))
	mux.HandleFunc("GET /shell/{$}", withStackAuth(handleStackShellPage(status)))
}
