//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// The Ubuntu userspace (Yosys, OpenROAD, the PDK, ~/eda) stays on the WSL ext4
// disk. An EDA job overlays that disk so writes land on the VM data disk, then
// chroots and execs the existing import.sh. Nothing in that script is rewritten.
const (
	ubuntuLower = "/.ubuntu"
	edaMerge    = "/var/lib/tug/ubuntu"
	edaUpper    = "/var/lib/tug/ovl/upper"
	edaWork     = "/var/lib/tug/ovl/work"
)

func runEDA(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "tug-init eda: missing command")
		return 2
	}
	if err := prepareEDARoot(); err != nil {
		fmt.Fprintf(os.Stderr, "tug-init eda: %v\n", err)
		return 1
	}
	if err := unix.Chroot(edaMerge); err != nil {
		fmt.Fprintf(os.Stderr, "tug-init eda: chroot: %v\n", err)
		return 1
	}
	if err := unix.Chdir("/"); err != nil {
		fmt.Fprintf(os.Stderr, "tug-init eda: chdir: %v\n", err)
		return 1
	}
	// HOME is grego's so import_pdk writes ~/runs and finds ~/eda. uid stays
	// root; the overlay is the VM's, and the distro disk is the lower layer.
	env := []string{
		"HOME=/home/grego",
		"USER=grego",
		"LOGNAME=grego",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=xterm",
		"LANG=C.UTF-8",
	}
	if err := unix.Exec(args[0], args, env); err != nil {
		fmt.Fprintf(os.Stderr, "tug-init eda: exec %s: %v\n", args[0], err)
		return 127
	}
	return 0
}

func prepareEDARoot() error {
	if !mountPoint(ubuntuLower) {
		return fmt.Errorf("ubuntu disk is not mounted at %s", ubuntuLower)
	}
	if !mountPoint(dataRoot) {
		return fmt.Errorf("data disk is not mounted at %s", dataRoot)
	}
	if !mountPoint(edaMerge) {
		if err := os.RemoveAll(edaWork); err != nil {
			return err
		}
		for _, d := range []string{edaUpper, edaWork, edaMerge} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
		}
		opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", ubuntuLower, edaUpper, edaWork)
		if err := unix.Mount("overlay", edaMerge, "overlay", unix.MS_NOATIME, opts); err != nil {
			return fmt.Errorf("overlay: %w", err)
		}
	}
	for _, mp := range mountPoints("9p") {
		if err := bindOnto(mp, filepath.Join(edaMerge, mp)); err != nil {
			return fmt.Errorf("share %s: %w", mp, err)
		}
	}
	for _, mp := range []string{"/proc", "/sys", "/dev", "/run"} {
		if err := bindOnto(mp, filepath.Join(edaMerge, mp)); err != nil {
			return fmt.Errorf("bind %s: %w", mp, err)
		}
	}
	// Same binary, two names. argv0 selects wslpath or the Windows binfmt bridge.
	// Both have to exist inside the chroot, where the proof looks them up.
	for _, name := range []string{"wslpath", "tug-win"} {
		if err := bindFile("/init", filepath.Join(edaMerge, "/usr/local/bin", name)); err != nil {
			return err
		}
	}
	if err := registerWinBinfmt(); err != nil {
		return err
	}
	return nil
}

func bindOnto(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	err := unix.Mount(src, dst, "", unix.MS_BIND|unix.MS_REC, "")
	if err == unix.EBUSY {
		return nil
	}
	return err
}

func bindFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil && !os.IsExist(err) {
		return err
	}
	if f != nil {
		f.Close()
	}
	err = unix.Mount(src, dst, "", unix.MS_BIND, "")
	if err == unix.EBUSY {
		return nil
	}
	return err
}

func registerWinBinfmt() error {
	const fs = "/proc/sys/fs/binfmt_misc"
	if !mountPoint(fs) {
		_ = os.MkdirAll(fs, 0o755)
		if err := unix.Mount("binfmt_misc", fs, "binfmt_misc", 0, ""); err != nil && err != unix.EBUSY {
			return fmt.Errorf("binfmt_misc: %w", err)
		}
	}
	// MZ → tug-win, which asks the Windows host to run the binary. Dawn is a
	// PE file; the proof execs it by path and reads its stdout.
	reg := ":tugwin:M::MZ::/usr/local/bin/tug-win:\n"
	err := os.WriteFile(filepath.Join(fs, "register"), []byte(reg), 0o644)
	if err != nil && !os.IsExist(err) {
		return fmt.Errorf("binfmt register: %w", err)
	}
	return nil
}

func mountPoint(target string) bool {
	for _, mp := range mountPoints("") {
		if mp == target {
			return true
		}
	}
	return false
}

func mountPoints(fstype string) []string {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		if fstype != "" && f[2] != fstype {
			continue
		}
		out = append(out, unescapeMount(f[1]))
	}
	return out
}

func unescapeMount(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			var n byte
			ok := true
			for _, c := range s[i+1 : i+4] {
				if c < '0' || c > '7' {
					ok = false
					break
				}
				n = n*8 + byte(c-'0')
			}
			if ok {
				b.WriteByte(n)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
