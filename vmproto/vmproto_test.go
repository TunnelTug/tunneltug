package vmproto

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHeader(&buf, Header{Op: OpDial, Addr: "127.0.0.1:6443"}); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("payload")
	h, rest, err := ReadHeader(&buf)
	if err != nil || h.Op != OpDial || h.Addr != "127.0.0.1:6443" {
		t.Fatalf("got %+v %v", h, err)
	}
	b, _ := io.ReadAll(rest)
	if string(b) != "payload" {
		t.Fatalf("payload after header lost: %q", b)
	}
}

func TestExecTrailer(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("line one\npartial")
	if err := WriteTrailer(&buf, Trailer{Exit: 3}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	tr, err := CopyExecOutput(&out, &buf)
	if err != nil || tr.Exit != 3 {
		t.Fatalf("trailer %+v %v", tr, err)
	}
	if out.String() != "line one\npartial" {
		t.Fatalf("output %q", out.String())
	}
	if _, err := CopyExecOutput(io.Discard, strings.NewReader("no trailer\n")); err == nil {
		t.Fatal("missing trailer must be an error")
	}
}
