package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Stack shell auth_proxy for WebAuthn + DBSC.
//
// WebAuthn cannot run on http:// or localhost. Operator auth and gonode shell
// are only valid on the meshTLS face with mesh DNS FQDNs:
//
//	https://stack.{mesh-zone}:{mesh-tls-port}/auth
//	https://stack.{mesh-zone}:{mesh-tls-port}/shell?app={name}
//
// Mesh DNS (VPI / system resolver) must resolve the operator FQDN → edge IP.
// TLS leaves come from platform Mesh CA via the same path as gonode
// (nameservice provision + /api/v1/pki/certs/active) — not a local invent.
// Browsers need the platform mesh-root in trust for WebAuthn RP ID on that FQDN.
//
// Loopback http://127.0.0.1:4070 is status/gateway only — /auth and /shell
// redirect to the mesh HTTPS origin.

type stackIDPSession struct {
	Subject       string `json:"sub"`
	Valid         bool   `json:"valid"`
	DBSCBound     bool   `json:"dbsc_bound"`
	HardwareBound bool   `json:"hardware_bound"`
}

func stackAuthEnabled() bool {
	return stackAuthProxy != nil && *stackAuthProxy
}

func stackAuthIdentityBase() string {
	if stackAuthPlatform != nil {
		if v := strings.TrimRight(strings.TrimSpace(*stackAuthPlatform), "/"); v != "" {
			return v
		}
	}
	if meshPlatform != nil {
		if v := strings.TrimRight(strings.TrimSpace(*meshPlatform), "/"); v != "" {
			return v
		}
	}
	return "https://0trust.cloud"
}

// stackOperatorMeshHost is the mesh DNS label for the stack operator face
// (auth_proxy + gonode shell). WebAuthn RP ID / SNI use this FQDN.
func stackOperatorMeshHost() string {
	return meshPrivateName("stack")
}

func stackMeshTLSPort() string {
	if meshTLSPort != nil {
		if p := strings.TrimSpace(*meshTLSPort); p != "" {
			return p
		}
	}
	return "4073"
}

// stackMeshOperatorOrigin is the only valid public origin for auth + shell.
// Always HTTPS on a mesh FQDN — never http://127.0.0.1.
func stackMeshOperatorOrigin() string {
	host := stackOperatorMeshHost()
	port := stackMeshTLSPort()
	if port == "" || port == "443" {
		return "https://" + host
	}
	return fmt.Sprintf("https://%s:%s", host, port)
}

// stackAuthPublicOrigin is the browser-facing origin for cookies, return_to, and
// auth_proxy X-Forwarded-Host. Mesh DNS HTTPS only.
func stackAuthPublicOrigin() string {
	return stackMeshOperatorOrigin()
}

// Deprecated name kept as alias for call sites mid-migration.
func stackDashPublicOrigin() string {
	return stackAuthPublicOrigin()
}

func stackAuthLoginURL(returnTo string) string {
	entry := stackAuthPublicOrigin() + "/auth"
	returnTo = strings.TrimSpace(returnTo)
	if returnTo == "" {
		return entry
	}
	// Absolute return_to on mesh origin when relative.
	if strings.HasPrefix(returnTo, "/") {
		returnTo = stackAuthPublicOrigin() + returnTo
	}
	return entry + "?return_to=" + url.QueryEscape(returnTo)
}

// requestIsMeshAuthFace is true when the request arrived on meshTLS with a mesh
// hostname (not loopback HTTP). WebAuthn / shell only run here.
func requestIsMeshAuthFace(r *http.Request) bool {
	if r == nil {
		return false
	}
	// TLS required (meshTLS gateway sets r.TLS).
	if r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return false
	}
	host := normalizeHost(r.Host)
	if host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return false
	}
	zone := strings.ToLower(strings.TrimSpace(*meshZone))
	if zone == "" {
		zone = "tunneltug.tunnel"
	}
	if host == zone || strings.HasSuffix(host, "."+zone) {
		return true
	}
	// Other private product TLDs used by meshTLS.
	for _, s := range []string{".mesh", ".tunnel", ".social"} {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return isMeshTLSHostname(host)
}

func stackSessionID(r *http.Request) string {
	if r == nil {
		return ""
	}
	if c, err := r.Cookie("session_id"); err == nil && strings.TrimSpace(c.Value) != "" {
		return strings.TrimSpace(c.Value)
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if v := strings.TrimSpace(h[len("Bearer "):]); v != "" {
			return v
		}
	}
	if t := strings.TrimSpace(r.Header.Get("X-0Trust-Session")); t != "" {
		return t
	}
	return ""
}

