# 01 — ACME / TLS (`-prod`)

TunnelTug uses **golang.org/x/crypto/acme/autocert** when `-prod` is set.  
There is no separate certificate tool.

## How HostWhitelist is built

From `tls.go` → `tlsHosts()`:

1. `-domain` (required with `-prod`)
2. If `-routing direct`: also `www.<domain>`
3. Every host in `-subalt` (comma-separated)
4. Every host from `-vhosts` file: `acme_domains` **and** each `vhosts[].domain`

Log on start:

```text
Production TLS: requesting ACME certificates for host1, host2, …
```

That list is the whitelist. Missing host ⇒  
`acme/autocert: host "…" not configured in HostWhitelist`  
(no Let's Encrypt order).

## Required flags / env (server or lb)

| Flag | Purpose |
|------|---------|
| `-prod` | Enable ACME |
| `-domain` | Primary name |
| `-email` | LE account contact |
| `-acme-cache` | Persistent dir (e.g. `/opt/0trust/data/acme-phisheries`) |
| `-acme-http` | Default `true` — listen `:80` for HTTP-01 |
| `-subalt` | Extra SANs not covered by domain/vhosts |
| `-vhosts` | Product apex list + upstreams |

Example (multi-product edge):

```bash
tunneltug-engine \
  -mode server \
  -prod \
  -routing direct \
  -domain phisheries.dev \
  -email ssl@example.com \
  -token "$TUNNELTUG_TOKEN" \
  -public 443 \
  -control 9011 \
  -acme-cache /opt/0trust/data/acme-phisheries \
  -vhosts /opt/0trust/config/tunneltug-vhosts-direct.yaml \
  -subalt www.phisheries.dev,www.tunneltug.com,hub.tunneltug.com,evilrbi.com,www.evilrbi.com,0trustos.com,www.0trustos.com
```

HTTP-01 listener:

```text
Listening for ACME HTTP-01 challenges on :80
```

## Cache layout

DirCache files under `-acme-cache`:

| Pattern | Meaning |
|---------|---------|
| `acme_account+key` | LE account |
| `<hostname>` | ECDSA leaf (ready) |
| `<hostname>+rsa` | RSA leaf if dual issued |
| `<hostname>+token` | In-flight private key (not ready) |
| `*+http-01` | Challenge material (transient) |

**Ready cert** = file named exactly like the hostname (no `+token`).  
Sister products work because those files already exist.

## Prerequisites before first issue

1. **DNS A/AAAA** for the hostname → this host’s public IP (from *public* resolvers).
2. **:80 and :443** reachable from the internet (HTTP-01 + HTTPS).
3. **No other process** bound to :80/:443.
4. Hostname on whitelist (flags + vhosts as above).
5. Authoritative NS for the zone must be **consistent and reachable** if LE multi-perspective validation is in play. Broken NS glue is a **DNS plane** problem; fix DNS, do not add certbot.

## Safe issue procedure

```bash
# 1) Config deployed, unit active, whitelist logged

# 2) Safe: domains that already have cache files
curl -sS -o /dev/null -w "%{http_code}\n" --resolve evilrbi.com:443:127.0.0.1 https://evilrbi.com/

# 3) New domain: ONE attempt only (local SNI to avoid client cache noise)
curl -sS -m 120 -o /dev/null -w "%{http_code}\n" \
  --resolve new.example.com:443:127.0.0.1 https://new.example.com/

# 4) Confirm cache
ls -la /path/to/acme-cache/new.example.com

# 5) If failure: READ journal once — do not retry in a loop
journalctl -u <unit> --since "5 min ago" | grep -iE 'acme|rateLimited|HostWhitelist|unable to satisfy'
```

### Rate limit (`429 … rateLimited`)

```text
too many failed authorizations (5) for "hostname" in the last 1h0m0s, retry after …
```

1. **Stop** all HTTPS clients for that hostname (including scanners if you can firewall SNI — optional).
2. Fix the underlying validation failure (DNS / :80 / dual ACME / wrong IP).
3. Wait until `retry after`.
4. Single attempt only.

While rate-limited, **do not** remove the hostname from whitelist just to “stop scanners” and then re-add and curl again in the same hour without fixing root cause — that pattern already cost multi-day outages.

## Dual-ACME ban list

Never list the same public marketing/product apex in:

- TunnelTug `-prod` edge **and**
- `0trust-services` / edge-standby `acme_domains` **and**
- platform `server.acme` for that apex

Platform may still **serve HTTP content** for `Host: marketing.example` on an internal port; TLS for the public name stays on TunnelTug.

## Dev vs prod

| Flag | TLS |
|------|-----|
| `-dev` | Self-signed for `-domain` (+ localhost); no LE |
| `-prod` | ACME |
| neither | Optional `-cert`/`-key`, else temporary self-signed |

Never run `-prod` and `-dev` together.

## BGPsec keys are not ACME

`-anycast-gen-bgpsec-key` writes an RPKI **router** key. Unrelated to Let's Encrypt. Do not mix the two mental models.
