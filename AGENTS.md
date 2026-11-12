# TunnelTug — agent rules (non-negotiable)

## You are the k3s provider

TunnelTug **is** the k3s control path for barge fleets and product stacks.

| Do | Do not |
|----|--------|
| `-mode stack` / `-mode barge -barge-runtime k3s` | Ad-hoc **kubectl** as control plane |
| `-k3s-hub-pull`, hub-publish | `docker save \| ctr import` as deploy path |
| Change YAML + restart engine unit | Hand-edit Deployments outside reconcile |
| Status via `/_tunneltug/stack` or `/_tunneltug/barges` | `kubectl get/scale/delete` for day-2 ops |

## 0Trust product hosts

- Clean product stack config lives in **0TrustCloud `config/stack.yaml`** — **never `stack.dirty.yaml`** for production or “clean 0TrustOS barges.”
- Product images are **0TrustOS** (gonode base + product binary). Stack injects provision with `-stack-0trustos`.
- Hub push token = hub process token (e.g. tunneltug droplet `tunneltug-hub.env`), not a mismatched stack token.
- Host ownership: one owner per port (systemd vs hostPort). Playbook **11-host-ownership.md**.

## Docs

`docs/playbooks/00-golden-rules.md`, `04-lb-barges-k3s.md`, `05-hub-publish-stack.md`, `11-host-ownership.md`.
