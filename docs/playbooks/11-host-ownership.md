# 11 — Host ownership: systemd **or** stack hostPort (never both)

## The rule (non-negotiable)

For every **public product port** on a node:

| Choose exactly one | Do not |
|--------------------|--------|
| **Host systemd** unit binds the port (e.g. `ack.service` → `:3083`) | Also run k3s Deployment with `hostPort` on that same port |
| **Stack hostPort** pod binds the port via CNI DNAT | Also run a host binary listening on the same port |

**Two owners = broken product.** Symptoms look random: intermittent 404, wrong app, empty UI (`View resource not located`), CrashLoop, or “systemd active but site is garbage.”

### Why (what actually breaks)

1. TunnelTug `-mode stack` with `-stack-host-ports products` (default) puts **hostPort** on product Deployments.
2. CNI installs **DNAT** so traffic to `nodeIP:PORT` (and often `127.0.0.1:PORT`) is rewritten to the **pod**.
3. Host `systemctl` can still show the unit **active** and `ss` can still show a host process **LISTEN** on the port.
4. Edge (`0trust-services` vhost → `http://127.0.0.1:PORT`) hits DNAT → **pod** wins, not the systemd process you just restarted.
5. Stack images often lack full UI trees / env → guikit **`View resource not located`**, wrong CMS URL, 404.

**Post-mortem: defcon.chat (2026-07-24)**  
Host `ack.service` had correct UI at `/opt/0trust/ack/ui`. Stack `deploy/ack` had `hostPort: 3083`. Public traffic hit the pod → 404. Fix: scale stack `ack` to **0**, keep host systemd.

---

## Ownership matrix (0trust.cloud control-plane host)

**Canonical production layout (2026-07 cutover): TunnelTug barges own product ports.**  
Host systemd product units (`0trust-platform`, `0trust-services`, app units) must be **stopped, disabled, masked**. MOTD aborts if any are active.

| Port | Product / face | **Owner** | Stack |
|------|----------------|-----------|-------|
| 8443 + 53 | platform (0trust.cloud) | **stack** host_network + host_port | `replicas: 1`, `host_network: true` |
| 443/80 | edge ACME + vhosts (0trust.services) | **stack** host_network + host_port | `replicas: 1`, `host_network: true`, port 443 |
| product hostPorts (3081…) | williwaw/ack/social/… | **stack** hostPort (default products policy) | ≥1 when listed in `stack.yaml` |
| 8480/8481 | ultimate_db / keystore | **stack** hostPort | ≥1 |

Allowed host units only: **`tunneltug-stack`**, k3s, monitoring/hygiene, SSH, HA helpers — **not** product binaries.

**Lab exception only:** dual systemd ownership is documented historically below; do **not** reintroduce on prod cloud.

<details><summary>Legacy matrix (do not use on prod)</summary>

| Port | Owner (legacy) |
|------|----------------|
| 8443 | systemd `0trust-platform` + platform ClusterIP-only |
| 443/80 | systemd `0trust-services` |
| 308x | host product units with stack `replicas: 0` |

</details>

**Stack may still run** kernel barges (`ultimate_db`, `ultimate_keystore`) and optional ClusterIP-only dogfood with **`host_ports: none`** + stack gateway (`:4070/apps/{name}/`).

### Product vhost droplet (e.g. 165.22.14.101)

| Port | Owner |
|------|--------|
| 443/80 | `0trust-vhost` / tunneltug-engine ACME |
| 3081 | williwaw systemd (if present) |
| 3098 | work (todotraq) systemd |

No k3s hostPort on those ports.

---

## How to pick the owner

### Prefer **host systemd** when

- Public ICANN face needs full UI tree, secrets, and durable host paths (`/opt/0trust/…`).
- You already have `deploy/droplet/*.service` install scripts.
- You need SMTP bind, large blobs, or same-host loopback to other systemd units.

### Prefer **stack hostPort** when

- Lab / single-tenant dirty barge with **no** host unit for that app.
- Kernel replication barges (udb/uks) with no competing systemd unit.
- You set **`host_ports: none`** and only use the stack gateway (no public bind).

### Never

- `replicas: 1` + hostPort **and** `systemctl start <app>` on the same node.
- “Fix” a broken public site by restarting only systemd while stack hostPort still owns DNAT.

---

## Enforce in stack YAML

Set **`replicas: 0`** on every barge that host systemd owns. TunnelTug respects explicit `0` (see `stack_config.go`).

