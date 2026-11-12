# 12 — VM runtime (TunnelTug owns the VM)

TunnelTug boots and owns a Linux VM on Windows, the way `wslservice` owns a WSL
session VM. The VM is the k8s node TunnelTug schedules onto. WSL does not run
TunnelTug; TunnelTug runs a VM built from WSL's parts.

```
tunneltug.exe vm up ──HCS──▶ VM (WSL kernel, TunnelTug initramfs)
   │  hvsocket 50100  ◀──── tug-init (PID 1): control session (yamux)
   │  hvsocket 50102  ◀──── tug-init net: eth0 frames → gvisor userspace NAT/DNS
   │  Plan9 50110+    ◀──── host shares mounted at /mnt/<name>
   │  127.0.0.1:16443 ────▶ guest k3s API (kubeconfig in the VM home)
   └─ 127.0.0.1:<api>      local API: status / exec / stop (token in vm.json)
```

## Sandbox

The site document names this VM a sandbox. It is the k3s node the stack schedules onto. See [09-site-config.md](09-site-config.md) and `config/site.sandbox.example.yaml`.

```powershell
tunneltug -config config/site.sandbox.example.yaml -pop sfo -config-check
tunneltug vm up -config config/site.sandbox.example.yaml -pop sfo
tunneltug -config config/site.sandbox.example.yaml -pop sfo -mode stack -token "$TOKEN"
```

Flags on `vm up` win over the sandbox block. Product workloads stay stack barges.

## What is forked from microsoft/WSL

| TunnelTug | WSL source |
|---|---|
| `vm_schema.go` HCS document | `src/windows/common/hcs_schema.h`, `src/windows/service/exe/HcsVirtualMachine.cpp` (schema 2.7, LinuxKernelDirect, SCSI, HvSocket SD) |
| `vm_hcs_windows.go` | `src/windows/common/hcs.cpp` (computecore.dll) |
| kernel cmdline | `helpers::AppendCommonKernelCommandLine` |
| modules VHD overlay, Plan9 `trans=fd` mount | `src/linux/init/main.cpp` |
| kernel + `modules.vhd` | the installed WSL's `tools\`, pinned into the VM home |

Not taken: WSL's init protocol (ports 50000–50006), wslservice/COM, WSLC's dockerd.
TunnelTug's guest is `cmd/tug-init` and k3s; containers are k8s pods.

## Guest image

`tunneltug vm image` builds `initrd.img` (newc cpio, root-owned) from verified inputs:
Alpine minirootfs (sha256 from `latest-releases.yaml`) → `APKINDEX` (RSA, keys from that
minirootfs) → each `.apk` (control Q1 sha1 + data sha256) → k3s (release sha256) →
`tug-init` as `/init`. `initrd.json` records every component. The root is immutable
(tmpfs, like an image); state lives on `data.vhdx` (`/var/lib/tug`).

## One-time host setup

HCS requires Administrators or **Hyper-V Administrators** (`HCS_E_ACCESS_DENIED`, 0x8037011B):

```powershell
# elevated, once; then sign out and back in
Add-LocalGroupMember -Group "Hyper-V Administrators" -Member "$env:USERNAME"
```

## Day-1

```powershell
make build                               # builds vmguest/tug-init, embeds it
tunneltug vm probe                       # HCS reachable? schemas, kernel source
tunneltug vm up -name eda -memory 12288 `
  -share wasmapollo=C:\Users\grego\WASMApollo:/mnt/wasmapollo
tunneltug vm status -name eda
tunneltug vm exec -name eda -- cat /var/lib/tug/log/k3s.log
tunneltug vm down -name eda
```

State: `%LOCALAPPDATA%\TunnelTug\vm\<name>\` (`kernel`, `modules.vhd`, `initrd.img`,
`data.vhdx`, `console.log`, `hcs.json`, `vm.json`, `kubeconfig.yaml`).

## OpenViaduct

One design is one VM. The VM attaches the default WSL distro's `ext4.vhdx`
read-only, overlays it onto the data disk, and execs `pnr/flow/import.sh`
unchanged (logic proof, Yosys, OpenViaduct prerun, OpenROAD). Writes stay on
the data disk. `import_pdk.py` publishes the text reports back onto the
WASMApollo share. Dawn is a Windows binary: the guest asks the host to run it.

WSL is shut down for the attach. Open Ubuntu again after the VM is down.
This logon has to include Hyper-V Administrators (sign out once after joining
the group).

```powershell
tunneltug vm eda ihp-sg13g2 jev_dot_pipe
tunneltug vm eda gf180mcuD --through drt --prerun-size
```

With no design names, the command reads `BLOCKS` from `import_pdk.py` and runs
those VMs one after another. Each VM is `eda-<design>` under
`%LOCALAPPDATA%\TunnelTug\vm\`.

## Rules

- One heavy EDA job per VM memory budget: the VM's `-memory` is its own (not shared
  with the Ubuntu WSL VM), size it for OpenROAD (place/route ≈ 8 GB).
- `vm exec` is for bring-up and diagnostics in the node namespace. Product
  workloads run as k8s pods through TunnelTug, never kubectl as the control
  plane (00-golden-rules). An OpenViaduct design is the VM above, not a pod.
