# 05 — Hub, hub-publish, product stack

All image distribution and product deploys described here are **TunnelTug-native** (Registry API + k3s client-go). No docker/crane/kubectl required for the happy path.

## Hub (`-mode hub`)

OCI registry process (often co-hosted with product edge as `hub.tunneltug.com` vhost → `:5000`).

```bash
tunneltug \
  -mode hub \
  -hub-listen :5000 \
  -hub-public https://hub.tunneltug.com \
  -hub-s3-url https://0trust.social \
  -hub-bucket tunneltug-hub \
  -token "$TOKEN"
```

| Flag | Role |
|------|------|
| `-hub-memory` | In-memory store (tests only) |
| Pull | Public |
| Push | Authenticated with token |

Also embedded when `-mode barge -barge-runtime k3s` with `-k3s-hub` (default).

## Hub-publish (`-mode hub-publish`)

Push product + engine images.

```bash
# Prefer images already in local k3s; else pack from -hub-dist
tunneltug \
  -mode hub-publish \
  -hub-host hub.tunneltug.com \
  -hub-tag latest \
  -hub-products platform,services,williwaw,tunneltug \
  -hub-dist /path/to/0TrustCloud/deploy/oci/dist \
  -token "$TOKEN"
```

Dist layout: `<hub-dist>/<product>/product` (linux amd64).

Engine-only via barge controller:

```bash
tunneltug -mode barge -barge-runtime k3s \
  -k3s-hub-publish tunneltug:local \
  -k3s-image hub.tunneltug.com/tunneltug/engine:latest \
  -k3s-hub-publish-only \
  -token "$TOKEN"
```

## Stack (`-mode stack`)

Self-contained product Deployments (no kubectl).

```bash
tunneltug \
  -mode stack \
  -stack-config config/stack.example.yaml \
  -token "$TOKEN"
# dashboard + one-port gateway (all services via stack-dash):
# http://127.0.0.1:4070/
# http://127.0.0.1:4070/apps/{name}/
# Auth + shell are mesh DNS + meshTLS HTTPS ONLY (WebAuthn forbids http/localhost):
# https://stack.{mesh-zone}:4073/auth                 # passkey + DBSC on mesh FQDN
# https://stack.{mesh-zone}:4073/shell?app={name}     # gonode Junos CLI after auth
# https://stack.{mesh-zone}:4073/shell?app={name}&cmd=sh
# Loopback http://127.0.0.1:4070 is status/gateway only (redirects /auth /shell → mesh HTTPS)
# Resolve stack.{zone} via mesh DNS / VPI; Mesh CA leaf from platform (gonode path)
# curl -sS http://127.0.0.1:4070/_tunneltug/stack | jq '.apps[] | {name, mesh_fqdn, gateway}'
```

| Flag | Purpose |
|------|---------|
| `-stack-products` | Comma list or use YAML barges |
| `-stack-namespace` | k3s namespace |
| `-stack-tag` / `-hub-tag` | Image tag |
| `-stack-host-ports` | `products` \| `all` \| `none` |
| `-stack-gateway` | **One port**: proxy all apps via `/apps/{name}/` on stack-dash (4070) |
| `-stack-0trustos` | Inject gonode sidecar provision env (`OTRUST_*` + mesh domain) for 0TrustOS images |
| `-stack-auth-proxy` | **Default on.** Require 0Trust passkey + DBSC before `/shell` (gonode CLI) |
| `-stack-auth-platform` | Identity plane base (default `-mesh-platform` / `https://0trust.cloud`) |
| `-mesh` / `-mesh-zone` | Publish `name.<mesh-zone>` A records; dashboard **Mesh address** column |

**0TrustOS:** product images are `FROM hub.tunneltug.com/0trust/gonode` (product binary + **gonode sidecar** via `0trustos-entry`). Stack injects `OTRUST_*` so each pod phones home with mesh PKI + `OTRUST_MESH_DOMAIN` (`{app}.tunneltug.tunnel`).

**Shell (mesh DNS + meshTLS + auth_proxy → gonode CLI):**

WebAuthn **cannot** run over `http://` or `localhost`. That is why mesh DNS + meshTLS exist.

1. Point resolver at mesh DNS / VPI so the operator FQDN resolves.
2. **meshTLS = platform Mesh CA leaves** (same path as **gonode**): nameservice provision → EnsureMeshCert → `GET /api/v1/pki/certs/active`. Trust the platform mesh-root on the client. TunnelTug does **not** invent a local CA.
3. FQDNs come from `-mesh-zone`, `-domain`, and **`-subalt`** (wildcards allowed). Prod mesh uses real domains; WebAuthn/DBSC need that FQDN TLS.
4. Open **`https://stack.{mesh-zone}:4073/auth`** (or your domain) — auth_proxy, WebAuthn RP ID = mesh FQDN SNI.
5. Complete **passkey + DBSC**, then **`/shell?app={name}`** for **gonode Junos CLI** in the 0TrustOS image.

Loopback `http://127.0.0.1:4070` is **status + /apps gateway only**; `/auth` and `/shell` redirect to mesh HTTPS. Override: `-stack-auth-proxy=false`. `?cmd=sh` for `/bin/sh`.

Co-run with fleet:

```bash
tunneltug -mode barge -barge-runtime k3s -k3s-stack \
  -barge-config config/stack.example.yaml \
  -token "$TOKEN"
```

## 0TrustOS inject

When `-stack-0trustos` is true (default), stack pods get provision env so images **FROM** `hub.tunneltug.com/0trust/gonode` can phone-home to platform `/access/{product}`.  
This is **not** public marketing TLS for `0trustos.com` — that remains the **server -prod vhost** playbook.

## Hard rules

1. Publish images with **hub-publish / k3s-hub**, not random external registries as the control path.
2. Stack domains come from YAML `domain` / per-barge `public_url` — nothing hardcoded for 0trust.* in the engine.
3. Do not dual-ACME product faces that are supposed to terminate on a TunnelTug public edge.
