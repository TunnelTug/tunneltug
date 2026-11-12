// Package vmproto is the host↔guest control protocol of the TunnelTug VM.
//
// The guest agent (tug-init, PID 1 in the VM) dials the host over hvsocket/vsock
// and runs a yamux session on that one connection. Each yamux stream starts with
// a single JSON header line naming the operation; what follows depends on it:
//
//	hello  guest→host  header carries Hello; stream closes
//	dial   host→guest  guest connects Addr (tcp) and both sides splice bytes
//	exec   host→guest  guest runs Argv; stdout+stderr stream back, then a Trailer line
//	status host→guest  guest answers one Status JSON line
//	stop   host→guest  guest shuts the VM down cleanly (sync, unmount, poweroff)
package vmproto

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// hvsocket/vsock ports (Linux AF_VSOCK port N = hvsocket service N-FACB-11E6-BD58-64006A7986D3).
const (
	ControlPort = 50100
	NetworkPort = 50102 // gvisor-tap-vsock frames (POST /connect, then hyperkit protocol)
	Plan9Base   = 50110
	HostCID     = 2 // VMADDR_CID_HOST

	// The host's userspace network (gvisor-tap-vsock). The guest address is static:
	// the host leases it to the guest MAC, so no DHCP client runs in the VM.
	Subnet     = "192.168.127.0/24"
	GatewayIP  = "192.168.127.1"
	GuestIP    = "192.168.127.2"
	GuestCIDR  = GuestIP + "/24"
	HostAlias  = "192.168.127.254" // NAT to the Windows host's 127.0.0.1
	GuestMAC   = "5a:94:ef:e4:0c:ee"
	GatewayMAC = "5a:94:ef:e4:0c:dd"
	maxHeader  = 1 << 16
)

const (
	OpHello  = "hello"
	OpDial   = "dial"
	OpExec   = "exec"
	OpStatus = "status"
	OpStop   = "stop"
	OpHost   = "host" // guest asks the host to run a Windows binary (Dawn)
)

type Header struct {
	Op    string   `json:"op"`
	Addr  string   `json:"addr,omitempty"`
	Argv  []string `json:"argv,omitempty"`
	Env   []string `json:"env,omitempty"`
	Hello *Hello   `json:"hello,omitempty"`
}

// Hello is the guest's first report after boot.
type Hello struct {
	Name     string   `json:"name"`
	Kernel   string   `json:"kernel"`
	Agent    string   `json:"agent"`
	Disks    []string `json:"disks"`
	Shares   []string `json:"shares"`
	BootMS   int64    `json:"boot_ms"`
	Warnings []string `json:"warnings,omitempty"`
}

// Status is the guest's live state.
type Status struct {
	UptimeS  int64             `json:"uptime_s"`
	MemTotal uint64            `json:"mem_total_kb"`
	MemAvail uint64            `json:"mem_avail_kb"`
	Load     string            `json:"load"`
	IP       string            `json:"ip"`
	Services map[string]string `json:"services"` // name → running|exited(N)|restarting
	DataFree uint64            `json:"data_free_bytes"`
}

// Trailer ends an exec stream.
type Trailer struct {
	Exit  int    `json:"exit"`
	Error string `json:"error,omitempty"`
}

// trailerMark prefixes the trailer line so it can follow arbitrary output.
const trailerMark = "\x00tug-exit "

func WriteHeader(w io.Writer, h Header) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if len(b) >= maxHeader {
		return fmt.Errorf("vmproto: header too large (%d bytes)", len(b))
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ReadHeader reads one header line and returns a reader positioned after it.
func ReadHeader(r io.Reader) (Header, *bufio.Reader, error) {
	br := bufio.NewReaderSize(r, 4096)
	var line []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return Header{}, br, err
		}
		line = append(line, chunk...)
		if len(line) >= maxHeader {
			return Header{}, br, errors.New("vmproto: header too large")
		}
		if !isPrefix {
			break
		}
	}
	var h Header
	if err := json.Unmarshal(line, &h); err != nil {
		return Header{}, br, fmt.Errorf("vmproto: bad header: %w", err)
	}
	return h, br, nil
}

func WriteTrailer(w io.Writer, t Trailer) error {
	b, _ := json.Marshal(t)
	_, err := io.WriteString(w, trailerMark+string(b)+"\n")
	return err
}

// CopyExecOutput copies exec output to out until the trailer and returns it.
// A stream that ends without a trailer means the guest dropped the command.
func CopyExecOutput(out io.Writer, r io.Reader) (Trailer, error) {
	br := bufio.NewReader(r)
	mark := []byte(trailerMark)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if i := indexOf(line, mark); i >= 0 {
				if _, werr := out.Write(line[:i]); werr != nil {
					return Trailer{}, werr
				}
				var t Trailer
				if jerr := json.Unmarshal(line[i+len(mark):], &t); jerr != nil {
					return Trailer{}, fmt.Errorf("vmproto: bad trailer: %w", jerr)
				}
				return t, nil
			}
			if _, werr := out.Write(line); werr != nil {
				return Trailer{}, werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return Trailer{}, errors.New("vmproto: exec stream ended without exit status")
			}
			return Trailer{}, err
		}
	}
}

func indexOf(b, sub []byte) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		match := true
		for j := range sub {
			if b[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
