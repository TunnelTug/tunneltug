# 03 — Client tunnel (`-mode client`)

Expose a local HTTP process through a TunnelTug server/LB.

## Minimal

```bash
# Server (elsewhere)
tunneltug -mode server -prod -domain example.com -email ops@example.com -token "$TOKEN"

# Client
tunneltug \
  -mode client \
  -server edge.example.com \
  -control 9000 \
  -local 3000 \
  -subdomain myapp \
  -token "$TOKEN"
```

Public URL (subdomain routing): `https://myapp.example.com`

## Direct routing

```bash
tunneltug -mode client -prod -routing direct -domain example.com -local 3000 -token "$TOKEN"
```

No subdomain; traffic shares the apex tunnel.

## Useful flags

| Flag | Purpose |
|------|---------|
| `-dash` | Local client dashboard (default 4040) |
| `-insecure` | Skip TLS verify (dev / self-signed only) |
| `-namespace` | Logical namespace (orchestrator multi-fleet) |
| `-mesh` | Publish into built-in mesh zone |
| `-vpi-stub` | Local private-TLD resolver (often with mesh) |

## Checklist

- [ ] Same `-token` as server/LB  
- [ ] UDP control port open server←client  
- [ ] Local app listening on `-local`  
- [ ] DNS for subdomain points at edge (or wildcard)  
- [ ] Production clients do **not** use `-insecure` against real ACME edges  