func stackTrustedMeshIdentity(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	if strings.TrimSpace(r.Header.Get("X-0Trust-DBSC")) != "bound" {
		return "", false
	}
	sub := strings.TrimSpace(r.Header.Get("X-0Trust-Subject"))
	if sub == "" || len(sub) > 128 {
		return "", false
	}
	return sub, true
}

func stackLookupIDPSession(r *http.Request) (stackIDPSession, int) {
	sid := stackSessionID(r)
	if sid == "" {
		return stackIDPSession{}, http.StatusUnauthorized
	}
	base := stackAuthIdentityBase()
	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/idp/session", nil)
	if err != nil {
		return stackIDPSession{}, http.StatusBadGateway
	}
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sid})
	for _, h := range []string{"Sec-Session-Response", "Sec-Session-Challenge", "Sec-Session-Id"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return stackIDPSession{}, http.StatusBadGateway
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return stackIDPSession{}, resp.StatusCode
	}
	var st stackIDPSession
	if json.Unmarshal(body, &st) != nil {
		return stackIDPSession{}, http.StatusBadGateway
	}
	return st, http.StatusOK
}

func stackSessionDBSCBound(st stackIDPSession) bool {
	return st.Valid && (st.DBSCBound || st.HardwareBound)
}

func stackPrefersHTML(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Header.Get("Upgrade") != "" {
		return false
	}
	a := r.Header.Get("Accept")
	return strings.Contains(a, "text/html") || a == "" || strings.Contains(a, "*/*")
}

// redirectToMeshAuthFace sends browsers from loopback/HTTP to mesh HTTPS.
func redirectToMeshAuthFace(w http.ResponseWriter, r *http.Request) {
	path := "/"
	if r != nil && r.URL != nil {
		path = r.URL.RequestURI()
		if path == "" {
			path = "/"
		}
	}
	dest := stackAuthPublicOrigin() + path
	if stackPrefersHTML(r) {
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", dest)
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, fmt.Sprintf(
		`{"error":"mesh_https_required","message":"WebAuthn/DBSC and gonode shell require mesh DNS + meshTLS HTTPS — not http:// or localhost","url":%q}`,
		dest,
	))
}

// requireStackAuth enforces mesh HTTPS face + 0Trust auth_proxy (passkey + DBSC).
func requireStackAuth(w http.ResponseWriter, r *http.Request) (subject string, ok bool) {
	if !stackAuthEnabled() {
		return "auth-proxy-off", true
	}
	// Never run WebAuthn/shell on loopback HTTP — mesh DNS + meshTLS only.
	if !requestIsMeshAuthFace(r) {
		redirectToMeshAuthFace(w, r)
		return "", false
	}
	if sub, ok := stackTrustedMeshIdentity(r); ok {
		return sub, true
	}
	st, code := stackLookupIDPSession(r)
	returnTo := r.URL.RequestURI()
	login := stackAuthLoginURL(returnTo)

	if code == http.StatusUnauthorized || !st.Valid {
		w.Header().Set("X-0Trust-Auth", "required")
		if stackPrefersHTML(r) {
			http.Redirect(w, r, login, http.StatusSeeOther)
			return "", false
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("WWW-Authenticate", `Bearer realm="0trust"`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, fmt.Sprintf(
			`{"error":"unauthorized","message":"0Trust auth_proxy on mesh HTTPS required (passkey + DBSC)","login":%q}`,
			login,
		))
		return "", false
	}
	if !stackSessionDBSCBound(st) {
		w.Header().Set("X-0Trust-DBSC", "required")
		if stackPrefersHTML(r) {
			// Return to mesh HTTPS shell after device bind (not localhost).
			ret := stackAuthPublicOrigin() + returnTo
			dest := stackAuthIdentityBase() + "/auth?dbsc=required&return_to=" + url.QueryEscape(ret)
			http.Redirect(w, r, dest, http.StatusFound)
			return "", false
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"dbsc_required","message":"DBSC device-bound session required after passkey login"}`)
		return "", false
	}
	subject = strings.TrimSpace(st.Subject)
	if subject == "" || len(subject) > 128 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthorized","message":"session missing subject"}`)
		return "", false
	}
	return subject, true
}

func withStackAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub, ok := requireStackAuth(w, r)
		if !ok {
			return
		}
		r.Header.Set("X-0Trust-Subject", sub)
		next(w, r)
	}
}

