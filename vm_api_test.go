package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"tunneltug/vmproto"
)

// fakeGuest speaks the tug-init side of the control protocol.
func fakeGuest(t *testing.T, conn net.Conn, echo net.Listener) {
	t.Helper()
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	sess, err := yamux.Client(conn, cfg)
	if err != nil {
		t.Error(err)
		return
	}
	st, _ := sess.Open()
	_ = vmproto.WriteHeader(st, vmproto.Header{Op: vmproto.OpHello, Hello: &vmproto.Hello{Name: "eda", Kernel: "6.18.33.1-microsoft-standard-WSL2", Agent: "fake"}})
	st.Close()
	for {
		s, err := sess.Accept()
		if err != nil {
			return
		}
		go func(s net.Conn) {
			defer s.Close()
			h, br, err := vmproto.ReadHeader(s)
			if err != nil {
				return
			}
			switch h.Op {
			case vmproto.OpExec:
				io.WriteString(s, "ran "+strings.Join(h.Argv, " ")+"\n")
				_ = vmproto.WriteTrailer(s, vmproto.Trailer{Exit: 7})
			case vmproto.OpStatus:
				b, _ := json.Marshal(vmproto.Status{UptimeS: 42, IP: vmproto.GuestIP, Services: map[string]string{"k3s": "running"}})
				s.Write(append(b, '\n'))
			case vmproto.OpDial:
				c, err := net.Dial("tcp", echo.Addr().String())
				if err != nil {
					return
				}
				defer c.Close()
				go io.Copy(c, br)
				io.Copy(s, c)
			}
		}(s)
	}
}

func TestTugVMHostControlSession(t *testing.T) {
	// A TCP echo server standing in for the guest's k3s API.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()

	hostEnd, guestEnd := net.Pipe()
	go fakeGuest(t, guestEnd, echo)
	h, err := newTugVMHost(tugVMOptions{Name: "eda"}, hostEnd)
	if err != nil {
		t.Fatal(err)
	}
	defer h.session.Close()
	if h.hello.Kernel == "" || h.hello.Name != "eda" {
		t.Fatalf("hello: %+v", h.hello)
	}

	var out bytes.Buffer
	code, err := h.exec([]string{"uname", "-r"}, &out)
	if err != nil || code != 7 || out.String() != "ran uname -r\n" {
		t.Fatalf("exec: code=%d err=%v out=%q", code, err, out.String())
	}

	st, err := h.status()
	if err != nil || st.UptimeS != 42 || st.Services["k3s"] != "running" {
		t.Fatalf("status: %+v %v", st, err)
	}

	fwd, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()
	go h.forward(fwd, "ignored-by-fake:6443")
	c, err := net.Dial("tcp", fwd.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(c, "ping")
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("forward echo: %q %v", buf, err)
	}
}

func TestCpioTreeLayersAndOrder(t *testing.T) {
	tree := newImgTree()
	tree.put(imgEntry{Path: "usr/local/bin/k3s", Mode: cpioReg | 0o755, Data: []byte("v1")})
	tree.put(imgEntry{Path: "/usr/local/bin/k3s", Mode: cpioReg | 0o755, Data: []byte("v2")}) // later layer wins
	tree.put(imgEntry{Path: "/sbin/ip", Mode: cpioSymlink | 0o777, Link: "/bin/busybox"})
	var buf bytes.Buffer
	if _, err := tree.writeCpio(&buf); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.HasPrefix(s, "070701") || !strings.Contains(s, "TRAILER!!!") {
		t.Fatal("not a newc archive")
	}
	if strings.Contains(s, "v1") || !strings.Contains(s, "v2") {
		t.Fatal("later layer did not replace earlier one")
	}
	if strings.Index(s, "usr/local\x00") > strings.Index(s, "usr/local/bin/k3s\x00") {
		t.Fatal("parent directory written after child")
	}
	if buf.Len()%4 != 0 {
		t.Fatal("archive not 4-byte aligned")
	}
}

func gz(b []byte) []byte {
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	z.Write(b)
	z.Close()
	return out.Bytes()
}

func TestAPKVerification(t *testing.T) {
	// Signed index: member 1 = signature over member 2's compressed bytes.
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	indexMember := gz([]byte("APKINDEX body"))
	sum := sha1.Sum(indexMember)
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, sum[:])
	sigTar := tarOf(t, map[string][]byte{".SIGN.RSA.test.rsa.pub": sig})
	if err := verifyAPKSignature(sigTar, indexMember, map[string]*rsa.PublicKey{"test.rsa.pub": &key.PublicKey}); err != nil {
		t.Fatalf("good signature rejected: %v", err)
	}
	if err := verifyAPKSignature(sigTar, gz([]byte("tampered")), map[string]*rsa.PublicKey{"test.rsa.pub": &key.PublicKey}); err == nil {
		t.Fatal("tampered index accepted")
	}
	if err := verifyAPKSignature(sigTar, indexMember, map[string]*rsa.PublicKey{}); err == nil {
		t.Fatal("unknown key accepted")
	}

	raw := append(append(gz([]byte("a")), gz([]byte("bb"))...), gz([]byte("ccc"))...)
	comp, plain, err := gzipMembers(raw)
	if err != nil || len(comp) != 3 || string(plain[2]) != "ccc" || !bytes.Equal(bytes.Join(comp, nil), raw) {
		t.Fatalf("gzip member split wrong: %d members, %v", len(comp), err)
	}

	idx := parseAPKIndexText("P:e2fsprogs\nV:1\nD:so:libuuid.so.1 so:libc.musl-x86_64.so.1\n\nP:libuuid\nV:2\np:so:libuuid.so.1=1\n")
	pkgs, err := idx.resolve([]string{"e2fsprogs"}, map[string]bool{"so:libc.musl-x86_64.so.1": true})
	if err != nil || len(pkgs) != 2 || pkgs[0].Name != "libuuid" {
		t.Fatalf("resolve: %v %v", pkgs, err)
	}
	if _, err := idx.resolve([]string{"nope"}, nil); err == nil {
		t.Fatal("missing package resolved")
	}
}

func tarOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, b := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		tw.Write(b)
	}
	tw.Close()
	return buf.Bytes()
}
