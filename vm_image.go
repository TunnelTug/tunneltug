package main

// Guest image for the TunnelTug VM: one initramfs holding Alpine (node OS),
// k3s, and tug-init as /init. Every download is verified before use:
//
//	Alpine minirootfs  sha256 from the branch's latest-releases.yaml (HTTPS)
//	APKINDEX           RSA signature by a key shipped in that minirootfs
//	each .apk          control segment = APKINDEX C: (Q1 sha1), data = .PKGINFO datahash (sha256)
//	k3s                sha256 from the release's sha256sum-amd64.txt
//
// The container images that do the work (OpenViaduct EDA, products) are not in
// here: k3s pulls them. The node stays small and immutable.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed vmguest
var vmGuestFS embed.FS

// tugImageSpec pins what goes into the guest image.
type tugImageSpec struct {
	AlpineMirror string   `json:"alpine_mirror"`
	AlpineBranch string   `json:"alpine_branch"`
	Arch         string   `json:"arch"`
	Packages     []string `json:"packages"` // added to the minirootfs (with dependencies)
	K3sVersion   string   `json:"k3s_version"`
}

func defaultTugImageSpec() tugImageSpec {
	return tugImageSpec{
		AlpineMirror: "https://dl-cdn.alpinelinux.org/alpine",
		AlpineBranch: "v3.22",
		Arch:         "x86_64",
		// e2fsprogs formats the data disk on first boot; kmod is modprobe (Alpine's
		// busybox has none). busybox (ip, mount) and ca-certificates come with the minirootfs.
		Packages:   []string{"e2fsprogs", "kmod"},
		K3sVersion: "v1.36.5+k3s1", // k3s stable channel, 2026-10
	}
}

// tugImageManifest records exactly what an image was built from (written next to it).
type tugImageManifest struct {
	Spec       tugImageSpec      `json:"spec"`
	Built      time.Time         `json:"built"`
	Agent      string            `json:"agent_sha256"`
	Components map[string]string `json:"components"` // name → version + digest
	Bytes      int64             `json:"bytes"`
}

// tugGuestInit returns the embedded linux tug-init (built into vmguest/ by `make vm-guest`).
func tugGuestInit() ([]byte, error) {
	b, err := vmGuestFS.ReadFile("vmguest/tug-init")
	if err != nil || len(b) == 0 {
		return nil, errors.New("this tunneltug was built without the VM guest agent; run `make vm-guest build`")
	}
	return b, nil
}

