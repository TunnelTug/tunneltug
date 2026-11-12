package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// dirHubStore is a self-hosted OCI blob store. Keys are content paths under
// root. Colon (digest algorithm separator) is encoded so the same layout
// works on Windows, where ':' is not a filename character.
type dirHubStore struct {
	mu   sync.Mutex
	root string
}

func newDirHubStore(root string) (*dirHubStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("hub dir is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	// A volume root would let a bad key walk the disk. Require a real directory.
	if filepath.Dir(abs) == abs {
		return nil, fmt.Errorf("hub dir %s is a filesystem root", abs)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, err
	}
	return &dirHubStore{root: abs}, nil
}

func (d *dirHubStore) Put(key string, data []byte, contentType string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	full, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".put-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	// Windows rename does not replace an existing file.
	_ = os.Remove(full)
	if err := os.Rename(tmpName, full); err != nil {
		os.Remove(tmpName)
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return os.WriteFile(full+".type", []byte(contentType), 0o640)
}

func (d *dirHubStore) Get(key string) ([]byte, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	full, err := d.path(key)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("not found")
		}
		return nil, "", err
	}
	ct, _ := os.ReadFile(full + ".type")
	return data, strings.TrimSpace(string(ct)), nil
}

func (d *dirHubStore) Head(key string) (int64, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	full, err := d.path(key)
	if err != nil {
		return 0, "", err
	}
	st, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "", fmt.Errorf("not found")
		}
		return 0, "", err
	}
	ct, _ := os.ReadFile(full + ".type")
	return st.Size(), strings.TrimSpace(string(ct)), nil
}

func (d *dirHubStore) ListPrefix(prefix string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	prefix = strings.TrimPrefix(strings.TrimSpace(prefix), "/")
	var out []string
	err := filepath.WalkDir(d.root, func(p string, ent fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ent.IsDir() {
			return nil
		}
		if strings.HasSuffix(ent.Name(), ".type") || strings.HasPrefix(ent.Name(), ".put-") {
			return nil
		}
		rel, err := filepath.Rel(d.root, p)
		if err != nil {
			return nil
		}
		key := decodeStoreKey(filepath.ToSlash(rel))
		if prefix == "" || strings.HasPrefix(key, prefix) {
			out = append(out, key)
			if len(out) >= 10000 {
				return errHubListCap
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errHubListCap) {
		return nil, err
	}
	return out, nil
}

func (d *dirHubStore) path(key string) (string, error) {
	key = strings.TrimSpace(key)
	key = strings.Trim(key, "/")
	if key == "" || strings.Contains(key, "\\") || strings.Contains(key, "~") || strings.Contains(key, "..") {
		return "", fmt.Errorf("invalid key")
	}
	cleaned := path.Clean("/" + key)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", fmt.Errorf("invalid key")
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid key")
		}
	}
	full := filepath.Join(d.root, filepath.FromSlash(encodeStoreKey(cleaned)))
	rel, err := filepath.Rel(d.root, full)
	if err != nil {
		return "", fmt.Errorf("invalid key")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid key")
	}
	return full, nil
}

func encodeStoreKey(key string) string {
	return strings.ReplaceAll(key, ":", "~")
}

func decodeStoreKey(key string) string {
	return strings.ReplaceAll(key, "~", ":")
}

// prefixHubStore isolates a registry inside a shared bucket or directory.
type prefixHubStore struct {
	inner  hubBlobStore
	prefix string
}

func wrapPrefix(inner hubBlobStore, prefix string) hubBlobStore {
	prefix = strings.Trim(prefix, "/")
	if inner == nil || prefix == "" {
		return inner
	}
	return &prefixHubStore{inner: inner, prefix: prefix + "/"}
}

func (p *prefixHubStore) Put(key string, data []byte, contentType string) error {
	return p.inner.Put(p.prefix+strings.TrimPrefix(key, "/"), data, contentType)
}

func (p *prefixHubStore) Get(key string) ([]byte, string, error) {
	return p.inner.Get(p.prefix + strings.TrimPrefix(key, "/"))
}

func (p *prefixHubStore) Head(key string) (int64, string, error) {
	return p.inner.Head(p.prefix + strings.TrimPrefix(key, "/"))
}

func (p *prefixHubStore) ListPrefix(prefix string) ([]string, error) {
	keys, err := p.inner.ListPrefix(p.prefix + strings.TrimPrefix(prefix, "/"))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if strings.HasPrefix(k, p.prefix) {
			out = append(out, strings.TrimPrefix(k, p.prefix))
		}
	}
	return out, nil
}