Example fragment for `stack.dirty.yaml` on the cloud host:

```yaml
barges:
  - name: platform
    # ClusterIP dogfood only — public 0trust.cloud is host 0trust-platform :8443
    image: localhost:5000/0trust/platform:dirty

  - name: williwaw
    replicas: 0   # host williwaw.service
  - name: ack
    replicas: 0   # host ack.service → defcon.chat
  - name: mail
    replicas: 0
  - name: search
    replicas: 0
  - name: motionkb
    replicas: 0
  - name: gitmesh
    replicas: 0
  - name: social
    replicas: 0

  - name: ultimate_db
    # hostPort OK — no systemd competitor
  - name: ultimate_keystore
```

Optional stack-wide:

```yaml
# Avoid hostPort for products entirely; use gateway only
# host_ports: none
```

Or engine flag: `-stack-host-ports none`.

After editing:

```bash
# Apply config (stack controller reconciles)
systemctl restart tunneltug-stack
# or wait for reconcile cycle

k3s kubectl -n 0trust-stack get deploy
# Host-owned apps must show 0/0 READY
```

---

## Audit checklist (run on the node)

```bash
# 1) Host units
systemctl is-active ack williwaw mail search motionkb gitmesh social bandy

# 2) Who is listening
ss -tlnp | grep -E ':(308[0-9]|8090|8443)\s'

# 3) Stack hostPort
k3s kubectl -n 0trust-stack get deploy -o json | \
  python3 -c 'import json,sys; d=json.load(sys.stdin)
for i in d["items"]:
  n=i["metadata"]["name"]; r=i["spec"].get("replicas",1)
  hp=[p.get("hostPort") for c in i["spec"]["template"]["spec"].get("containers",[]) for p in (c.get("ports") or []) if p.get("hostPort")]
  if hp: print(n, "replicas=", r, "hostPort=", hp)'

# 4) CNI DNAT still pointing at scaled-down apps?
iptables -t nat -S | grep -E 'dports 308|dports 8090' || echo "no product hostPort DNAT"

# 5) Smoke (expect 2xx/3xx from host process, not random 404)
for p in 3081 3083 3084 3085 3086 3087 3088 8090; do
  echo -n ":$p "; curl -sS -o /dev/null -w "%{http_code}\n" --max-time 3 http://127.0.0.1:$p/ || echo fail
done
```

### Conflict signature

| Observation | Likely cause |
|-------------|--------------|
| `systemctl is-active X` = active, public site 404 / wrong body | Stack hostPort DNAT steals traffic |
| `ss` shows host LISTEN, still broken | DNAT before local process (CNI) |
| Works after `kubectl scale deploy/X --replicas=0` | Confirmed dual owner |
| Pod CrashLoop + host unit crash | Both bind; one fails |

---

## Incident fix (cutover to host systemd)

```bash
# Pick the app (example: ack / defcon.chat)
APP=ack
PORT=3083

# 1) Stack gives up the port
k3s kubectl -n 0trust-stack scale deploy/$APP --replicas=0

# 2) Persist in stack.dirty.yaml:  replicas: 0  under that barge
# 3) Restart host unit
systemctl restart $APP
systemctl is-active $APP

# 4) Confirm DNAT gone for that port and host owns LISTEN
ss -tlnp | grep ":$PORT"
iptables -t nat -S | grep "dports $PORT" || echo "DNAT clear"

# 5) Local + public smoke
curl -sS -o /dev/null -w "%{http_code}\n" -H "Host: defcon.chat" http://127.0.0.1:$PORT/
curl -sS -o /dev/null -w "%{http_code}\n" https://defcon.chat/
```

Cutover the other way (stack owns the port): **stop and disable** the host unit first, then set `replicas: 1` and ensure hostPort policy includes the app.

---

## Related

- [00-golden-rules.md](00-golden-rules.md) — one owner per public **hostname** / TLS  
- [05-hub-publish-stack.md](05-hub-publish-stack.md) — `-stack-host-ports`, gateway  
- [02-server-edge-vhosts.md](02-server-edge-vhosts.md) — edge upstreams to `127.0.0.1:PORT`  
- [10-diagnostics.md](10-diagnostics.md) — safe curls  

**Remember:** one owner per public **hostname** (TLS), and one owner per **node port** (systemd vs hostPort). Both rules are required for a healthy 0Trust host.
