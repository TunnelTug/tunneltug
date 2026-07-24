# 10 — Diagnostics (safe ops)

## Always-safe checks

```bash
# Unit / process
systemctl is-active tunneltug-direct-phisheries   # example unit name
ss -lntp | grep -E ':80|:443'
pgrep -a tunneltug-engine

# Startup truth
journalctl -u <unit> --since "10 min ago" | grep -E 'Production TLS|vhost|ACME HTTP-01|Listening'

# Domains with EXISTING acme-cache files (no new LE order)
ls /opt/0trust/data/acme-phisheries/
curl -sS -m 10 -o /dev/null -w "%{http_code}\n" \
  --resolve evilrbi.com:443:127.0.0.1 https://evilrbi.com/

# DNS ownership (not ACME)
dig +short A example.com @1.1.1.1
dig +short NS example.com @1.1.1.1
```

## Dangerous checks (can burn LE quota)

```bash
# ONLY if hostname has no cert file yet — at most ONCE after fixing root cause
curl -sS -m 120 --resolve example.com:443:127.0.0.1 https://example.com/
```

**Never** in a loop. **Never** from many machines at once against an uncached name.

## Log signatures

| Log | Meaning | Action |
|-----|---------|--------|
| `host "x" not configured in HostWhitelist` | Missing `-subalt` / vhosts / domain | Add to TunnelTug config; restart once |
| `rateLimited: too many failed authorizations` | 5 fails/hour | Stop probes; wait `retry after`; fix validation |
| `unable to satisfy … no viable challenge type` | Authz dead/invalid | Fix :80/DNS/dual ACME; wait; one retry |
| `missing certificate` | GetCertificate failed or mid-flight | Check above; do not spam |
| `Listening for ACME HTTP-01 challenges on :80` | Healthy ACME path | Good |
| `[vhost] loaded N product vhost(s)` | Vhosts OK | Confirm domain listed |

## Dual-owner detection

```bash
# On cloud services host — marketing name must NOT appear if TunnelTug owns it
grep -R "0trustos.com" /opt/0trust/config/services*.yaml || true

# On TunnelTug edge — name MUST appear
grep -n "0trustos.com" /opt/0trust/config/tunneltug-vhosts-direct.yaml \
  /etc/systemd/system/tunneltug-direct-phisheries.service
```

## What not to run

| Tool / action | Why |
|---------------|-----|
| certbot / lego / acme.sh | Bypasses TunnelTug; races HTTP-01 |
| DO/registrar ACME helpers | Outside product PKI |
| `while curl https://newname; do …` | Burns failed-authz |
| kubectl apply as primary fleet control | Use `-mode barge` reconcile |
| “Cooldownown” whitelist thrash + immediate re-issue | Creates more failures |

## Recovery template (new public name)

1. DNS A → TunnelTug edge only.  
2. Vhosts + `-subalt` include name; **not** in services ACME lists.  
3. Restart TunnelTug unit **once**.  
4. Confirm whitelist log.  
5. Single HTTPS SNI.  
6. Confirm `acme-cache/<name>` file exists.  
7. Done. Document owner in deploy notes.

## Fleet / hub

```bash
curl -sS http://127.0.0.1:4050/_tunneltug/barges
curl -sS http://127.0.0.1:4070/_tunneltug/stack   # if stack dash enabled
curl -sS http://127.0.0.1:5000/v2/                # hub catalog root
```