// withMeshAuthFaceOnly redirects loopback HTTP auth/shell hits to mesh HTTPS
// before the auth_proxy reverse proxy runs (WebAuthn cannot use http/localhost).
func withMeshAuthFaceOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requestIsMeshAuthFace(r) {
			redirectToMeshAuthFace(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mountStackAuthProxy registers identity paths reverse-proxied to the platform.
// Served only on the meshTLS + mesh DNS face (see withMeshAuthFaceOnly).
func mountStackAuthProxy(mux *http.ServeMux) {
	if !stackAuthEnabled() {
		return
	}
	planeURL := stackAuthIdentityBase()
	cloud, err := url.Parse(planeURL)
	if err != nil || planeURL == "" {
		log.Printf("[stack-auth] invalid identity plane %q — auth_proxy paths disabled", planeURL)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(cloud)
	orig := proxy.Director
	logicalHost := strings.ToLower(cloud.Hostname())
	meshOrigin := stackAuthPublicOrigin()
	meshHost := stackOperatorMeshHost()

	proxy.Director = func(req *http.Request) {
		// Prefer the actual mesh Host the browser used (SNI / WebAuthn RP ID).
		public := normalizeHost(req.Host)
		if public == "" || public == "127.0.0.1" || public == "localhost" {
			public = meshHost
		}
		orig(req)
		req.URL.Scheme = cloud.Scheme
		req.URL.Host = cloud.Host
		req.Host = cloud.Host
		if h, p, err := net.SplitHostPort(req.Host); err == nil && p != "" {
			req.Host = h
		}
		req.Header.Set("X-0Trust-Tunnel", "1")
		req.Header.Set("X-0Trust-Services", public)
		req.Header.Set("X-0Trust-Stack-Auth", "1")
		req.Header.Set("X-0Trust-Mesh-Host", public)
		req.Header.Set("X-Forwarded-Host", public)
		req.Header.Set("X-Forwarded-Proto", "https")
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		rewriteCloudResponse(resp, logicalHost, meshOrigin)
		// Cookie Domain → mesh zone so session sticks on stack.{zone} HTTPS.
		zone := strings.ToLower(strings.TrimSpace(*meshZone))
		if zone == "" {
			zone = "tunneltug.tunnel"
		}
		if cookies := resp.Header["Set-Cookie"]; len(cookies) > 0 {
			for i, c := range cookies {
				c = strings.ReplaceAll(c, "Domain="+logicalHost, "Domain="+zone)
				c = strings.ReplaceAll(c, "domain="+logicalHost, "Domain="+zone)
				c = strings.ReplaceAll(c, "Domain=."+logicalHost, "Domain=."+zone)
				// Secure cookies for HTTPS mesh face.
				if !strings.Contains(strings.ToLower(c), "secure") {
					c += "; Secure"
				}
				resp.Header["Set-Cookie"][i] = c
			}
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[stack-auth] %s %s via %s: %v", r.Method, r.URL.Path, planeURL, err)
		http.Error(w, "identity plane unreachable (auth_proxy)", http.StatusBadGateway)
	}

	authHandler := withMeshAuthFaceOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isCloudAuthPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/auth/logout" {
			http.SetCookie(w, &http.Cookie{
				Name:     "session_id",
				Value:    "",
				Path:     "/",
				MaxAge:   -1,
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteLaxMode,
			})
		}
		proxy.ServeHTTP(w, r)
	}))

	for _, p := range []string{
		"/auth", "/auth/",
		"/samln/",
		"/.well-known/",
		"/StartSession",
		"/RefreshEndpoint",
		"/api/v1/idp/session",
	} {
		mux.Handle(p, authHandler)
	}
	log.Printf("[stack-auth] auth_proxy WebAuthn/DBSC on mesh HTTPS %s/auth  (identity %s; Mesh CA FQDN TLS via gonode path)",
		meshOrigin, planeURL)
}

func stackAuthStatusJSON() map[string]any {
	return map[string]any{
		"enabled":    stackAuthEnabled(),
		"platform":   stackAuthIdentityBase(),
		"mesh_host":  stackOperatorMeshHost(),
		"mesh_origin": stackAuthPublicOrigin(),
		"login":      stackAuthPublicOrigin() + "/auth",
		"shell":      stackAuthPublicOrigin() + "/shell?app={name}",
		"shell_gate": "mesh_dns+meshtls+auth_proxy+dbsc",
		"note": "WebAuthn/DBSC need Mesh CA FQDN TLS (gonode provision path). Sign in on mesh HTTPS FQDN — never http/localhost",
	}
}

func stackAuthSubjectHeader(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-0Trust-Subject"))
}
