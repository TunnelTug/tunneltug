//go:build linux

package main

// Guest half of the VM network: a TAP device whose frames travel over vsock to
// the host's gvisor-tap-vsock virtual network (userspace NAT, DHCP, DNS). This
// is the protocol of gvisor-tap-vsock's cmd/vm forwarder (Apache-2.0,
// github.com/containers/gvisor-tap-vsock): POST /connect, then each Ethernet
// frame prefixed by its length as a little-endian uint16 ("hyperkit" framing).

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
	"unsafe"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"

	"tunneltug/vmproto"
)

const tapMAC = vmproto.GuestMAC

// runNet is "/init net": supervised by PID 1, reconnects forever.
func runNet(args []string) {
	fs := flag.NewFlagSet("net", flag.ExitOnError)
	iface := fs.String("iface", "eth0", "tap interface")
	mtu := fs.Int("mtu", 1500, "mtu")
	port := fs.Uint("port", vmproto.NetworkPort, "host vsock port")
	addr := fs.String("addr", vmproto.GuestCIDR, "guest address (the host's virtual network leases it to tapMAC)")
	gw := fs.String("gw", vmproto.GatewayIP, "gateway and DNS server")
	_ = fs.Parse(args)
	for {
		if err := netSession(*iface, *mtu, uint32(*port), *addr, *gw); err != nil {
			fmt.Fprintf(os.Stderr, "net: %v\n", err)
		}
		time.Sleep(time.Second)
	}
}

func netSession(iface string, mtu int, port uint32, addr, gw string) error {
	conn, err := vsock.Dial(vmproto.HostCID, port, nil)
	if err != nil {
		return fmt.Errorf("dial host vsock %d: %w", port, err)
	}
	defer conn.Close()
	req, _ := http.NewRequest(http.MethodPost, "/connect", nil)
	req.Host = "tunneltug"
	if err := req.Write(conn); err != nil {
		return err
	}

	tap, err := openTap(iface)
	if err != nil {
		return err
	}
	defer tap.Close()
	for _, argv := range [][]string{
		{"/sbin/ip", "link", "set", iface, "address", tapMAC},
		{"/sbin/ip", "link", "set", iface, "mtu", fmt.Sprint(mtu)},
		{"/sbin/ip", "link", "set", iface, "up"},
		{"/sbin/ip", "addr", "replace", addr, "dev", iface},
		{"/sbin/ip", "route", "replace", "default", "via", gw, "dev", iface},
	} {
		if out, err := runOut(argv); err != nil {
			return fmt.Errorf("%v: %v %s", argv, err, out)
		}
	}

	// The gateway answers DNS (forwarding to the Windows resolver).
	if err := os.WriteFile("/etc/resolv.conf", []byte("nameserver "+gw+"\n"), 0o644); err != nil {
		return err
	}

	errc := make(chan error, 2)
	go func() { errc <- tapToHost(tap, conn, mtu) }()
	go func() { errc <- hostToTap(conn, tap, mtu) }()
	return <-errc
}

func tapToHost(tap *os.File, conn net.Conn, mtu int) error {
	buf := make([]byte, 2+mtu+14)
	for {
		n, err := tap.Read(buf[2:])
		if err != nil {
			return fmt.Errorf("tap read: %w", err)
		}
		binary.LittleEndian.PutUint16(buf[:2], uint16(n))
		if _, err := conn.Write(buf[:2+n]); err != nil {
			return fmt.Errorf("host write: %w", err)
		}
	}
}

func hostToTap(conn net.Conn, tap *os.File, mtu int) error {
	r := bufio.NewReaderSize(conn, 64<<10)
	size := make([]byte, 2)
	buf := make([]byte, 64<<10)
	for {
		if _, err := io.ReadFull(r, size); err != nil {
			return fmt.Errorf("host read: %w", err)
		}
		n := int(binary.LittleEndian.Uint16(size))
		if n == 0 || n > len(buf) {
			return fmt.Errorf("bad frame length %d", n)
		}
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return fmt.Errorf("host read: %w", err)
		}
		if _, err := tap.Write(buf[:n]); err != nil {
			return fmt.Errorf("tap write: %w", err)
		}
	}
}

// openTap creates a TAP interface (IFF_TAP|IFF_NO_PI) on /dev/net/tun.
func openTap(name string) (*os.File, error) {
	if _, err := os.Stat("/dev/net/tun"); os.IsNotExist(err) {
		// tun built in but no node yet (devtmpfs creates it only when the driver registers).
		_ = os.MkdirAll("/dev/net", 0o755)
		_ = unix.Mknod("/dev/net/tun", unix.S_IFCHR|0o600, int(unix.Mkdev(10, 200)))
	}
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("/dev/net/tun: %w", err)
	}
	var ifr [unix.IFNAMSIZ + 64]byte
	if len(name) >= unix.IFNAMSIZ {
		unix.Close(fd)
		return nil, errors.New("tap name too long")
	}
	copy(ifr[:], name)
	*(*uint16)(unsafe.Pointer(&ifr[unix.IFNAMSIZ])) = unix.IFF_TAP | unix.IFF_NO_PI
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TUNSETIFF), uintptr(unsafe.Pointer(&ifr[0]))); errno != 0 {
		unix.Close(fd)
		return nil, fmt.Errorf("TUNSETIFF %s: %w", name, errno)
	}
	return os.NewFile(uintptr(fd), "/dev/net/tun"), nil
}

// runOut runs a helper outside PID 1 (the net subcommand is its own process,
// so exec.Cmd is safe here; only PID 1 must leave waiting to the reaper).
func runOut(argv []string) (string, error) {
	return execOutput(argv)
}
