# TunnelTug operator playbooks

Operational procedures for **every** TunnelTug mode and feature.  
Written so public TLS, fleets, hub, and stack work are done **only** through the `tunneltug` / `tunneltug-engine` binary and its configs — not certbot, lego, kubectl-as-control-plane, or registrar/DNS API hacks for ACME.

| Playbook | Feature area |
|----------|----------------|
| [00-golden-rules.md](00-golden-rules.md) | Hard rules (read first; includes post-mortem of rate-limit mistakes) |
| [01-acme-tls.md](01-acme-tls.md) | `-prod` ACME, cache, HostWhitelist, vhosts SANs |
| [02-server-edge-vhosts.md](02-server-edge-vhosts.md) | `-mode server`, direct/subdomain, product vhosts |
| [03-client-tunnel.md](03-client-tunnel.md) | `-mode client` |
| [04-lb-barges-k3s.md](04-lb-barges-k3s.md) | `-mode lb` / `barge`, k3s fleets |
| [05-hub-publish-stack.md](05-hub-publish-stack.md) | `-mode hub`, `hub-publish`, `stack`, 0TrustOS inject |
| [06-mesh-vpi-dns.md](06-mesh-vpi-dns.md) | Built-in mesh, VPI stub, `-dns` zones |
| [07-anycast.md](07-anycast.md) | `-mode anycast` / `-anycast` sidecar, BGPsec |
| [08-kernel-replication.md](08-kernel-replication.md) | `ultimate_db` / `ultimate_keystore` barges |
| [09-site-config.md](09-site-config.md) | Site YAML / Tugconf multi-PoP and sandbox |
| [10-diagnostics.md](10-diagnostics.md) | Safe checks (what to curl, what not to) |
| [11-host-ownership.md](11-host-ownership.md) | **systemd vs stack hostPort** — one owner per port (defcon.chat post-mortem) |
| [12-vm-runtime.md](12-vm-runtime.md) | **TunnelTug owns the VM** — HCS VM from WSL parts, tug-init, k3s node, `tunneltug vm` |

Related design docs: [ARCHITECTURES.md](../ARCHITECTURES.md), root [README.md](../../README.md), [deploy/hub/README.md](../../deploy/hub/README.md), [deploy/k3s/README.md](../../deploy/k3s/README.md).

Binary names in the field:

| Name | Meaning |
|------|---------|
| `tunneltug` | Upstream / local build |
| `tunneltug-engine` | Same binary installed on 0Trust edges (e.g. `/opt/0trust/bin/tunneltug-engine`) |

All commands below work with either name.
