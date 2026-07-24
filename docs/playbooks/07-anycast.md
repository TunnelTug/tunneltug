# 07 — Anycast edge

Health-gated BGP anycast + split-horizon DNS. Same binary as tunnel modes.

## Standalone

```bash
tunneltug -mode anycast -anycast-config config/anycast.example.yaml
# lab:
tunneltug -mode anycast -anycast-config config/anycast.local.yaml
```

## Sidecar on server/lb

```bash
tunneltug -mode server -mesh -anycast \
  -anycast-config config/anycast.example.yaml \
  -token "$TOKEN" …
```

Env: `TUNNELTUG_ANYCAST=1`, `TUNNELTUG_ANYCAST_CONFIG=…`.

## BGP backends

Configured in YAML: `log` (dry-run), `exabgp`, `bird`, `file`.

## Security (fail-closed when enabled)

1. **ROV** — RPKI origin validation before announce.  
2. **BGPsec** — ECDSA P-256 origin signatures (RFC 8205/8208). **Not** ACME/TLS keys.

```bash
tunneltug -anycast-gen-bgpsec-key config/bgpsec-router.lab.key
```

## Status

Default listen from YAML (often `127.0.0.1:9099`):

```bash
curl -sS http://127.0.0.1:9099/health
curl -sS http://127.0.0.1:9099/ready
curl -sS http://127.0.0.1:9099/status
```

`/ready` is 200 only when healthy **and** routes announced (security gates passed).

## Shadow profile

Extra private suffixes + zone pack: `config/shadow.example.yaml`, `deploy/shadow/`.

## Hard rules

1. Anycast DNS announce/withdraw is traffic steering — **TLS still** follows [01-acme-tls.md](01-acme-tls.md) on whichever PoP answers HTTPS.
2. Never confuse BGPsec router keys with Let's Encrypt material.
