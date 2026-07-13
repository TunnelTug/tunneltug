package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// mountMeshHandlers attaches built-in mesh registry endpoints to a ServeMux.
func mountMeshHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/_tunneltug/mesh/status", handleMeshStatus)
	mux.HandleFunc("/_tunneltug/mesh/lookup", handleMeshLookup)
	mux.HandleFunc("/_tunneltug/mesh/register", handleMeshRegister)
	mux.HandleFunc("/_tunneltug/mesh/deregister", handleMeshDeregister)
}

func handleMeshStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth := getGlobalMeshAuthority()
	w.Header().Set("Content-Type", "application/json")
	if auth == nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled": false,
			"hint":    "start server/lb with -mesh to enable built-in secure_dns + secure_registrar",
		})
		return
	}
	_ = json.NewEncoder(w).Encode(auth.Status())
}

func handleMeshLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth := getGlobalMeshAuthority()
	if auth == nil {
		http.Error(w, "mesh authority not enabled", http.StatusServiceUnavailable)
		return
	}
	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	if domain == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}
	// Ownership metadata when present.
	if meta, err := auth.registrar.GetOwnership(domain); err == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"domain":   meta.Domain,
			"owner":    meta.OwnerPub,
			"parent":   meta.ParentDomain,
			"registered_at": meta.RegisteredAt,
		})
		return
	}
	records, err := auth.Lookup(domain)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"domain":  domain,
		"records": records,
	})
}

type meshRegisterRequest struct {
	HostID string `json:"host_id"`
	EdgeIP string `json:"edge_ip"`
	Value  string `json:"value"` // alias for edge_ip
}

func handleMeshRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !authorizeMeshRequest(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	auth := getGlobalMeshAuthority()
	if auth == nil {
		http.Error(w, "mesh authority not enabled", http.StatusServiceUnavailable)
		return
	}

	var req meshRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Allow form-encoded fallbacks.
		_ = r.ParseForm()
		req.HostID = r.FormValue("host_id")
		req.EdgeIP = r.FormValue("edge_ip")
		if req.EdgeIP == "" {
			req.EdgeIP = r.FormValue("value")
		}
	}
	if req.EdgeIP == "" {
		req.EdgeIP = req.Value
	}
	hostID := strings.TrimSpace(req.HostID)
	if hostID == "" {
		hostID = meshHostID()
	}
	name, err := auth.PublishTunnel(hostID, strings.TrimSpace(req.EdgeIP))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "registered",
		"host_id": hostID,
		"domain":  name,
		"edge_ip": firstNonEmpty(strings.TrimSpace(req.EdgeIP), auth.edgeIP),
	})
}

func handleMeshDeregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !authorizeMeshRequest(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	auth := getGlobalMeshAuthority()
	if auth == nil {
		http.Error(w, "mesh authority not enabled", http.StatusServiceUnavailable)
		return
	}
	var req meshRegisterRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.HostID == "" {
		_ = r.ParseForm()
		req.HostID = r.FormValue("host_id")
	}
	hostID := strings.TrimSpace(req.HostID)
	if hostID == "" {
		http.Error(w, "host_id is required", http.StatusBadRequest)
		return
	}
	auth.UnpublishTunnel(hostID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "deregistered",
		"host_id": hostID,
		"domain":  meshPrivateName(hostID),
	})
}

func authorizeMeshRequest(r *http.Request) bool {
	token := strings.TrimSpace(r.Header.Get("X-Tunnel-Token"))
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("Authorization"))
		token = strings.TrimPrefix(token, "Bearer ")
		token = strings.TrimSpace(token)
	}
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	return tokensEqual(token, *authToken)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