// buildTugGuestImage writes initrd.img (+ initrd.json) into dir.
func buildTugGuestImage(spec tugImageSpec, cacheDir, dir string, logf func(string, ...any)) (*tugImageManifest, error) {
	agent, err := tugGuestInit()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	man := &tugImageManifest{Spec: spec, Built: time.Now().UTC(), Components: map[string]string{}}
	agentSum := sha256.Sum256(agent)
	man.Agent = hex.EncodeToString(agentSum[:])
	tree := newImgTree()

	// 1. Alpine minirootfs, pinned by the release index's sha256.
	relBase := fmt.Sprintf("%s/%s/releases/%s/", spec.AlpineMirror, spec.AlpineBranch, spec.Arch)
	relYAML, err := httpGetBytes(relBase + "latest-releases.yaml")
	if err != nil {
		return nil, err
	}
	var releases []struct {
		Flavor  string `yaml:"flavor"`
		File    string `yaml:"file"`
		Version string `yaml:"version"`
		Sha256  string `yaml:"sha256"`
	}
	if err := yaml.Unmarshal(relYAML, &releases); err != nil {
		return nil, fmt.Errorf("alpine latest-releases.yaml: %w", err)
	}
	var rootFile, rootSum string
	for _, r := range releases {
		if r.Flavor == "alpine-minirootfs" {
			rootFile, rootSum = r.File, r.Sha256
		}
	}
	if rootFile == "" || len(rootSum) != 64 {
		return nil, errors.New("alpine latest-releases.yaml has no minirootfs entry")
	}
	logf("alpine: %s", rootFile)
	rootTGZ, err := fetchVerified(relBase+rootFile, rootSum, filepath.Join(cacheDir, rootFile))
	if err != nil {
		return nil, err
	}
	man.Components["alpine-minirootfs"] = rootFile + " sha256:" + rootSum
	if err := untarInto(tree, bytes.NewReader(rootTGZ), true, nil); err != nil {
		return nil, fmt.Errorf("minirootfs: %w", err)
	}

	// 2. Packages: resolve against the signed APKINDEX, verify each .apk.
	if len(spec.Packages) > 0 {
		keys := map[string]*rsa.PublicKey{}
		for p, e := range tree.entries {
			if strings.HasPrefix(p, "/etc/apk/keys/") && e.Mode&0o170000 == cpioReg {
				if k, err := parseRSAPublicKey(e.Data); err == nil {
					keys[path.Base(p)] = k
				}
			}
		}
		installed := apkInstalled(tree)
		repo := fmt.Sprintf("%s/%s/main/%s/", spec.AlpineMirror, spec.AlpineBranch, spec.Arch)
		idxRaw, err := httpGetBytes(repo + "APKINDEX.tar.gz")
		if err != nil {
			return nil, err
		}
		idx, err := parseSignedAPKIndex(idxRaw, keys)
		if err != nil {
			return nil, fmt.Errorf("APKINDEX %s: %w", repo, err)
		}
		pkgs, err := idx.resolve(spec.Packages, installed)
		if err != nil {
			return nil, err
		}
		for _, p := range pkgs {
			file := p.Name + "-" + p.Version + ".apk"
			raw, err := fetchCached(repo+file, filepath.Join(cacheDir, "apk", file))
			if err != nil {
				return nil, err
			}
			if err := extractVerifiedAPK(tree, raw, p.Checksum); err != nil {
				_ = os.Remove(filepath.Join(cacheDir, "apk", file))
				return nil, fmt.Errorf("%s: %w", file, err)
			}
			man.Components["apk:"+p.Name] = p.Version
			logf("apk: %s-%s", p.Name, p.Version)
		}
	}

	// 3. k3s, pinned by the release's checksum file.
	k3sBase := "https://github.com/k3s-io/k3s/releases/download/" + url.PathEscape(spec.K3sVersion) + "/"
	sums, err := httpGetBytes(k3sBase + "sha256sum-amd64.txt")
	if err != nil {
		return nil, err
	}
	k3sSum := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "k3s" {
			k3sSum = f[0]
		}
	}
	if len(k3sSum) != 64 {
		return nil, fmt.Errorf("k3s %s: no checksum for the k3s binary", spec.K3sVersion)
	}
	logf("k3s: %s", spec.K3sVersion)
	k3s, err := fetchVerified(k3sBase+"k3s", k3sSum, filepath.Join(cacheDir, "k3s-"+strings.ReplaceAll(spec.K3sVersion, "+", "-")))
	if err != nil {
		return nil, err
	}
	man.Components["k3s"] = spec.K3sVersion + " sha256:" + k3sSum
	tree.put(imgEntry{Path: "/usr/local/bin/k3s", Mode: cpioReg | 0o755, Data: k3s})

	// 4. TunnelTug's own layer.
	tree.put(imgEntry{Path: "/init", Mode: cpioReg | 0o755, Data: agent})
	tree.put(imgEntry{Path: "/dev/console", Mode: cpioChar | 0o600, Major: 5, Minor: 1})
	tree.put(imgEntry{Path: "/dev/null", Mode: cpioChar | 0o666, Major: 1, Minor: 3})
	for _, d := range []string{"/var/lib/tug", "/lib/modules", "/mnt", "/run", "/sys", "/proc", "/tmp", "/root"} {
		tree.put(imgEntry{Path: d, Mode: cpioDir | 0o755})
	}
	tree.put(imgEntry{Path: "/tmp", Mode: cpioDir | 0o1777})
	tree.put(imgEntry{Path: "/etc/tunneltug-release", Mode: cpioReg | 0o644,
		Data: []byte(fmt.Sprintf("tunneltug %s\nk3s %s\nalpine %s\n", Version, spec.K3sVersion, rootFile))})

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp := filepath.Join(dir, "initrd.img.tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	n, err := tree.writeCpio(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	man.Bytes = n
	if err := os.Rename(tmp, filepath.Join(dir, "initrd.img")); err != nil {
		return nil, err
	}
	mj, _ := json.MarshalIndent(man, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "initrd.json"), mj, 0o644); err != nil {
		return nil, err
	}
	logf("image: %s (%d MB, %d entries)", filepath.Join(dir, "initrd.img"), n>>20, len(tree.entries))
	return man, nil
}

