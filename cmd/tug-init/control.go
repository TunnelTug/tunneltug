//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"

	"tunneltug/vmproto"
)

type helloInfo struct {
	name     string
	kernel   string
	disks    []string
	shares   []string
	bootMS   int64
	warnings []string
}

func dialHostVsock(port uint32) (net.Conn, error) {
	return vsock.Dial(vmproto.HostCID, port, nil)
}

// serveControl runs one control session until the host goes away.
func serveControl(cfg bootConfig, sup *supervisor, r *reaper, h *helloInfo) error {
	conn, err := dialHostVsock(cfg.CtlPort)
	if err != nil {
		return fmt.Errorf("dial host port %d: %w", cfg.CtlPort, err)
	}
	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard
	ycfg.KeepAliveInterval = 10 * time.Second
	sess, err := yamux.Client(conn, ycfg)
	if err != nil {
		conn.Close()
		return err
	}
	defer sess.Close()
	setBridgeSession(sess)
	defer setBridgeSession(nil)

	st, err := sess.Open()
	if err != nil {
		return err
	}
	err = vmproto.WriteHeader(st, vmproto.Header{Op: vmproto.OpHello, Hello: &vmproto.Hello{
		Name: h.name, Kernel: h.kernel, Agent: agentVersion, Disks: h.disks,
		Shares: h.shares, BootMS: h.bootMS, Warnings: h.warnings,
	}})
	st.Close()
	if err != nil {
		return err
	}
	bootLog("control session up (boot %d ms)", h.bootMS)

	for {
		stream, err := sess.Accept()
		if err != nil {
			return err
		}
		go handleStream(stream, sup, r)
	}
}

func handleStream(c net.Conn, sup *supervisor, r *reaper) {
	defer c.Close()
	h, br, err := vmproto.ReadHeader(c)
	if err != nil {
		return
	}
	switch h.Op {
	case vmproto.OpDial:
		target, err := net.DialTimeout("tcp", h.Addr, 10*time.Second)
		if err != nil {
			return
		}
		defer target.Close()
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(target, br); closeWrite(target); done <- struct{}{} }()
		go func() { _, _ = io.Copy(c, target); closeWrite(c); done <- struct{}{} }()
		<-done
		<-done
	case vmproto.OpExec:
		runExec(c, h, r)
	case vmproto.OpStatus:
		writeJSONLine(c, guestStatus(sup))
	case vmproto.OpStop:
		bootLog("stop requested by host")
		_ = vmproto.WriteTrailer(c, vmproto.Trailer{Exit: 0})
		c.Close()
		shutdown(sup)
	}
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

func runExec(c net.Conn, h vmproto.Header, r *reaper) {
	if len(h.Argv) == 0 {
		_ = vmproto.WriteTrailer(c, vmproto.Trailer{Exit: 127, Error: "empty argv"})
		return
	}
	argv := append([]string(nil), h.Argv...)
	if !strings.Contains(argv[0], "/") {
		if p := lookPath(argv[0]); p != "" {
			argv[0] = p
		}
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		_ = vmproto.WriteTrailer(c, vmproto.Trailer{Exit: 127, Error: err.Error()})
		return
	}
	devnull, _ := os.Open("/dev/null")
	env := append(baseEnv(), h.Env...)
	_, done, err := r.spawn(argv, env, "/", []uintptr{devnull.Fd(), pw.Fd(), pw.Fd()})
	devnull.Close()
	pw.Close()
	if err != nil {
		pr.Close()
		_ = vmproto.WriteTrailer(c, vmproto.Trailer{Exit: 127, Error: err.Error()})
		return
	}
	_, _ = io.Copy(c, pr)
	pr.Close()
	_ = vmproto.WriteTrailer(c, vmproto.Trailer{Exit: exitCode(<-done)})
}

func lookPath(name string) string {
	for _, dir := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		p := dir + "/" + name
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func guestStatus(sup *supervisor) vmproto.Status {
	st := vmproto.Status{Services: sup.states(), IP: ifaceIPv4("eth0")}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				st.UptimeS = int64(v)
			}
		}
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			st.Load = strings.Join(f[:3], " ")
		}
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, _ := strings.Cut(sc.Text(), ":")
			n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			switch k {
			case "MemTotal":
				st.MemTotal = n
			case "MemAvailable":
				st.MemAvail = n
			}
		}
		f.Close()
	}
	var fs unix.Statfs_t
	if unix.Statfs(dataRoot, &fs) == nil {
		st.DataFree = fs.Bavail * uint64(fs.Bsize)
	}
	return st
}

func writeJSONLine(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write(append(b, '\n'))
}

func shutdown(sup *supervisor) {
	sup.stopAll(30 * time.Second)
	unix.Sync()
	_ = unix.Unmount(dataRoot, 0)
	unix.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}
