# 09 — Site config (multi-PoP YAML / Tugconf)

One document describes global topology; each host runs with `-pop <id>`.

## Flags

| Flag | Purpose |
|------|---------|
| `-config` / `TUNNELTUG_CONFIG` | Path to YAML or `.tug` / set lines |
| `-pop` / `TUNNELTUG_POP` | Select `pops[]` entry |
| `-config-check` | Expand plan, print, exit |
| `-config-show-set` | Emit Tugconf set lines, exit |

```bash
tunneltug -config config/site.example.yaml -pop sfo -config-check
tunneltug -config config/site.example.yaml -pop sfo -mode stack -token "$TOKEN"
```

Examples: `config/site.example.yaml`, `config/site.example.tug`.

Interactive builder: [hub.tunneltug.com](https://hub.tunneltug.com) config modal (YAML + Tugconf).

## Kernel mesh modes (in site)

| Mode | Peers |
|------|--------|
| `full-mesh` | Every PoP ↔ every other with `kernel.*.url` |
| `hub-spoke` | Spokes ↔ `hub_pop` only |
| `manual` | Only explicit peers |

## Principles

1. Domains never hardcoded — set `domain` / `public_url` per site or barge.  
2. Secrets out of files — `token_env: TUNNELTUG_TOKEN`.  
3. CLI flags win over site-expanded defaults after apply.  
4. Per-PoP ACME still follows [01-acme-tls.md](01-acme-tls.md) — one owner per name per PoP answer IP.