// untarInto adds a (gzip) tar stream to the tree. skip filters top-level names.
func untarInto(tree *imgTree, r io.Reader, gz bool, skip func(string) bool) error {
	if gz {
		z, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		defer z.Close()
		r = z
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if skip != nil && skip(h.Name) {
			continue
		}
		e := imgEntry{Path: h.Name, UID: h.Uid, GID: h.Gid, MTime: h.ModTime.Unix()}
		perm := uint32(h.Mode) & 0o7777
		switch h.Typeflag {
		case tar.TypeDir:
			e.Mode = cpioDir | perm
		case tar.TypeReg:
			e.Mode = cpioReg | perm
			if e.Data, err = io.ReadAll(tr); err != nil {
				return err
			}
		case tar.TypeSymlink:
			e.Mode, e.Link = cpioSymlink|0o777, h.Linkname
		case tar.TypeLink: // hardlink: the initramfs gets its own copy
			src := tree.get("/" + strings.TrimPrefix(h.Linkname, "./"))
			if src == nil {
				return fmt.Errorf("hardlink %s → missing %s", h.Name, h.Linkname)
			}
			e.Mode, e.Data = src.Mode, src.Data
		case tar.TypeChar:
			e.Mode, e.Major, e.Minor = cpioChar|perm, uint32(h.Devmajor), uint32(h.Devminor)
		case tar.TypeBlock:
			e.Mode, e.Major, e.Minor = cpioBlock|perm, uint32(h.Devmajor), uint32(h.Devminor)
		default:
			continue
		}
		tree.put(e)
	}
}

// ---- downloads ----

var tugHTTP = &http.Client{Timeout: 10 * time.Minute}

