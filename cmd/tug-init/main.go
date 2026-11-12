//go:build linux

// tug-init is PID 1 of a TunnelTug VM. The Windows host (tunneltug vm up) boots
// the kernel with this binary as the initramfs /init; it brings the node up and
// then serves the host's control session over vsock.
//
// Boot order: tmpfs root switch → kernel filesystems → disks (modules, data) →
// identity → network forwarder (gvisor-tap-vsock) → host shares (Plan9) →
// k3s → control session (hello, then dial/exec/status/stop streams).
package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const agentVersion = "tug-init/0.1"

func main() {
	// Bind-mounted names. argv0 selects the helper; the boot path is PID 1.
	switch filepath.Base(os.Args[0]) {
	case "tug-win":
		os.Exit(runTugWin(os.Args[1:]))
	case "wslpath":
		os.Exit(runWslpath(os.Args[1:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "net" {
		runNet(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "eda" {
		os.Exit(runEDA(os.Args[2:]))
	}
	start := time.Now()
	if os.Getpid() == 1 && os.Getenv("TUG_SWITCHED") == "" {
		if err := switchToTmpfs(); err != nil {
			// Keep booting on the initramfs root; containers will fail to pivot_root.
			bootLog("tmpfs root switch failed: %v", err)
		}
	}
	earlyMounts()
	startHostBridge()
	if f, err := os.OpenFile("/dev/kmsg", os.O_WRONLY, 0); err == nil {
		kmsg = f
	}
	cmdline, _ := os.ReadFile("/proc/cmdline")
	cfg := parseCmdline(string(cmdline))
	bootLog("%s booting %s (disks=%v shares=%d)", agentVersion, cfg.Name, cfg.Disks, len(cfg.Shares))

	r := newReaper()
	sup := &supervisor{r: r}
	var warnings []string

	_ = runWait(r, []string{"/sbin/ip", "link", "set", "lo", "up"})
	warnings = append(warnings, setupDisks(cfg, r)...)
	for _, d := range []string{dataRoot + "/k3s", dataRoot + "/log"} {
		_ = os.MkdirAll(d, 0o755)
	}
	writeHostIdentity(cfg.Name)
	// tun first: WSL's kernel builds it as a module and the network forwarder needs
	// /dev/net/tun (first boot, 2026-10-10: eth0 never came up, k3s crash-looped).
	for _, m := range []string{"tun", "overlay", "br_netfilter", "nf_conntrack", "vxlan", "ip_tables", "iptable_nat", "iptable_filter", "xt_conntrack", "xt_MASQUERADE"} {
		_ = runWait(r, []string{"/sbin/modprobe", "-q", m})
	}
	setSysctls()

	// gvisor-tap-vsock forwarder: tap eth0 over vsock to the host's userspace
	// network, static 192.168.127.2 (gateway/DNS .1). k3s waits for it.
	sup.start(&service{
		name:    "network",
		argv:    []string{"/init", "net", "-iface", "eth0", "-mtu", "1500"},
		env:     baseEnv(),
		logPath: dataRoot + "/log/network.log",
	})
	if ip := waitForIPv4("eth0", 30*time.Second); ip == "" {
		warnings = append(warnings, "eth0 has no IPv4 after 30s; k3s starts without egress")
	}

	warnings = append(warnings, mountShares(cfg.Shares)...)

	sup.start(&service{
		name: "k3s",
		argv: []string{
			"/usr/local/bin/k3s", "server",
			"--data-dir=" + dataRoot + "/k3s",
			"--node-name=" + cfg.Name,
			"--write-kubeconfig=" + dataRoot + "/k3s/kubeconfig.yaml",
			"--write-kubeconfig-mode=0600",
			"--tls-san=127.0.0.1",
			"--disable=traefik,servicelb",
			"--flannel-iface=eth0",
			"--kubelet-arg=cgroup-driver=cgroupfs",
		},
		env:     baseEnv(),
		logPath: dataRoot + "/log/k3s.log",
	})

	for _, w := range warnings {
		bootLog("warning: %s", w)
	}
	hello := helloFor(cfg, start, warnings)
	for {
		if err := serveControl(cfg, sup, r, hello); err != nil {
			bootLog("control: %v (reconnecting)", err)
		}
		time.Sleep(2 * time.Second)
	}
}

func waitForIPv4(iface string, wait time.Duration) string {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if ip := ifaceIPv4(iface); ip != "" {
			return ip
		}
		time.Sleep(250 * time.Millisecond)
	}
	return ""
}

func ifaceIPv4(name string) string {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	return ""
}

func helloFor(cfg bootConfig, start time.Time, warnings []string) *helloInfo {
	var uts unix.Utsname
	_ = unix.Uname(&uts)
	var shares []string
	for _, sh := range cfg.Shares {
		shares = append(shares, sh.Name+"="+sh.Mount)
	}
	return &helloInfo{
		name:     cfg.Name,
		kernel:   strings.TrimSpace(unix.ByteSliceToString(uts.Release[:])),
		disks:    cfg.Disks,
		shares:   shares,
		bootMS:   time.Since(start).Milliseconds(),
		warnings: warnings,
	}
}
