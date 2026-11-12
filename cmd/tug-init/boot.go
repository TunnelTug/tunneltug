//go:build linux

package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const dataRoot = "/var/lib/tug"

var kmsg *os.File

func bootLog(format string, args ...any) {
	msg := fmt.Sprintf("tug-init: "+format, args...)
	if kmsg != nil {
		fmt.Fprintf(kmsg, "<6>%s\n", msg)
	}
	fmt.Fprintln(os.Stderr, msg)
}

// bootConfig is what the host put on the kernel command line (tug.* keys).
type bootConfig struct {
	Name    string
	Disks   []string // role per SCSI LUN
	CtlPort uint32
	Shares  []shareSpec
}

type shareSpec struct {
	Name  string
	Port  uint32
	Mount string
	RO    bool
}

func parseCmdline(cmdline string) bootConfig {
	cfg := bootConfig{Name: "tug", CtlPort: 50100}
	for _, f := range strings.Fields(cmdline) {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		switch k {
		case "tug.name":
			cfg.Name = v
		case "tug.disks":
			cfg.Disks = strings.Split(v, ",")
		case "tug.ctl":
			if p, err := strconv.ParseUint(v, 10, 32); err == nil {
				cfg.CtlPort = uint32(p)
			}
		case "tug.share": // name:port:/mount:rw|ro
			parts := strings.Split(v, ":")
			if len(parts) != 4 {
				continue
			}
			p, err := strconv.ParseUint(parts[1], 10, 32)
			if err != nil {
				continue
			}
			cfg.Shares = append(cfg.Shares, shareSpec{Name: parts[0], Port: uint32(p), Mount: parts[2], RO: parts[3] == "ro"})
		}
	}
	return cfg
}

// switchToTmpfs moves an initramfs root into tmpfs and re-execs /init there.
// Containers pivot_root out of the host root, which the kernel refuses for the
// initramfs "rootfs" mount; LinuxKit's init does the same switch.
func switchToTmpfs() error {
	const newRoot = "/.tugroot"
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", newRoot, "tmpfs", 0, "mode=0755"); err != nil {
		return fmt.Errorf("tmpfs: %w", err)
	}
	if err := copyTree("/", newRoot, newRoot); err != nil {
		return fmt.Errorf("copy root: %w", err)
	}
	if err := unix.Chdir(newRoot); err != nil {
		return err
	}
	if err := unix.Mount(newRoot, "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move root: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return err
	}
	if err := unix.Chdir("/"); err != nil {
		return err
	}
	return unix.Exec("/init", os.Args, append(os.Environ(), "TUG_SWITCHED=1"))
}

// copyTree copies src into dst preserving mode, owner and links, deleting each
// source file once copied so the initramfs pages are freed as we go.
func copyTree(src, dst, skip string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		if s == skip {
			continue
		}
		d := filepath.Join(dst, e.Name())
		var st unix.Stat_t
		if err := unix.Lstat(s, &st); err != nil {
			return err
		}
		mode := st.Mode & 0o7777
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if err := os.Mkdir(d, os.FileMode(mode)); err != nil && !os.IsExist(err) {
				return err
			}
			if err := copyTree(s, d, skip); err != nil {
				return err
			}
			_ = unix.Chmod(d, mode)
		case unix.S_IFLNK:
			target, err := os.Readlink(s)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, d); err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := copyFile(s, d, mode); err != nil {
				return err
			}
			_ = os.Remove(s)
		default: // device nodes, fifos
			if err := unix.Mknod(d, st.Mode, int(st.Rdev)); err != nil {
				return err
			}
		}
		_ = unix.Lchown(d, int(st.Uid), int(st.Gid))
		if st.Mode&unix.S_IFMT != unix.S_IFLNK {
			_ = unix.Chmod(d, mode) // chown clears setuid
		}
	}
	return nil
}

func copyFile(src, dst string, mode uint32) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(mode))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type mountSpec struct {
	source, target, fstype string
	flags                  uintptr
	data                   string
}