func httpGetBytes(u string) ([]byte, error) {
	resp, err := tugHTTP.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// fetchCached returns the cached copy or downloads it (callers verify contents).
func fetchCached(u, cachePath string) ([]byte, error) {
	if b, err := os.ReadFile(cachePath); err == nil {
		return b, nil
	}
	b, err := httpGetBytes(u)
	if err != nil {
		return nil, err
	}
	_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
	_ = os.WriteFile(cachePath, b, 0o644)
	return b, nil
}

// fetchVerified downloads (or reuses) a file whose sha256 must equal want.
func fetchVerified(u, want, cachePath string) ([]byte, error) {
	want = strings.ToLower(want)
	if b, err := os.ReadFile(cachePath); err == nil {
		if s := sha256.Sum256(b); hex.EncodeToString(s[:]) == want {
			return b, nil
		}
	}
	b, err := httpGetBytes(u)
	if err != nil {
		return nil, err
	}
	if s := sha256.Sum256(b); hex.EncodeToString(s[:]) != want {
		return nil, fmt.Errorf("%s: sha256 %x, want %s", u, s, want)
	}
	_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
	if err := os.WriteFile(cachePath, b, 0o644); err != nil {
		return nil, err
	}
	return b, nil
}

// ---- apk ----

type apkPkg struct {
	Name, Version, Checksum string
	Depends                 []string
	Provides                []string
}

type apkIndex struct {
	byName    map[string]*apkPkg
	providers map[string]*apkPkg // "so:libuuid.so.1", "cmd:mkfs.ext4", plain names
}

func parseRSAPublicKey(pemBytes []byte) (*rsa.PublicKey, error) {
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return nil, errors.New("no PEM block")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	return rk, nil
}

// apkInstalled lists package names and provides already in the minirootfs.
func apkInstalled(tree *imgTree) map[string]bool {
	out := map[string]bool{}
	e := tree.get("/lib/apk/db/installed")
	if e == nil {
		return out
	}
	for _, line := range strings.Split(string(e.Data), "\n") {
		switch {
		case strings.HasPrefix(line, "P:"):
			out[line[2:]] = true
		case strings.HasPrefix(line, "p:"):
			for _, p := range strings.Fields(line[2:]) {
				out[apkDepName(p)] = true
			}
		}
	}
	return out
}

// apkDepName strips version constraints: "so:libc.musl-x86_64.so.1", "foo>=1.2" → "foo".
func apkDepName(d string) string {
	if i := strings.IndexAny(d, "<>=~"); i >= 0 {
		return d[:i]
	}
	return d
}

// gzipMembers splits concatenated gzip members, returning each member's raw
// (compressed) bytes and its decompressed content.
func gzipMembers(raw []byte) (compressed, plain [][]byte, err error) {
	br := bufio.NewReader(bytes.NewReader(raw))
	cr := &countingByteReader{r: br}
	var z *gzip.Reader
	for {
		start := cr.n
		if z == nil {
			z, err = gzip.NewReader(cr)
		} else {
			err = z.Reset(cr)
		}
		if err == io.EOF {
			return compressed, plain, nil
		}
		if err != nil {
			return nil, nil, err
		}
		z.Multistream(false)
		body, err := io.ReadAll(z)
		if err != nil {
			return nil, nil, err
		}
		compressed = append(compressed, raw[start:cr.n])
		plain = append(plain, body)
	}
}

// countingByteReader implements io.ByteReader so flate reads exactly one member.
type countingByteReader struct {
	r *bufio.Reader
	n int64
}

func (c *countingByteReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingByteReader) ReadByte() (byte, error) {
	b, err := c.r.ReadByte()
	if err == nil {
		c.n++
	}
	return b, err
}

// parseSignedAPKIndex verifies APKINDEX.tar.gz (member 1: .SIGN.RSA*.<key>.pub
// over member 2's compressed bytes) and parses it.
func parseSignedAPKIndex(raw []byte, keys map[string]*rsa.PublicKey) (*apkIndex, error) {
	comp, plain, err := gzipMembers(raw)
	if err != nil {
		return nil, err
	}
	if len(comp) < 2 {
		return nil, errors.New("not a signed index (one gzip member)")
	}
	if err := verifyAPKSignature(plain[0], comp[1], keys); err != nil {
		return nil, err
	}
	tr := tar.NewReader(bytes.NewReader(plain[1]))
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("no APKINDEX entry: %w", err)
		}
		if h.Name == "APKINDEX" {
			body, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			return parseAPKIndexText(string(body)), nil
		}
	}
}

func verifyAPKSignature(sigTar, signed []byte, keys map[string]*rsa.PublicKey) error {
	tr := tar.NewReader(bytes.NewReader(sigTar))
	for {
		h, err := tr.Next()
		if err != nil {
			return errors.New("no signature from a trusted key")
		}
		var hashFn crypto.Hash
		var keyName string
		switch {
		case strings.HasPrefix(h.Name, ".SIGN.RSA256."):
			hashFn, keyName = crypto.SHA256, strings.TrimPrefix(h.Name, ".SIGN.RSA256.")
		case strings.HasPrefix(h.Name, ".SIGN.RSA."):
			hashFn, keyName = crypto.SHA1, strings.TrimPrefix(h.Name, ".SIGN.RSA.")
		default:
			continue
		}
		key := keys[keyName]
		if key == nil {
			continue
		}
		sig, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		var digest []byte
		if hashFn == crypto.SHA256 {
			s := sha256.Sum256(signed)
			digest = s[:]
		} else {
			s := sha1.Sum(signed)
			digest = s[:]
		}
		if err := rsa.VerifyPKCS1v15(key, hashFn, digest, sig); err != nil {
			return fmt.Errorf("signature by %s does not verify: %w", keyName, err)
		}
		return nil
	}
}

