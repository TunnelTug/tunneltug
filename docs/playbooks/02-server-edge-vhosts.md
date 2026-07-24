# 02 — Server edge + product vhosts

## Modes

| Mode | Role |
|------|------|
| `-mode server` | Public ingress + QUIC control for tunnel clients (or backend behind LB) |
| `-routing subdomain` | Default: `*.domain` / host-based tunnels |
| `-routing direct` | Single shared tunnel; apex-focused; still supports **vhosts** for co-hosted products |

## Production direct multi-vhost edge (0Trust pattern)

One process owns **:80 / :443** and terminates ACME for many product faces; reverse-proxies to local or backhaul upstreams.

```yaml
# -vhosts file (see config/vhosts.example.yaml)
platform_url: https://0trust.cloud
platform_upstream: https://0trust.cloud
cloud_domain: 0trust.cloud
cloud_backhaul: "http://<platform-ip>"

acme_domains:
  - product.example
  - www.product.example

vhosts:
  - domain: product.example
    upstream: http://127.0.0.1:3082   # or cloud://8443 / http://ip:8443
    auth_proxy: false
    wildcard_subdomains: false
```

| Field | Meaning |
|-------|---------|
| `upstream` | `http://…`, `https://…`, or `cloud://PORT` (uses `cloud_backhaul`) |
| `auth_proxy` | Identity-aware proxy toward platform when true |
| `wildcard_subdomains` | Single-label `app.domain` only when true; leave **false** on tunnel product domains so user tunnels are not stolen |
| `acme_domains` | Explicit SANs; also every `vhosts[].domain` is collected for ACME |

### Deploy steps

1. Ensure A records for each vhost domain → this edge IP.
2. Place vhosts YAML on the host; point unit `-vhosts` at it.
3. Put every public name in `-subalt` **or** rely on vhosts ACME collection (both is fine; duplicates are de-duped).
4. Restart **only** the TunnelTug direct unit.
5. Confirm:  
   `[vhost] loaded N product vhost(s)` and each domain listed.
6. ACME: follow [01-acme-tls.md](01-acme-tls.md). Do not dual-issue.

### Content on another host

Upstream may be platform/cloud while TLS stays local:

```yaml
- domain: 0trustos.com
  upstream: http://206.189.239.120:8443
  auth_proxy: false
  wildcard_subdomains: false
```

Platform answers `Host: 0trustos.com` (e.g. redirect to `/os`).  
**Certificate is still TunnelTug’s job.**

## Control plane ports

| Port | Use |
|------|-----|
| `-public` (443) | HTTPS + HTTP/3 |
| `-control` | QUIC tunnel clients (`ALPN: tunneltug`, optional mesh overlay) |
| 80 | ACME HTTP-01 only (when `-acme-http`) |

## Snapshots (optional)

| Flag | Purpose |
|------|---------|
| `-snapshot-dir` | Persist tunnel state |
| `-snapshot-restore` | Load on start |
| `-snapshot-on-shutdown` | Write on stop |
| `-snapshot-interval` | Periodic |

Use for barge/server reconnect hints; not a substitute for ACME cache backup.

## Register with LB (backend server)

When this server is behind `-mode lb`:

```bash
-register-lb <lb-host:port>
-register-host <this-node-ip>
-register-fleet-id <fleet>
```

Heartbeats prune dead backends on the LB.
