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

Examples: `config/site.example.yaml`, `config/site.example.tug`, `config/site.sandbox.example.yaml`.

Interactive builder: [hub.tunneltug.com](https://hub.tunneltug.com) config modal (YAML + Tugconf).

## Sandbox

A sandbox is the TunnelVM for a pop. It is the k3s node that pop's stack schedules onto. Declare it in the same site document. Product workloads stay barges.

```yaml
sandboxes:
  - name: sandbox
    pop: sfo
    cpus: 8
    memory_mb: 12288
    data_gb: 256
    kube_port: 16443
    shares:
      - name: work
        host: C:\src\share
        mount: /mnt/work
```

```bash
tunneltug -config config/site.sandbox.example.yaml -pop sfo -config-check
```

On a TunnelVM build, the same file boots the node:

```bash
tunneltug vm up -config config/site.sandbox.example.yaml -pop sfo
```

Flags on the command line win over the sandbox block. `cpus: 0` (or omitting `cpus`) uses the host processor count at boot. `memory_mb` below 512 is rejected. A share mount is an absolute guest path.

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