func parseAPKIndexText(s string) *apkIndex {
	idx := &apkIndex{byName: map[string]*apkPkg{}, providers: map[string]*apkPkg{}}
	for _, rec := range strings.Split(s, "\n\n") {
		p := &apkPkg{}
		for _, line := range strings.Split(rec, "\n") {
			if len(line) < 2 || line[1] != ':' {
				continue
			}
			v := line[2:]
			switch line[0] {
			case 'P':
				p.Name = v
			case 'V':
				p.Version = v
			case 'C':
				p.Checksum = v
			case 'D':
				p.Depends = strings.Fields(v)
			case 'p':
				p.Provides = strings.Fields(v)
			}
		}
		if p.Name == "" {
			continue
		}
		idx.byName[p.Name] = p
		if _, ok := idx.providers[p.Name]; !ok {
			idx.providers[p.Name] = p
		}
		for _, pr := range p.Provides {
			if _, ok := idx.providers[apkDepName(pr)]; !ok {
				idx.providers[apkDepName(pr)] = p
			}
		}
	}
	return idx
}

// resolve returns want plus dependencies not already installed, deps first.
func (idx *apkIndex) resolve(want []string, installed map[string]bool) ([]*apkPkg, error) {
	var order []*apkPkg
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(name string) error
	visit = func(dep string) error {
		if strings.HasPrefix(dep, "!") {
			return nil // conflict marker
		}
		name := apkDepName(dep)
		if installed[name] {
			return nil
		}
		p := idx.providers[name]
		if p == nil {
			return fmt.Errorf("apk: nothing provides %q", name)
		}
		if installed[p.Name] || state[p.Name] != 0 {
			return nil
		}
		state[p.Name] = 1
		for _, d := range p.Depends {
			if err := visit(d); err != nil {
				return fmt.Errorf("%s: %w", p.Name, err)
			}
		}
		state[p.Name] = 2
		order = append(order, p)
		return nil
	}
	names := append([]string(nil), want...)
	sort.Strings(names)
	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// extractVerifiedAPK checks an .apk against its index checksum (Q1 = sha1 of the
// control member) and the control's datahash (sha256 of the data member), then
// adds the data files to the tree.
func extractVerifiedAPK(tree *imgTree, raw []byte, indexChecksum string) error {
	comp, plain, err := gzipMembers(raw)
	if err != nil {
		return err
	}
	if len(comp) != 3 {
		return fmt.Errorf("expected 3 gzip members (signature, control, data), got %d", len(comp))
	}
	if !strings.HasPrefix(indexChecksum, "Q1") {
		return fmt.Errorf("unsupported index checksum %q", indexChecksum)
	}
	want, err := base64.StdEncoding.DecodeString(indexChecksum[2:])
	if err != nil {
		return err
	}
	if got := sha1.Sum(comp[1]); !bytes.Equal(got[:], want) {
		return errors.New("control segment does not match APKINDEX checksum")
	}
	dataHash := ""
	tr := tar.NewReader(bytes.NewReader(plain[1]))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Name == ".PKGINFO" {
			body, _ := io.ReadAll(tr)
			for _, line := range strings.Split(string(body), "\n") {
				if k, v, ok := strings.Cut(line, " = "); ok && k == "datahash" {
					dataHash = strings.TrimSpace(v)
				}
			}
		}
	}
	if len(dataHash) != 64 {
		return errors.New(".PKGINFO has no datahash")
	}
	var h hash.Hash = sha256.New()
	h.Write(comp[2])
	if hex.EncodeToString(h.Sum(nil)) != dataHash {
		return errors.New("data segment does not match .PKGINFO datahash")
	}
	return untarInto(tree, bytes.NewReader(plain[2]), false, func(name string) bool {
		return strings.HasPrefix(name, ".")
	})
}
