# Shadow anycast profile

Standard anycast edge with:

- extra `dns.private_suffixes` (e.g. `.com`)
- local `dns.zone_pack` (ZoneSnapshot JSON)
- optional `origin` HTTP face
- `bgp.ibgp` same-ASN peer default

## Run

```bash
go build -o bin/tunneltug .
./bin/tunneltug -mode anycast -anycast-config config/shadow.example.yaml
```

```bash
dig @127.0.0.1 -p 15353 www.example.com +short
curl -s -H 'Host: www.example.com' http://127.0.0.1:8080/
curl -s http://127.0.0.1:19099/status
curl -s http://127.0.0.1:19099/ready
```

## Config keys

| Key | Role |
|-----|------|
| `dns.private_suffixes` | Authoritative suffixes (split-horizon) |
| `dns.zone_pack` | Local zone file/dir; `{{VIP}}` → anycast IP |
| `origin` | Optional HTTP origin on the edge |
| `bgp.ibgp` | `peer_asn` defaults to `local_asn` when unset |

BIRD iBGP speaker examples: `bird/`.
