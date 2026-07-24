# 00 — Golden rules

## Tooling boundary (non-negotiable)

| Allowed | Forbidden for TLS / fleets / product ingress |
|---------|-----------------------------------------------|
| `tunneltug` / `tunneltug-engine` | certbot, lego, acme.sh, Caddy standalone ACME |
| TunnelTug `-prod` autocert + `-acme-cache` | Issuing the same name from 0trust-services / platform ACME |
| TunnelTug `-mode barge` / `stack` / hub | Ad-hoc `kubectl` as the control plane for fleets TunnelTug owns |
| TunnelTug mesh / VPI / `-dns` | “Just open port 53 with random firewall tools” instead of the product DNS plane |
| Config in `-vhosts`, systemd unit flags, site YAML | Registrar/DNS **API** changes as part of an ACME firefight (unless you own that plane as a separate, explicit DNS change) |

**PKI for public HTTPS on a TunnelTug edge = TunnelTug only.**  
If two processes try Let's Encrypt for the same hostname, you get HTTP-01 races and **failed-authorization rate limits**.

---

## One owner per public hostname

Before adding a domain to production:

1. **Who terminates TLS?** Exactly one process (usually `tunneltug-engine -mode server -prod`).
2. **Where is the A/AAAA record?** Must point at **that** process’s public IP.
3. **Is the name in the HostWhitelist?** Built from:
   - `-domain`
   - `-subalt` (comma list)
   - `acme_domains` + every `vhosts[].domain` from `-vhosts` YAML  
   See `tlsHosts()` in `tls.go`.
4. **Is the name *not* listed** in cloud `0trust-services` / standby `acme_domains`?

If any answer is fuzzy, stop. Do not “try ACME from both sides.”

---

## One owner per node port (systemd **or** hostPort)

On any host that runs both **product systemd units** and TunnelTug **`-mode stack`**:

| Exactly one | Never both |
|-------------|------------|
| Host `*.service` binds `:PORT` | Same `:PORT` as k3s Deployment **hostPort** |
| Stack pod hostPort + DNAT | Host process also LISTEN on that port |

Stack hostPort DNAT can steal traffic from a “healthy” systemd unit → public 404 / wrong UI.  
**Full procedure:** [11-host-ownership.md](11-host-ownership.md) (defcon.chat post-mortem).

---

## ACME is on-demand and expensive when empty

| Situation | LE behavior |
|-----------|-------------|
| Cert file already in `-acme-cache` | Serve from disk; **no** new order |
| Name on HostWhitelist, **no** cert file | First HTTPS SNI → full HTTP-01 order |
| Name **not** on HostWhitelist | Handshake fails locally; **no** LE order |
| Order fails validation | Counts toward **5 failed authorizations / hostname / hour** |

**Why evilrbi / tunneltug.com “just work” while a new marketing domain dies:**  
Cached certs. Scanners and operators hitting a **new** name with no cache burn the quota.

### Do not

- Loop `curl https://new-domain/` to “force issue.”
- Restart the unit in a tight loop while the name is whitelisted and uncached.
- Strip/restore whitelist every few minutes “to cool down” **while** also restoring it and curling again.
- Use certbot on the same host “just this once.”

### Do

- Put DNS A + vhost + `acme_domains`/`-subalt` in place **before** first public traffic.
- Issue **once** (natural traffic or a single client hello).
- On rate limit: **stop all HTTPS probes** for that hostname; wait for `retry after` in the log; fix root cause; then **one** attempt.

---

## Config change procedure (edge)

1. Edit repo sources of truth (`tunneltug-vhosts-direct.yaml`, unit file, env).
2. Deploy **only** those files + binary if needed.
3. `systemctl daemon-reload && systemctl restart <tunneltug-direct-unit>`.
4. Confirm log line:  
   `Production TLS: requesting ACME certificates for …` includes intended hosts.
5. Confirm cached domains still `200` (safe).
6. For **new** names: wait for one successful cache file under `-acme-cache` before declaring done.  
   Do not poll with aggressive curls.

---

## 0Trust product co-host (marketing faces)

Typical 0Trust layout:

| Role | Host |
|------|------|
| TunnelTug public edge | e.g. `146.190.212.246` — `tunneltug-engine -prod -routing direct -vhosts …` |
| Platform content | e.g. cloud `:8443` — Host header preserved; **not** ACME for marketing apex |
| DNS A for marketing | → TunnelTug edge IP |

Content can live on platform; **certificate and :443** stay on TunnelTug.

---

## Incident pattern we must not repeat

1. New hostname pointed at TunnelTug edge.
2. Name missing from live `-subalt` / vhosts → `host not configured in HostWhitelist`.
3. Operator “fixes” with dual ACME / certbot / DNS API thrash.
4. Validation fails (wrong owner, dead NS, race).
5. Automated retries + scanners burn **5 failed authz / hour**.
6. Edge temporarily broken by half-applied cooldown YAML.

**Correct path:** one owner (TunnelTug) → one config deploy → one issue → cache forever (until renew).

---

## Quick self-check before any TLS change

```text
[ ] Only tunneltug-engine listens on public :80 and :443 for this name
[ ] dig A name @public → edge IP that runs that process
[ ] Name in -domain / -subalt / vhosts acme_domains
[ ] Name NOT in other product ACME lists
[ ] acme-cache path is persistent and backed up
[ ] I will not curl https://name more than once until cache file exists
[ ] I will not use certbot/lego/DO ACME
[ ] EVERY public NS glue (ns.0trust.cloud, ns.0trust.name, ns.0trust.services, …)
    dig A @that-ip for the hostname succeeds (no timeout)
```

## Nameservice glue vs zone sync (0Trust root cause class)

| Plane | What “healthy” means |
|-------|----------------------|
| **Zone sync API** | Edge logs `zone sync OK — N records` from `GET /api/v1/nameservice/sync` |
| **Public NS glue** | Every hostname in the zone’s **NS set** has A/AAAA that answer **UDP/TCP 53 from the internet** |

These are **not** the same. Edges can sync zone JSON perfectly while `ns.0trust.services` / `ns2.0trust.name` still point at a host whose **public :53 is dead**. Recursive resolvers and Let's Encrypt multi-perspective then see intermittent or hard DNS failures — which looks like “ns.0trust.cloud and ns.0trust.name are not syncing” even when both names resolve to the same healthy cloud IP.

**Fix in-product:** point unreachable NS glue at a host that serves the same zone publicly (platform nameservice provision / bootstrap), or make the edge answer public DNS. Do not dual-ACME to paper over bad glue.
