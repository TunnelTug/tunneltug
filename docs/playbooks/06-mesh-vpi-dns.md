# 06 — Mesh DNS, VPI, QUIC / HTTP/3 / WebAuthn

## Why mesh DNS exists

TunnelTug’s built-in mesh DNS (and the platform gTLD / VPI split resolver) is **not** a nice-to-have private zone. It is the hostname plane that makes the rest of the stack work:

| Protocol | Why stable mesh names matter |
|----------|------------------------------|
| **QUIC** | Connection migration and 0-RTT still need a consistent authority name |
| **HTTP/3** | TLS **SNI** is a hostname; peers dial `https://name…`, not raw `http://ip` |
| **WebAuthn** | **RP ID** is a registrable domain name; passkeys bind to that name, not an IP |

If you strip mesh DNS and talk IP + plain HTTP:

- HTTP/3 SNI and cert SANs no longer match
- WebAuthn ceremonies break or bind to the wrong RP
- Kernel replication “works in curl” but fails the security model

**Public** marketing ACME (e.g. `0trustos.com` on the TunnelTug edge) is a separate plane. Mesh DNS does not replace Let's Encrypt for ICANN faces — it names **private** mesh endpoints those public edges and products use.

```text
Browser / product
    │  HTTPS + HTTP/3 (SNI = product or mesh name)
    ▼
TunnelTug edge (-prod ACME for public, mesh TLS for private)
    │  QUIC control (ALPN tunneltug)
    ▼
Mesh DNS / VPI  ── resolves ──►  peer.tunneltug.tunnel / *.mesh / …
    │
    ▼
Kernel barges (ultimate_db / ultimate_keystore) — HTTP/3 only on :8480/:8481
```

## Mesh (`-mesh`)

| Role | Behavior |
|------|----------|
| server / lb | Zone authority: `secure_dns` + `secure_registrar` |
| client | Publish host + resolve via VPI |

```bash
tunneltug -mode server -mesh \
  -mesh-tld tunnel \
  -mesh-zone tunneltug.tunnel \
  -mesh-ns ns.tunneltug.tunnel \
  -mesh-dns 127.0.0.1:5353 \
  -mesh-data-dir /var/lib/tunneltug/mesh \
  -http3 \
  -prod -domain example.com \
  -token "$TOKEN"
```

| Flag | Purpose |
|------|---------|
| `-mesh-edge-ip` | A records for mesh names (edge that terminates HTTP/3) |
| `-mesh-join-platform` | Optional join external 0Trust platform mesh |
| `-http3` | Public ingress HTTP/3 (default on with TLS) |

Default private face: `*.tunneltug.tunnel`.

## meshTLS (FQDN HTTPS for WebAuthn / DBSC)

Production mesh uses **real domains** (plus any private zone). WebAuthn RP ID and DBSC need **FQDN TLS**. That is **platform Mesh CA**, not ACME and not a local invent.

**gonode (0TrustOS microkernel)** is the normative provisioner:

```text
PhoneHome → agent provision → nameservice A + EnsureMeshCert
  → GET /api/v1/pki/mesh-root
  → GET /api/v1/pki/certs/active  (X-0Trust-Sync-Token)
  → serve leaf by SNI
```

TunnelTug `-mesh-tls` follows the **same path** for the stack/edge operator face (`-mesh-zone`, `-domain`, `-subalt` wildcards):

| Piece | Flag / path |
|-------|-------------|
| Enable | `-mesh-tls` (default on with mesh) |
| Store | `-mesh-tls-dir` (synced leaves from certs/active) |
| HTTPS | `-mesh-tls-port` (default `4073`) — SNI = FQDN |
| Names | `-mesh-zone`, `-domain`, `-subalt` (incl. `*.example.com`) |

Client trust: platform mesh-root (`0trust pki install-root`).

```text
https://stack.{mesh-zone}:4073/auth      ← WebAuthn + DBSC (auth_proxy; NOT http/localhost)
https://stack.{mesh-zone}:4073/shell     ← gonode Junos CLI after auth
https://platform.{mesh-zone}:4073/       ← meshTLS SNI + Host gateway to product
http://127.0.0.1:4070/apps/platform/     ← loopback one-port only (no WebAuthn)
```

**WebAuthn requires mesh HTTPS.** Sign-in and shell never use `http://127.0.0.1` or bare localhost — mesh DNS names + meshTLS SNI only.

## VPI stub (`-vpi-stub`)

Local resolver so **processes on the node** resolve private suffixes the same way the edge does:

```bash
-vpi-stub \
-vpi-listen 127.0.0.1:5354 \
-vpi-upstream <mesh-dns> \
-vpi-fallback 8.8.8.8:53
```

Point system or app resolvers at VPI when they must dial `https://….tunnel` / `….mesh` for HTTP/3.

## Kernel peers (ultimate_sync / ultimate_db)

| Do | Don’t |
|----|--------|
| `https://ultimate-db.tunneltug.tunnel:8480` | `http://10.0.0.5:8480` |
| Mesh DNS A → peer edge | IP-only peer lists in prod |
| HTTP/3 client (`ultimate_sync.NewHTTP3Client`) | `http.DefaultClient` / HTTP/1.1 |
| Cert SANs include mesh hostname | Cert for IP only |

```bash
# Peer string for barges (https forced; http rejected)
tunneltug -mode ultimate_db \
  -udb-peers "ams=https://ultimate-db-ams.tunneltug.tunnel:8480" \
  -token "$TOKEN"
```

Kernel servers listen **HTTP/3 only** (QUIC/UDP). No TCP `:8480` cleartext.

## Hard rules

1. Mesh DNS = hostname plane for QUIC, HTTP/3 SNI, and WebAuthn RP ID.  
2. Public ICANN ACME stays on TunnelTug `-prod` edge (playbook 01) — not mesh ACME for private TLDs.  
3. Kernel replication and ultimate_sync: **HTTPS + HTTP/3 only**.  
4. Every **public** NS glue IP must still answer :53 (playbook 00) so LE multi-perspective and split DoH stay consistent — that is the *public* nameservice plane, not a substitute for mesh DNS.

## Related

- [01-acme-tls.md](01-acme-tls.md) — public LE ownership  
- [08-kernel-replication.md](08-kernel-replication.md) — ultimate_db / keystore + ultimate_sync  
- Platform gTLD: `0TrustCloud/docs/GTLD-SPEC.md`  
