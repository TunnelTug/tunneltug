# TunnelTug architectures

This guide describes **deployment shapes** you can build with TunnelTug — from a single laptop tunnel to multi-region anycast with real-time kernel replication.

Config surfaces:

| Surface | Use |
|---------|-----|
| **Flags / env** | Simple single-process runs |
| **Stack YAML** (`-stack-config`) | Product barges in one cluster |
| **Site YAML / Tugconf** (`-config`) | Multi-PoP global sites |
| **Hub config builder** | `https://hub.tunneltug.com` — modal per image that emits YAML + Tugconf |

Interactive builder: open any product on [hub.tunneltug.com](https://hub.tunneltug.com), click **Configure**, scale/link services, copy **YAML** or **Tugconf**.

---

## Principles

1. **Ingress scales independently of data** — more edges for traffic; kernel mesh for consistency.
2. **Local embeds stay primary** — products serve from local DBs; kernel is a **replication peer**, not a prefer-remote store.
3. **Domains are never hardcoded** — set `domain` / `public_url` in YAML or Tugconf.
4. **Secrets stay out of files** — use `token_env: TUNNELTUG_TOKEN` (or `-token`).

---

## Architecture catalog

### A1 — Dev tunnel (single process)

Expose one local app with self-signed TLS.

```text
Browser → server (:public) → QUIC control → client → 127.0.0.1:3000
```

```bash
tunneltug -mode server -dev -domain localhost -token "$TOKEN" -public 8443
tunneltug -mode client -server 127.0.0.1 -local 3000 -subdomain myapp -token "$TOKEN" -insecure
```

**When:** local demos, agent workstations.  
**Resilience:** none (single box).

---

### A2 — Production direct apex

One shared tunnel on the apex domain (no multi-tenant subdomains).

```bash
tunneltug -mode server -prod -routing direct -domain example.com -email ops@example.com -token "$TOKEN"
tunneltug -mode client -prod -routing direct -domain example.com -local 3000 -token "$TOKEN"
```

**When:** single public app.  
**Resilience:** process supervisor / host reboot only.

---

### A3 — Subdomain multi-tenant edge

Wildcard (or per-host) ACME; many clients, one server.

```text
*.example.com → server/LB → barges → per-tenant local apps
```

**When:** SaaS-style tunnels.  
**Resilience:** add **A4** (LB + barges) for HA.

---

### A4 — LB + barge fleet (horizontal tunnel HA)

Public LB fans out to many tunnel server replicas (process or **k3s**).

```text
Internet → LB (sticky/RR) → barge pods (engine image) ⇄ clients
                ↑ dynamic register / heartbeat
```

```bash
tunneltug -mode lb -prod -domain example.com -token "$TOKEN" -lb-dynamic
tunneltug -mode barge -barge-runtime k3s -barge-replicas 3 \
  -barge-lb <lb-public>:443 -token "$TOKEN" -domain example.com
```

**When:** production tunnel capacity and rolling updates.  
**Resilience:** N barge replicas; LB prunes dead backends; k3s STS survives controller restarts.

---

### A5 — Product stack (single region)

Self-contained k3s Deployments for apps (Williwaw, Social, kernel, …).

```bash
tunneltug -mode stack -stack-config config/stack.example.yaml -token "$TOKEN"
# or site-less product list:
tunneltug -mode stack -stack-products williwaw,social,ultimate_db -token "$TOKEN"
```

```text
Users → ingress/vhost → product Services
                              ↕ (optional)
                    ultimate_db / keystore kernel (replication peers in-region)
```

**When:** run 0Trust product images from the hub in one cluster.  
**Resilience:** Deployment replicas; kernel barge for in-cluster replication peers (local embeds still primary).

---

### A6 — Multi-PoP global ingress + kernel mesh (real-time sync)

Site config with `pops[]` and `kernel_mesh`. Each PoP has local ingress **and** local product data; kernel peers sync in real time.

```text
Users worldwide
   ├─ anycast/LB PoP SFO → products + local embeds ──┐
   └─ anycast/LB PoP AMS → products + local embeds ──┼→ kernel full-mesh / hub-spoke
                                                     │   (NetworkTransport /kernel/*)
```

```bash
tunneltug -config config/site.example.yaml -pop sfo -config-check
tunneltug -config config/site.example.yaml -pop sfo -mode stack -token "$TOKEN"
# AMS host:
tunneltug -config config/site.example.yaml -pop ams -mode stack -token "$TOKEN"
```

`kernel_mesh.mode`:

| Mode | Peers |
|------|--------|
| `full-mesh` | Every PoP ↔ every other PoP with `kernel.*.url` |
| `hub-spoke` | Spokes ↔ `hub_pop` only |
| `manual` | Only explicit `peers:` strings |

**When:** global scale-out with consistent service data.  
**Resilience:** PoP isolation + health-gated anycast (A7) + multi-replica barges (A4).

---

### A7 — Anycast edge (BGP health-gated)

Split-horizon DNS + BGP announce/withdraw. Unhealthy PoP withdraws so traffic shifts.

```bash
tunneltug -mode anycast -anycast-config config/anycast.example.yaml
# or sidecar: -mode server -anycast -anycast-config ...
```

**When:** multi-region DNS/anycast VIP face.  
**Resilience:** ROV + BGPsec (fail-closed), health probes, automatic withdraw.

---

### A8 — Product vhosts + tunnel subdomains

Edge co-hosts apex product apps and user tunnel subdomains.

```yaml
# vhosts.yaml
vhosts:
  - domain: app.example.com
    upstream: http://127.0.0.1:3081
    wildcard_subdomains: false   # keep myapp.example.com as tunnel
```

**When:** marketing/product apex next to tunnel product.  
**Resilience:** pair with A4 for the tunnel plane.

---

### A9 — Mesh-only private network

Built-in private TLD (`*.tunneltug.tunnel`) without public barge haul.

```bash
tunneltug -mode server -mesh -mesh-zone tunneltug.tunnel -token "$TOKEN" ...
tunneltug -mode client -mesh -vpi-stub ...
```

**When:** private names / lab overlay.  
**Resilience:** authority on server/LB; clients publish + resolve via VPI stub.

---

### A10 — Hub + fleet image pipeline

OCI hub (embedded in k3s barge controller or `-mode hub`) stores engine + product images.

```text
hub-publish → hub.tunneltug.com → k3s ctr pull → barge / stack pods
```

**When:** air-gapped-friendly pipeline with public pull, authed push.  
**Resilience:** S3-backed blobs; SDF fleet+image digest attestation on barge reconcile.

---

## Resilient designs (recommended combinations)

### R1 — HA tunnel edge (single region)

| Layer | Choice |
|-------|--------|
| Edge | **A4** LB + ≥2 barge replicas (k3s) |
| Certs | ACME `-prod` + shared/cache or per-LB |
| Register | Dynamic barge heartbeat; TTL prune |
| Snapshot | `-snapshot-dir` on servers for inventory restore |

Failure modes covered: single barge death, rolling image update, LB backend flap.

---

### R2 — Global anycast + multi-PoP stack

| Layer | Choice |
|-------|--------|
| DNS / VIP | **A7** anycast per PoP, health withdraw |
| Ingress | **A4** LB+barges per PoP |
| Apps | **A6** site config, `roles: [anycast,lb,barge,stack,kernel]` |
| Data | Kernel full-mesh; **local embeds primary**, peers for real-time sync |

Failure modes covered: PoP loss (anycast), regional app outage, split-brain mitigation via mesh mode (prefer hub-spoke if you need a write hub).

---

### R3 — Active / standby product failover (vhost cutover)

| Layer | Choice |
|-------|--------|
| Public hostname | Unchanged (vhost domain) |
| Upstream | API/runtime set vhost upstream to standby barge or stack Service |
| Tunnel plane | Separate from product vhost |

Use vhost upstream failover so customers keep the same URL while backend fleet moves.

---

### R4 — Kernel replication without single remote DB

| Do | Don't |
|----|--------|
| Open local product DB | Prefer `ULTIMATE_DB_URL` as only store |
| `AddPeer(kernel URL)` for replication | Treat kernel barge as mandatory remote primary |
| Multi-PoP `node_id` + `url` in site YAML | Hardcode peer IPs only in one region without mesh policy |

---

### R5 — Image supply chain resilience

| Control | Mechanism |
|---------|-----------|
| Public pull | Hub allows fleet pull without write creds |
| Auth push | Crypto tunnel token |
| Fleet integrity | SDF GRANT binds fleet shape **and** image digest |
| Verify | Digest mismatch fails integrity check after retag/swap |

---

## Choosing a shape

| Goal | Start with |
|------|------------|
| Expose laptop app | A1 / A2 |
| Many tunnels, one edge | A3 → A4 |
| Run product images | A5 |
| Multi-region users + sync | A6 + A7 + R2 |
| Private names only | A9 |
| Ship images to fleets | A10 |

---

## Config builder (hub)

On **hub.tunneltug.com**, each catalog image opens a **Configure** modal:

1. Pick **architecture** (stack, multi-PoP, resilient global, …).
2. Set **replicas**, **link** other services, or add **multiple instances** of the same product.
3. Preview **YAML** and **Tugconf** side by side.
4. **Scale & link** regenerates the config; copy or download.

Generated configs target `-stack-config` / `-config` so they drop into the architectures above.

---

## Related docs

- [README.md](../README.md) — features, site config, kernel replication
- [MESH-AND-TUNNELTUG](https://github.com/0TrustCloud/0TrustCloud) (0Trust monorepo) — mesh vs public barge haul
- [deploy/hub/README.md](../deploy/hub/README.md) — hub publish and fleet images
- Examples: `config/site.example.yaml`, `config/site.example.tug`, `config/stack.example.yaml`