func earlyMounts() {
	for _, m := range []mountSpec{
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"},
		{"devpts", "/dev/pts", "devpts", unix.MS_NOSUID | unix.MS_NOEXEC, "gid=5,mode=0620,ptmxmode=0666"},
		{"tmpfs", "/dev/shm", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
		{"tmpfs", "/run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=0755"},
		{"tmpfs", "/tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
		{"cgroup2", "/sys/fs/cgroup", "cgroup2", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, "nsdelegate"},
	} {
		_ = os.MkdirAll(m.target, 0o755)
		if err := unix.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil && err != unix.EBUSY {
			bootLog("mount %s: %v", m.target, err)
		}
	}
	// kubelet needs shared propagation on / for volume mounts.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_SHARED, ""); err != nil {
		bootLog("make-rshared /: %v", err)
	}
	// Hand every cgroup v2 controller to the children (kubelet's cgroupfs driver).
	if ctrls, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		var enable []string
		for _, c := range strings.Fields(string(ctrls)) {
			enable = append(enable, "+"+c)
		}
		if err := os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", []byte(strings.Join(enable, " ")), 0); err != nil {
			bootLog("cgroup subtree_control: %v", err)
		}
	}
}

// diskByLun finds /dev/sdX for SCSI LUN n (hv_storvsc: /sys/bus/scsi/devices/H:C:T:L/block/sdX).
func diskByLun(lun int, wait time.Duration) (string, error) {
	deadline := time.Now().Add(wait)
	for {
		matches, _ := filepath.Glob(fmt.Sprintf("/sys/bus/scsi/devices/*:*:*:%d/block/*", lun))
		if len(matches) > 0 {
			return "/dev/" + filepath.Base(matches[0]), nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no block device for LUN %d after %s", lun, wait)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func hasExtSuperblock(dev string) bool {
	f, err := os.Open(dev)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [2]byte
	if _, err := f.ReadAt(magic[:], 1024+0x38); err != nil {
		return false
	}
	return magic[0] == 0x53 && magic[1] == 0xEF
}

// setupDisks mounts each LUN by its role and returns warnings.
func setupDisks(cfg bootConfig, r *reaper) []string {
	var warn []string
	for lun, role := range cfg.Disks {
		dev, err := diskByLun(lun, 15*time.Second)
		if err != nil {
			warn = append(warn, err.Error())
			continue
		}
		switch role {
		case "modules":
			if err := mountModules(dev); err != nil {
				warn = append(warn, "modules: "+err.Error())
			}
		case "data":
			if !hasExtSuperblock(dev) {
				bootLog("data disk %s is blank, formatting ext4", dev)
				if code := runWait(r, []string{"/sbin/mkfs.ext4", "-q", "-F", "-L", "tugdata", "-E", "lazy_itable_init=1,lazy_journal_init=1", dev}); code != 0 {
					warn = append(warn, fmt.Sprintf("mkfs.ext4 %s exited %d", dev, code))
					continue
				}
			}
			_ = os.MkdirAll(dataRoot, 0o755)
			if err := unix.Mount(dev, dataRoot, "ext4", unix.MS_NOATIME, "discard"); err != nil {
				warn = append(warn, fmt.Sprintf("mount data %s: %v", dev, err))
			}
		case "ubuntu":
			// The WSL ext4 disk, attached read-only. EDA jobs overlay it; they do not
			// write the distro. A dirty journal needs one clean `wsl --shutdown` first.
			_ = os.MkdirAll(ubuntuLower, 0o755)
			if err := unix.Mount(dev, ubuntuLower, "ext4", unix.MS_RDONLY, ""); err != nil {
				warn = append(warn, "ubuntu: "+err.Error())
			}
		}
	}
	return warn
}

// mountModules follows WSL's init: the artifacts VHD nests <release>/modules,
// older module VHDs are flat; overlay a writable layer so depmod works.
func mountModules(dev string) error {
	const vhd = "/.modules"
	_ = os.MkdirAll(vhd, 0o755)
	if err := unix.Mount(dev, vhd, "ext4", unix.MS_RDONLY, ""); err != nil {
		return err
	}
	var uts unix.Utsname
	_ = unix.Uname(&uts)
	release := unix.ByteSliceToString(uts.Release[:])
	lower := vhd
	if st, err := os.Stat(filepath.Join(vhd, release, "modules")); err == nil && st.IsDir() {
		lower = filepath.Join(vhd, release, "modules")
	}
	target := filepath.Join("/lib/modules", release)
	upper, work := "/run/modules/upper", "/run/modules/work"
	for _, d := range []string{target, upper, work} {
		_ = os.MkdirAll(d, 0o755)
	}
	return unix.Mount("overlay", target, "overlay", unix.MS_NOATIME|unix.MS_NOSUID|unix.MS_NODEV,
		fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, upper, work))
}

// mountShares mounts each host share over Plan9 on its own vsock connection,
// exactly as WSL's init mounts a Plan9 share (trans=fd on a connected socket).
func mountShares(shares []shareSpec) []string {
	var warn []string
	for _, sh := range shares {
		fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			warn = append(warn, "vsock: "+err.Error())
			continue
		}
		if err := unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: sh.Port}); err != nil {
			unix.Close(fd)
			warn = append(warn, fmt.Sprintf("share %s: connect port %d: %v", sh.Name, sh.Port, err))
			continue
		}
		_ = os.MkdirAll(sh.Mount, 0o755)
		var flags uintptr = unix.MS_NOATIME
		if sh.RO {
			flags |= unix.MS_RDONLY
		}
		opts := fmt.Sprintf("msize=65536,trans=fd,rfdno=%d,wfdno=%d,cache=mmap,aname=%s", fd, fd, sh.Name)
		if err := unix.Mount(sh.Name, sh.Mount, "9p", flags, opts); err != nil {
			warn = append(warn, fmt.Sprintf("share %s on %s: %v", sh.Name, sh.Mount, err))
		}
		unix.Close(fd) // the 9p transport holds its own reference
	}
	return warn
}

