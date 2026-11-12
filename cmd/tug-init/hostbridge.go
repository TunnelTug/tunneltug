//go:build linux

package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/hashicorp/yamux"

	"tunneltug/vmproto"
)

// The guest is the yamux client. PID 1 also opens streams toward the host when
// a chrooted proof execs a Windows binary (Dawn). The unix socket is how that
// short-lived interpreter reaches PID 1; /run is bind-mounted into the chroot.
const hostBridgeSock = "/run/tug-host.sock"

var (
	bridgeMu   sync.Mutex
	bridgeSess *yamux.Session
)

func setBridgeSession(s *yamux.Session) {
	bridgeMu.Lock()
	bridgeSess = s
	bridgeMu.Unlock()
}

func startHostBridge() {
	_ = os.Remove(hostBridgeSock)
	ln, err := net.Listen("unix", hostBridgeSock)
	if err != nil {
		bootLog("host bridge: %v", err)
		return
	}
	_ = os.Chmod(hostBridgeSock, 0o600)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleHostBridge(c)
		}
	}()
}

func handleHostBridge(c net.Conn) {
	defer c.Close()
	hdr, _, err := vmproto.ReadHeader(c)
	if err != nil {
		return
	}
	bridgeMu.Lock()
	sess := bridgeSess
	bridgeMu.Unlock()
	if sess == nil {
		_ = vmproto.WriteTrailer(c, vmproto.Trailer{Exit: 127, Error: "host bridge is not up"})
		return
	}
	st, err := sess.Open()
	if err != nil {
		_ = vmproto.WriteTrailer(c, vmproto.Trailer{Exit: 127, Error: err.Error()})
		return
	}
	defer st.Close()
	if err := vmproto.WriteHeader(st, hdr); err != nil {
		_ = vmproto.WriteTrailer(c, vmproto.Trailer{Exit: 127, Error: err.Error()})
		return
	}
	_, _ = io.Copy(c, st)
}

// runTugWin is the binfmt interpreter for PE files. argv[1] is the program the
// proof exec'd (a /mnt/<drive> path). The host runs that Windows path.
func runTugWin(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "tug-win: missing program")
		return 127
	}
	prog := args[0]
	if strings.HasPrefix(prog, "/mnt/") {
		win, err := vmproto.WSLPath("w", prog)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tug-win: %v\n", err)
			return 127
		}
		prog = win
	}
	c, err := net.Dial("unix", hostBridgeSock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tug-win: %v\n", err)
		return 127
	}
	defer c.Close()
	argv := append([]string{prog}, args[1:]...)
	if err := vmproto.WriteHeader(c, vmproto.Header{Op: vmproto.OpHost, Argv: argv}); err != nil {
		fmt.Fprintf(os.Stderr, "tug-win: %v\n", err)
		return 127
	}
	tr, err := vmproto.CopyExecOutput(os.Stdout, c)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tug-win: %v\n", err)
		return 127
	}
	if tr.Error != "" {
		fmt.Fprintf(os.Stderr, "tug-win: %s\n", tr.Error)
	}
	if tr.Exit != 0 {
		return tr.Exit
	}
	return 0
}

// runWslpath answers `wslpath -w` / `-u` from the logic proof. Ubuntu's own
// wslpath talks to wslservice, which is not in this VM.
func runWslpath(args []string) int {
	mode := ""
	path := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-w", "-u":
			mode = a[1:]
		case "-a", "-m":
		case "--":
			if i+1 < len(args) {
				path = args[i+1]
			}
			i = len(args)
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "wslpath: unsupported %s\n", a)
				return 1
			}
			path = a
		}
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "wslpath: missing path")
		return 1
	}
	if mode == "" {
		if strings.HasPrefix(path, "/mnt/") {
			mode = "w"
		} else {
			mode = "u"
		}
	}
	got, err := vmproto.WSLPath(mode, path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(got)
	return 0
}
