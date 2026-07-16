# k3s barge fleet

Run TunnelTug **server** replicas as a StatefulSet so:

- Updating web / other host services does **not** hard-reset the fleet
- Barge image rolls replace **one pod at a time** (LB keeps ≥ N−1 backends)

## Network

| Path | Requirement |
|------|-------------|
| LB → barge node | UDP control ports (default `9001+`), TCP public ports (default `8445+`) |
| Barge pods → LB | TCP to LB public (register / heartbeat / deregister) |
| Clients → LB only | Control QUIC and public HTTPS stay on the LB |

Pods use **hostNetwork** and `-index-from-hostname` so pod `tunneltug-barge-0` binds `9001/8445`, pod `-1` binds `9002/8446`, etc.

## Apply

1. Edit `configmap.yaml` (token, domain, LB address). Mint a crypto token with `tunneltug -gen-token` — never user-submitted or weak defaults.
2. **Barge** = this controller running the fleet on **k3s**. Hub is embedded (`-k3s-hub`).
   - OCI registry (public pull / auth push → 0trust.social S3)
   - Pulls the **engine** image (`-k3s-image`, default `hub.tunneltug.com/tunneltug/engine:latest`)
   - Reconciles StatefulSet pods that run that engine

```bash
# Publish engine image, then run k3s fleet
tunneltug -mode barge -barge-runtime k3s \
  -k3s-hub-publish tunneltug:local \
  -k3s-image hub.tunneltug.com/tunneltug/engine:latest \
  -token "$TOKEN" ...

# Push engine only
tunneltug -mode barge -barge-runtime k3s \
  -k3s-hub-publish tunneltug:local \
  -k3s-hub-publish-only \
  -token "$TOKEN"
```

3. Prefer the **controller** above (self-contained). YAML under `deploy/k3s/` is reference shape only — TunnelTug reconciles via the k3s API.


## Controller (optional)

Instead of raw YAML, the binary can reconcile the same shape:

```bash
# -barge-runtime defaults to k3s
./bin/tunneltug \
  -mode barge \
  -barge-replicas 2 \
  -control 9001 \
  -public 8445 \
  -barge-port-step 1 \
  -barge-lb 165.22.14.101:8444 \
  -k3s-image hub.tunneltug.com/tunneltug/engine:latest \
  -k3s-kubeconfig /etc/rancher/k3s/k3s.yaml \
  -token "$TOKEN" \
  -domain tunneltug.com \
  -backend-insecure
```

Stopping the controller leaves pods running (default). Pass `-k3s-cleanup` only if you want delete-on-exit.

Dashboard (controller host): `http://127.0.0.1:4050/_tunneltug/barges`.

## Rolling an image (no full fleet kill)

```bash
# Roll engine image via the controller (re-run with new -k3s-image)
tunneltug -mode barge -barge-runtime k3s \
  -k3s-image hub.tunneltug.com/tunneltug/engine:1.2.4 \
  -token "$TOKEN" ...
```

Canary: set `spec.updateStrategy.rollingUpdate.partition` (or `-k3s-update-partition`) so lower ordinals stay on the old revision until you lower partition.

## Snapshots (restore tunnel inventory after roll)

Before SIGTERM / image roll, each server writes a JSON snapshot under
`/var/lib/tunneltug/snapshots` (hostPath). On start it restores:

- Mesh zone records for previously connected tunnels
- **Pending reconnect** list (live QUIC sessions cannot be restored; clients re-dial)

Manual:

```bash
curl -X POST -H "X-TunnelTug-Token: $TOKEN" http://127.0.0.1:8445/_tunneltug/snapshot
curl -H "X-TunnelTug-Token: $TOKEN" http://127.0.0.1:8445/_tunneltug/snapshot
```

The k3s controller also POSTs snapshots to ready pods before applying an image change.

## Deploy isolation

Do **not** `systemctl restart` a shared barge parent when k3s owns the fleet. Host deploys for web/0trust should leave the StatefulSet alone; only barge image/config changes should roll pods.