// runWait runs a short command to completion with output on the console.
func runWait(r *reaper, argv []string) int {
	console, err := os.OpenFile("/dev/console", os.O_WRONLY, 0)
	if err != nil {
		console = os.Stderr
	}
	devnull, _ := os.Open("/dev/null")
	_, done, err := r.spawn(argv, baseEnv(), "/", []uintptr{devnull.Fd(), console.Fd(), console.Fd()})
	devnull.Close()
	if err != nil {
		bootLog("%v", err)
		return 127
	}
	return exitCode(<-done)
}

func baseEnv() []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"TERM=xterm",
	}
}

func writeHostIdentity(name string) {
	_ = unix.Sethostname([]byte(name))
	_ = os.WriteFile("/etc/hostname", []byte(name+"\n"), 0o644)
	_ = os.WriteFile("/etc/hosts", []byte("127.0.0.1 localhost "+name+"\n::1 localhost\n"), 0o644)

	// A stable machine-id per data disk: kubelet derives the node's system UUID from it.
	idPath := filepath.Join(dataRoot, "machine-id")
	id, err := os.ReadFile(idPath)
	if err != nil || len(strings.TrimSpace(string(id))) != 32 {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		id = []byte(hex.EncodeToString(b) + "\n")
		_ = os.WriteFile(idPath, id, 0o444)
	}
	_ = os.WriteFile("/etc/machine-id", id, 0o444)
}

func setSysctls() {
	for k, v := range map[string]string{
		"net/ipv4/ip_forward":                 "1",
		"net/ipv6/conf/all/forwarding":        "1",
		"net/bridge/bridge-nf-call-iptables":  "1",
		"net/bridge/bridge-nf-call-ip6tables": "1",
		"fs/inotify/max_user_instances":       "8192",
		"fs/inotify/max_user_watches":         "524288",
		"vm/overcommit_memory":                "1",
		"kernel/panic":                        "10",
	} {
		if err := os.WriteFile("/proc/sys/"+k, []byte(v), 0); err != nil {
			bootLog("sysctl %s: %v", k, err)
		}
	}
}
