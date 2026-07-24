# 08 — Kernel replication barges

Dedicated processes for **service data replication** between product embeds.  
Not mesh/SDF. Not a remote-primary DB products should “prefer over” local embeds.

## Modes

| Mode | Default listen |
|------|----------------|
| `-mode ultimate_db` | `:8480` |
| `-mode ultimate_keystore` | `:8481` |

```bash
tunneltug \
  -mode ultimate_db \
  -udb-listen :8480 \
  -udb-data /var/lib/tunneltug/kernel/ultimate_db \
  -udb-node-id pop-sfo-db \
  -udb-peers "pop-ams-db=https://ams.example:8480"
```

```bash
tunneltug \
  -mode ultimate_keystore \
  -uks-listen :8481 \
  -uks-data /var/lib/tunneltug/kernel/ultimate_keystore \
  -uks-node-id pop-sfo-ks \
  -uks-peers "pop-ams-ks=https://ams.example:8481"
```

Also ship as stack barges via `config/barges/ultimate_*.example.yaml` and `-mode stack`.

## Design rules

1. Products keep **local** ultimate_db/keystore embeds and `AddPeer` kernel URLs.  
2. Scale-out = more ingress PoPs + peer mesh — not one central store.  
3. Wire peers via site `kernel_mesh` ([09-site-config.md](09-site-config.md)) or explicit `-udb-peers` / YAML.

## ultimate_sync (replication state machine)

Package: `0TrustCloud/modules/ultimate_sync` — FSM for peer replication **states** and **messages** (not a remote-primary store).

| State | Meaning |
|-------|---------|
| `disconnected` → `connecting` → `catching_up` → `in_sync` | Happy path after dial + health + catch-up |
| `degraded` / `error` | Lag, partial fan-out, or hard failure |

Kernel barges expose:

- `GET /kernel/sync` — peer list + aggregate  
- `GET /kernel/health` may include `sync_aggregate`  
- `GET /kernel/info` embeds `ultimate_sync` object  

Subscribe with `Machine.OnMessage` to log/metric/orchid each transition (`type: ultimate_sync.state`).  
See `0TrustCloud/modules/ultimate_sync/README.md`.

### Run with KV pull catch-up

```bash
# PoP A
tunneltug -mode ultimate_db -udb-node-id sfo-db -udb-listen :8480 \
  -udb-peers "ams-db=http://ams.example:8480" -token "$TOKEN"

# PoP B  
tunneltug -mode ultimate_db -udb-node-id ams-db -udb-listen :8480 \
  -udb-peers "sfo-db=http://sfo.example:8480" -token "$TOKEN"

curl -sS http://127.0.0.1:8480/kernel/sync | jq .
curl -sS http://127.0.0.1:8480/kernel/health
```

Keystore plane: `-mode ultimate_keystore -uks-peers …` (same FSM, `KindKV`).  
Catch-up = scan peer `/kernel/kv` → merge into local store (no delete of local-only keys).
