# 04 — Load balancer + barge fleets (k3s)

TunnelTug owns the fleet shape. Prefer **`-mode barge -barge-runtime k3s`** over hand-editing cluster objects as the control plane.  
`deploy/k3s/*.yaml` is reference shape; the binary reconciles via the k3s API.

## LB (`-mode lb`)

```bash
tunneltug \
  -mode lb \
  -prod \
  -domain example.com \
  -email ops@example.com \
  -token "$TOKEN" \
  -lb-dynamic \
  -lb-policy sticky   # or round-robin
```

| Flag | Purpose |
|------|---------|
| `-backends` | Static `host[:control[:public]],…` |
| `-lb-dynamic` | Accept barge register/heartbeat (default on) |
| `-lb-register-ttl` | Prune silent backends (seconds) |
| `-backend-insecure` | TLS skip when dialing backend control (lab) |

Clients connect to the **LB** public + control addresses only.

## Barge (`-mode barge`)

### Production default: k3s

```bash
export TUNNELTUG_TOKEN="$(tunneltug -gen-token)"

tunneltug \
  -mode barge \
  -barge-runtime k3s \
  -barge-replicas 2 \
  -barge-lb <lb-public-ip>:443 \
  -k3s-kubeconfig /etc/rancher/k3s/k3s.yaml \
  -k3s-namespace tunneltug \
  -k3s-image hub.tunneltug.com/tunneltug/engine:latest \
  -token "$TUNNELTUG_TOKEN" \
  -domain example.com
```

| Flag | Default | Role |
|------|---------|------|
| `-barge-runtime` | `k3s` | `k3s` prod / `process` dev |
| `-k3s-hub` | true | Embed OCI hub in controller |
| `-k3s-hub-pull` | true | Pull engine image before reconcile |
| `-k3s-cleanup` | false | Leave pods up when controller stops |
| `-k3s-stack` | false | Also reconcile product stack YAML |
| `-barge-config` / `-stack-config` | | Product barge YAML |
| `-barge-dash` | 4050 | Fleet dashboard |

Pods: hostNetwork + `-index-from-hostname` → ordinal ports (see deploy/k3s README).

### Development: process supervisor

```bash
tunneltug -mode barge -barge-runtime process -barge-replicas 2 -barge-service server …
```

## Orchestrator (`-mode orchestrator`)

Namespace-aware LB + control for multi-fleet ops. Same family as LB with namespace routing; dashboard `-orch-dash` (4060).

## Hard rules

1. **Do not** use kubectl as the day-2 control plane for fleets TunnelTug reconciles — change flags/YAML and restart the controller.
2. **Do not** put production public ACME on every barge public port if the LB already terminates TLS for the apex (architecture-dependent; usually LB owns `-prod`).
3. Token: `tunneltug -gen-token` only — never weak defaults in prod.
4. Rolling barge image: publish via hub / `-k3s-hub-publish`, then reconcile — not random `ctr` one-offs without the controller.

## Status

```bash
curl -sS http://127.0.0.1:4050/_tunneltug/barges
```
