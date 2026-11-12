package main

// newc cpio writer for the VM initramfs. Entries carry their own uid/gid/mode,
// so the guest root is root-owned no matter who builds the image.

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// imgEntry is one file in the guest root (a merged view of every layer).
type imgEntry struct {
	Path  string // absolute, cleaned ("/usr/bin/k3s")
	Mode  uint32 // permission bits | type (cpioDir, cpioReg, cpioSymlink, cpioChar, cpioBlock)
	UID   int
	GID   int
	Data  []byte
	Link  string // symlink target
	Major uint32 // char/block devices
	Minor uint32
	MTime int64
}

const (
	cpioDir     = 0o040000
	cpioReg     = 0o100000
	cpioSymlink = 0o120000
	cpioChar    = 0o020000
	cpioBlock   = 0o060000
)

// imgTree merges layers: later puts replace earlier ones (like image layers).
type imgTree struct {
	entries map[string]*imgEntry
}

func newImgTree() *imgTree { return &imgTree{entries: map[string]*imgEntry{}} }

func (t *imgTree) put(e imgEntry) {
	e.Path = path.Clean("/" + strings.TrimPrefix(e.Path, "./"))
	if e.Path == "/" {
		return
	}
	t.mkdirAll(path.Dir(e.Path))
	cp := e
	t.entries[e.Path] = &cp
}

func (t *imgTree) mkdirAll(dir string) {
	for dir != "/" && dir != "." {
		if _, ok := t.entries[dir]; ok {
			return
		}
		t.entries[dir] = &imgEntry{Path: dir, Mode: cpioDir | 0o755}
		dir = path.Dir(dir)
	}
}

func (t *imgTree) get(p string) *imgEntry { return t.entries[path.Clean(p)] }

// writeCpio writes the tree as an uncompressed newc archive, parents first.
func (t *imgTree) writeCpio(w io.Writer) (int64, error) {
	paths := make([]string, 0, len(t.entries))
	for p := range t.entries {
		paths = append(paths, p)
	}
	sort.Strings(paths) // "/a" < "/a/b": a directory always precedes its children
	bw := bufio.NewWriterSize(w, 1<<20)
	var off int64
	ino := uint32(1000)
	write := func(name string, e *imgEntry, data []byte) error {
		ino++
		nlink := 1
		if e.Mode&0o170000 == cpioDir {
			nlink = 2
		}
		name = strings.TrimPrefix(name, "/")
		hdr := fmt.Sprintf("070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
			ino, e.Mode, e.UID, e.GID, nlink, uint32(e.MTime), len(data), 0, 0, e.Major, e.Minor, len(name)+1, 0)
		n1, err := io.WriteString(bw, hdr+name+"\x00")
		if err != nil {
			return err
		}
		off += int64(n1)
		if err := pad4(bw, &off); err != nil {
			return err
		}
		n2, err := bw.Write(data)
		if err != nil {
			return err
		}
		off += int64(n2)
		return pad4(bw, &off)
	}
	for _, p := range paths {
		e := t.entries[p]
		data := e.Data
		if e.Mode&0o170000 == cpioSymlink {
			data = []byte(e.Link)
		} else if e.Mode&0o170000 != cpioReg {
			data = nil
		}
		if err := write(p, e, data); err != nil {
			return off, err
		}
	}
	if err := write("TRAILER!!!", &imgEntry{}, nil); err != nil {
		return off, err
	}
	return off, bw.Flush()
}

func pad4(w io.Writer, off *int64) error {
	if r := *off % 4; r != 0 {
		n, err := w.Write(make([]byte, 4-r))
		*off += int64(n)
		return err
	}
	return nil
}
