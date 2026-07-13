package main

import (
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func isCloudAuthPath(path string) bool {
	switch {
	case path == "/StartSession", path == "/RefreshEndpoint":
		return true
	case strings.HasPrefix(path, "/samln/"):
		return true
	case path == "/auth", strings.HasPrefix(path, "/auth/"):
		return true
	case strings.HasPrefix(path, "/.well-known/"):
		return true
	case path == "/api/v1/idp/session":
		return true
	}
	return false
}

func identityPlaneURL(id VHostIdentity) string {
	if u := strings.TrimSpace(id.PlatformURL); u != "" {
		return strings.TrimRight(u, "/")
	}
	if u := strings.TrimSpace(id.PlatformUpstream); u != "" {
		return strings.TrimRight(u, "/")
	}
	base := strings.TrimRight(strings.TrimSpace(id.CloudBackhaul), "/")
	if base != "" {
		if strings.HasPrefix(base, "http://") || strings.HasPrefix(base, "https://") {
			return base + ":8443"
		}
		return "http://" + base + ":8443"
	}
	return ""
}

func logicalCloudHost(id VHostIdentity) string {
	return strings.ToLower(strings.TrimSpace(id.CloudDomain))
}

func rewriteCloudResponse(resp *http.Response, cloudHost, publicOrigin string) {
	publicURL, _ := url.Parse(publicOrigin)
	publicHost := ""
	if publicURL != nil {
		publicHost = publicURL.Host
		if h, _, err := net.SplitHostPort(publicHost); err == nil {
			publicHost = h
		}
	}
	cloudHost = strings.ToLower(strings.TrimSpace(cloudHost))
	if cloudHost == "" || publicHost == "" {
		return
	}

	if loc := resp.Header.Get("Location"); loc != "" {
		loc = strings.ReplaceAll(loc, "https://"+cloudHost, publicOrigin)
		loc = strings.ReplaceAll(loc, "http://"+cloudHost, publicOrigin)
		resp.Header.Set("Location", loc)
	}
	for i, c := range resp.Header["Set-Cookie"] {
		c = strings.ReplaceAll(c, "Domain="+cloudHost, "Domain="+publicHost)
		c = strings.ReplaceAll(c, "domain="+cloudHost, "domain="+publicHost)
		resp.Header["Set-Cookie"][i] = c
	}
}

// wrapVHostWithCloudAuth proxies identity paths to the platform when auth_proxy is set.
func wrapVHostWithCloudAuth(id VHostIdentity, vh VHostConfig, app http.Handler) http.Handler {
	if !vh.AuthProxy {
		return app
	}
	planeURL := identityPlaneURL(id)
	cloud, err := url.Parse(planeURL)
	if err != nil || planeURL == "" {
		return app
	}
	proxy := httputil.NewSingleHostReverseProxy(cloud)
	orig := proxy.Director
	logicalHost := logicalCloudHost(id)
	if logicalHost == "" {
		logicalHost = cloud.Host
		if h, _, err := net.SplitHostPort(logicalHost); err == nil {
			logicalHost = h
		}
	}

	proxy.Director = func(req *http.Request) {
		publicHost := normalizeHost(req.Host)
		if publicHost == "" {
			publicHost = normalizeHost(vh.Domain)
		}
		orig(req)
		req.URL.Scheme = cloud.Scheme
		req.URL.Host = cloud.Host
		req.Host = cloud.Host
		if h, p, err := net.SplitHostPort(req.Host); err == nil && p != "" {
			req.Host = h
		}
		req.Header.Set("X-0Trust-Tunnel", "1")
		req.Header.Set("X-0Trust-Services", publicHost)
		if req.Header.Get("X-Forwarded-Host") == "" {
			req.Header.Set("X-Forwarded-Host", publicHost)
		}
		if req.Header.Get("X-Forwarded-Proto") == "" {
			req.Header.Set("X-Forwarded-Proto", "https")
		}
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		publicOrigin := "https://" + normalizeHost(vh.Domain)
		rewriteCloudResponse(resp, logicalHost, publicOrigin)
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if !*quiet {
			log.Printf("[vhost-auth] %s %s via %s: %v", r.Method, r.URL.Path, planeURL, err)
		}
		http.Error(w, "identity plane unreachable", http.StatusBadGateway)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isCloudAuthPath(r.URL.Path) {
			proxy.ServeHTTP(w, r)
			return
		}
		app.ServeHTTP(w, r)
	})
}
