# Image hub — embedded in TunnelTug k3s fleets

**License:** MIT — see the repository [LICENSE](../../LICENSE).

**Public UI:** [hub.tunneltug.com](https://hub.tunneltug.com) — architecture catalog, product cards, and a **config builder modal** (YAML + Tugconf) with **Scale & link** for replicas, linked services, and multi-instance layouts. API: `GET /_tunneltug/hub/catalog`.

Architectures & resilient designs: [docs/ARCHITECTURES.md](../../docs/ARCHITECTURES.md).

**Barge** = TunnelTug running a **k3s fleet** (`-mode barge -barge-runtime k3s`).  
Not a product. The hub and the **engine image** are how that fleet gets its pods.

```text
tunneltug -mode barge -barge-runtime k3s
```

| Piece | Behavior |
|-------|----------|
| **Who runs hub** | Same process as the k3s fleet controller (`-k3s-hub`, default **on**) |
| **Pull** | Public; controller `k3s ctr images pull` of the **engine** image |
| **Push** | Authenticated; products + engine via `-mode hub-publish` |
| **Storage** | 0trust.social S3 |
| **Engine image** | `hub.tunneltug.com/tunneltug/engine:latest` (tunneltug binary in each pod) |
| **SDF manifest** | Binds **fleet shape + image digest**; verify detects altered/retagged images |

## Normal fleet start

```bash
export TUNNELTUG_TOKEN="$(tunneltug -gen-token)"

tunneltug -mode barge -barge-runtime k3s \
  -barge-replicas 2 \
  -barge-lb 165.22.14.101:8444 \
  -k3s-kubeconfig /etc/rancher/k3s/k3s.yaml \
  -token "$TUNNELTUG_TOKEN" \
  -domain tunneltug.com
# Hub listens on :5000 (or -hub-listen)
# Engine image defaults to hub.tunneltug.com/tunneltug/engine:latest
# Controller pulls into local k3s, then reconciles StatefulSet pods
```

## Publish a new engine image (for the fleet)

```bash
# Engine binary already in local k3s/containerd
tunneltug -mode barge -barge-runtime k3s \
  -k3s-hub-publish tunneltug:local \
  -k3s-image hub.tunneltug.com/tunneltug/engine:1.2.4 \
  -k3s-hub-publish-only \
  -token "$TUNNELTUG_TOKEN"
```

## Flags (k3s barge)

| Flag | Default | Role |
|------|---------|------|
| `-k3s-hub` | `true` | Embed registry in barge controller |
| `-k3s-hub-pull` | `true` | `k3s ctr images pull` before reconcile |
| `-k3s-hub-publish` | — | Local image to push to `-k3s-image` |
| `-k3s-hub-publish-only` | `false` | Push then exit |
| `-hub-listen` | `:5000` | Registry bind |
| `-hub-public` | `https://hub.tunneltug.com` | Advertised registry URL |
| `-hub-s3-url` | `https://0trust.social` | Blob CDN |
| `-hub-bucket` | `tunneltug-hub` | S3 bucket |

## Multi-product publish

Same registry hosts **0Trust apps** and the **TunnelTug engine**:

| What | Image |
|------|--------|
| MeshMail | `hub.tunneltug.com/0trust/mail:<tag>` |
| MeshSearch | `hub.tunneltug.com/0trust/search:<tag>` |
| Platform | `hub.tunneltug.com/0trust/platform:<tag>` |
| Services | `hub.tunneltug.com/0trust/services:<tag>` |
| CDN (social) | `hub.tunneltug.com/0trust/social:<tag>` |
| Ack | `hub.tunneltug.com/0trust/ack:<tag>` |
| Williwaw | `hub.tunneltug.com/0trust/williwaw:<tag>` |
| MotionKB | `hub.tunneltug.com/0trust/motionkb:<tag>` |
| 0Trust Name (gTLD) | `hub.tunneltug.com/0trust/name:<tag>` |
| DBSC Relay | `hub.tunneltug.com/0trust/dbsc-relay:<tag>` |
| Anycast edge | `hub.tunneltug.com/0trust/anycast:<tag>` |
| Orchid Sync Ingest | `hub.tunneltug.com/0trust/orchid-ingest:<tag>` |
| 0Trust Auth / IAM / Access / SCIM / PKI / … | `hub.tunneltug.com/0trust/<face>:<tag>` |
| Ultimate DB (kernel) | `hub.tunneltug.com/0trust/ultimate-db:<tag>` |
| Ultimate Keystore (kernel) | `hub.tunneltug.com/0trust/ultimate-keystore:<tag>` |
| TunnelTug engine (k3s fleet pods) | `hub.tunneltug.com/tunneltug/engine:<tag>` |

Platform faces (`auth`, `iam`, `access`, `scim`, `pki`, `workflows`, `topology`, `nameservice`, `servicekeys`, `vpi`, `logs`) are individual barges that run the platform binary with `TRUST_PRODUCT` set.

```bash
# Prefer: images already in local k3s → k3s ctr push
# Fallback: pack binaries from -hub-dist and push via the hub Registry API
tunneltug -mode hub-publish \
  -hub-products name,dbsc_relay,anycast,orchid_ingest,williwaw,motionkb,ack,tunneltug \
  -hub-tag latest \
  -hub-dist /path/to/0TrustCloud/deploy/oci/dist \
  -token "$TUNNELTUG_TOKEN"
```

Build linux binaries into `deploy/oci/dist/<product>/product`, then hub-publish (TunnelTug only — no docker/crane).

```bash
# Product apps — self-contained (client-go), no kubectl
tunneltug -mode stack -stack-products williwaw,motionkb,ack,name,dbsc_relay,anycast,orchid_ingest,social -token "$TOKEN"

# Or co-run with tunnel fleet
tunneltug -mode barge -barge-runtime k3s -k3s-stack -stack-products williwaw,motionkb ...
```

Fleet: `tunneltug -mode barge -barge-runtime k3s` (k3s through TunnelTug).

## Dashboard

Fleet JSON includes hub status:

```text
GET http://127.0.0.1:4050/_tunneltug/barges
→ { "hub": { "enabled": true, "layer": "k3s-barge", "pull": "public", "push": "authenticated", ... } }
```
