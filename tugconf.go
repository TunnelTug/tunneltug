package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Tugconf is a Junos-like hierarchical configuration language for TunnelTug.
//
// Commands:
//
//	set <path...> <value>
//	delete <path...>
//	load override|merge|set <file>
//	show [path...]          (when building interactively)
//
// Paths use space-separated hierarchy. Keyed lists:
//
//	set pop sfo domain sfo.example.com
//	set stack barge williwaw replicas 2
//	set kernel-mesh mode full-mesh
//
// Compiles to the same SiteConfig IR as YAML.

// loadTugconfFile reads a .tug / .set file of set/delete/load lines.
func loadTugconfFile(path string) (*SiteConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseTugconf(string(raw), filepath.Dir(path))
}

// parseTugconf applies set/delete/load lines into a SiteConfig.
func parseTugconf(src, baseDir string) (*SiteConfig, error) {
	tree := map[string]any{}
	if err := applyTugconfSource(tree, src, baseDir); err != nil {
		return nil, err
	}
	return treeToSiteConfig(tree)
}

func applyTugconfSource(tree map[string]any, src, baseDir string) error {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// strip inline comments when not inside quotes
		if idx := indexUnquoted(line, '#'); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
			if line == "" {
				continue
			}
		}
		if err := applyTugconfLine(tree, line, baseDir); err != nil {
			return fmt.Errorf("tugconf line %d: %w", i+1, err)
		}
	}
	return nil
}

func applyTugconfLine(tree map[string]any, line, baseDir string) error {
	fields, err := splitTugFields(line)
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		return nil
	}
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "set":
		if len(fields) < 3 {
			return fmt.Errorf("set requires path and value: %s", line)
		}
		path := fields[1 : len(fields)-1]
		val := fields[len(fields)-1]
		return treeSet(tree, path, parseTugValue(val))
	case "delete":
		if len(fields) < 2 {
			return fmt.Errorf("delete requires a path")
		}
		return treeDelete(tree, fields[1:])
	case "load":
		if len(fields) < 3 {
			return fmt.Errorf("load requires mode and file: load override|merge|set <file>")
		}
		mode := strings.ToLower(fields[1])
		file := fields[2]
		if !filepath.IsAbs(file) {
			file = filepath.Join(baseDir, file)
		}
		return treeLoad(tree, mode, file, baseDir)
	case "show", "commit", "edit", "top", "up", "rollback":
		// No-ops in batch load (interactive conf mode later).
		return nil
	default:
		return fmt.Errorf("unknown command %q (want set|delete|load)", fields[0])
	}
}

func treeLoad(tree map[string]any, mode, file, baseDir string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	switch mode {
	case "set":
		return applyTugconfSource(tree, string(raw), filepath.Dir(file))
	case "override", "merge":
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			// maybe tugconf
			if looksLikeTugconf(string(raw)) {
				return applyTugconfSource(tree, string(raw), filepath.Dir(file))
			}
			return err
		}
		if mode == "override" {
			// deep merge still for nested maps; top-level keys from file win
			deepMerge(tree, doc)
		} else {
			deepMerge(tree, doc)
		}
		return nil
	default:
		return fmt.Errorf("load mode %q: use override, merge, or set", mode)
	}
}

func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if vm, ok := v.(map[string]any); ok {
			if existing, ok := dst[k].(map[string]any); ok {
				deepMerge(existing, vm)
				continue
			}
			// copy map
			cp := map[string]any{}
			deepMerge(cp, vm)
			dst[k] = cp
			continue
		}
		dst[k] = v
	}
}

// splitTugFields splits on spaces respecting double quotes.
func splitTugFields(line string) ([]string, error) {
	var out []string
	var b strings.Builder
	inQ := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQ = !inQ
		case (c == ' ' || c == '\t') && !inQ:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteByte(c)
		}
	}
	if inQ {
		return nil, fmt.Errorf("unterminated quote")
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out, nil
}

func indexUnquoted(s string, ch byte) int {
	inQ := false
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			inQ = !inQ
			continue
		}
		if s[i] == ch && !inQ {
			return i
		}
	}
	return -1
}

func parseTugValue(s string) any {
	s = strings.TrimSpace(s)
	// bracket list: [a,b,c] or [a b c]
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return []any{}
		}
		// split on comma or space
		var parts []string
		if strings.Contains(inner, ",") {
			for _, p := range strings.Split(inner, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					parts = append(parts, p)
				}
			}
		} else {
			parts = strings.Fields(inner)
		}
		out := make([]any, len(parts))
		for i, p := range parts {
			out[i] = p
		}
		return out
	}
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && strings.Contains(s, ".") {
		return f
	}
	return s
}

// treeSet sets path in a nested map. Special keys: pop <id>, stack barge <name>, kernel-mesh.
func treeSet(tree map[string]any, path []string, val any) error {
	if len(path) == 0 {
		return fmt.Errorf("empty path")
	}
	// Normalize aliases.
	path = normalizeTugPath(path)

	// Keyed collections: pop <id> ..., stack barge <name> ...
	if len(path) >= 2 && path[0] == "pop" {
		return setKeyedList(tree, "pops", "id", path[1], path[2:], val)
	}
	if len(path) >= 3 && path[0] == "stack" && (path[1] == "barge" || path[1] == "product") {
		return setKeyedList(tree, "stack.barges", "name", path[2], path[3:], val)
	}
	if path[0] == "kernel-mesh" || path[0] == "kernel_mesh" {
		path[0] = "kernel_mesh"
	}

	return setNested(tree, path, val)
}

func normalizeTugPath(path []string) []string {
	out := make([]string, len(path))
	for i, p := range path {
		p = strings.TrimSpace(p)
		// common juniper-style hyphens → snake for yaml keys where needed
		switch p {
		case "kernel-mesh":
			p = "kernel_mesh"
		case "public-scheme":
			p = "public_scheme"
		case "token-env":
			p = "token_env"
		case "node-id":
			p = "node_id"
		case "data-dir":
			p = "data_dir"
		case "fleet-id":
			p = "fleet_id"
		case "hub-pop":
			p = "hub_pop"
		case "api-version":
			p = "apiVersion"
		case "ultimate-db":
			p = "ultimate_db"
		case "ultimate-keystore":
			p = "ultimate_keystore"
		case "stack-overrides":
			p = "stack_overrides"
		}
		out[i] = p
	}
	return out
}

func setKeyedList(tree map[string]any, listPath, keyField, key string, rest []string, val any) error {
	// Ensure list exists
	parts := strings.Split(listPath, ".")
	parent := tree
	for i := 0; i < len(parts)-1; i++ {
		m, ok := parent[parts[i]].(map[string]any)
		if !ok {
			m = map[string]any{}
			parent[parts[i]] = m
		}
		parent = m
	}
	listKey := parts[len(parts)-1]
	var list []any
	if existing, ok := parent[listKey].([]any); ok {
		list = existing
	}

	// Find or create entry with keyField == key
	var entry map[string]any
	idx := -1
	for i, item := range list {
		if m, ok := item.(map[string]any); ok {
			if strings.EqualFold(fmt.Sprint(m[keyField]), key) {
				entry = m
				idx = i
				break
			}
		}
	}
	if entry == nil {
		entry = map[string]any{keyField: key}
		list = append(list, entry)
		idx = len(list) - 1
	}

	if len(rest) == 0 {
		// set pop sfo <value> invalid — need a leaf
		return fmt.Errorf("path under %s %s needs a field", listPath, key)
	}

	// Special: roles list append when rest is ["roles"] and val is scalar
	if len(rest) == 1 && rest[0] == "roles" {
		if arr, ok := val.([]any); ok {
			entry["roles"] = arr
		} else {
			// append single role
			var roles []any
			if existing, ok := entry["roles"].([]any); ok {
				roles = existing
			}
			s := fmt.Sprint(val)
			found := false
			for _, r := range roles {
				if fmt.Sprint(r) == s {
					found = true
					break
				}
			}
			if !found {
				roles = append(roles, s)
			}
			entry["roles"] = roles
		}
	} else {
		if err := setNested(entry, rest, val); err != nil {
			return err
		}
	}

	list[idx] = entry
	parent[listKey] = list
	return nil
}

func setNested(tree map[string]any, path []string, val any) error {
	if len(path) == 0 {
		return fmt.Errorf("empty path")
	}
	cur := tree
	for i := 0; i < len(path)-1; i++ {
		k := path[i]
		next, ok := cur[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[k] = next
		}
		cur = next
	}
	leaf := path[len(path)-1]
	// Append semantics for known list fields when value is scalar and key exists as list.
	if leaf == "roles" {
		if arr, ok := val.([]any); ok {
			cur[leaf] = arr
			return nil
		}
		var roles []any
		if existing, ok := cur[leaf].([]any); ok {
			roles = existing
		}
		s := fmt.Sprint(val)
		for _, r := range roles {
			if fmt.Sprint(r) == s {
				return nil
			}
		}
		cur[leaf] = append(roles, s)
		return nil
	}
	cur[leaf] = val
	return nil
}

func treeDelete(tree map[string]any, path []string) error {
	path = normalizeTugPath(path)
	if len(path) == 0 {
		return fmt.Errorf("empty delete path")
	}
	if len(path) >= 2 && path[0] == "pop" {
		return deleteKeyed(tree, "pops", "id", path[1], path[2:])
	}
	cur := tree
	for i := 0; i < len(path)-1; i++ {
		next, ok := cur[path[i]].(map[string]any)
		if !ok {
			return nil // nothing to delete
		}
		cur = next
	}
	delete(cur, path[len(path)-1])
	return nil
}

func deleteKeyed(tree map[string]any, listKey, keyField, key string, rest []string) error {
	raw, ok := tree[listKey]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	if len(rest) == 0 {
		// delete whole pop
		var out []any
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				out = append(out, item)
				continue
			}
			if !strings.EqualFold(fmt.Sprint(m[keyField]), key) {
				out = append(out, item)
			}
		}
		tree[listKey] = out
		return nil
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(fmt.Sprint(m[keyField]), key) {
			return treeDelete(m, rest)
		}
	}
	return nil
}

// treeToSiteConfig marshals the generic tree through YAML into SiteConfig.
func treeToSiteConfig(tree map[string]any) (*SiteConfig, error) {
	// Ensure apiVersion/kind defaults.
	if _, ok := tree["apiVersion"]; !ok {
		tree["apiVersion"] = siteAPIVersion
	}
	if _, ok := tree["kind"]; !ok {
		tree["kind"] = siteKind
	}
	raw, err := yaml.Marshal(tree)
	if err != nil {
		return nil, err
	}
	var cfg SiteConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("tugconf → site: %w", err)
	}
	if err := normalizeSiteConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SiteConfigToSetLines emits Junos-like set lines for a site config (display set).
func SiteConfigToSetLines(cfg *SiteConfig) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("nil config")
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	var tree map[string]any
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return "", err
	}
	var lines []string
	emitSetLines("", tree, &lines)
	// Re-write pops as "set pop <id> ..."
	// emitSetLines already flattens; improve pops presentation:
	lines = rewritePopSetLines(cfg, lines)
	return strings.Join(lines, "\n") + "\n", nil
}

func emitSetLines(prefix string, v any, lines *[]string) {
	switch t := v.(type) {
	case map[string]any:
		// stable-ish: range is random — collect keys
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		// simple insertion order not available; sort-ish by name
		for i := 0; i < len(keys); i++ {
			for j := i + 1; j < len(keys); j++ {
				if keys[j] < keys[i] {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		}
		for _, k := range keys {
			p := k
			if prefix != "" {
				p = prefix + " " + k
			}
			emitSetLines(p, t[k], lines)
		}
	case []any:
		// Detect list of maps with id/name → emit keyed form partially
		allMaps := true
		for _, item := range t {
			if _, ok := item.(map[string]any); !ok {
				allMaps = false
				break
			}
		}
		if allMaps {
			for _, item := range t {
				m := item.(map[string]any)
				key := ""
				keyField := ""
				if id, ok := m["id"]; ok {
					key = fmt.Sprint(id)
					keyField = "id"
				} else if name, ok := m["name"]; ok {
					key = fmt.Sprint(name)
					keyField = "name"
				}
				if key != "" {
					// emit children under pop/barge style
					head := prefix
					if strings.HasSuffix(prefix, "pops") || prefix == "pops" {
						head = "pop " + key
					} else if strings.Contains(prefix, "barges") || strings.HasSuffix(prefix, "products") {
						head = "stack barge " + key
					} else {
						head = prefix + " " + key
					}
					for mk, mv := range m {
						if mk == keyField {
							continue
						}
						emitSetLines(head+" "+mk, mv, lines)
					}
					continue
				}
				emitSetLines(prefix, item, lines)
			}
			return
		}
		// scalar list
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = fmt.Sprint(item)
		}
		*lines = append(*lines, fmt.Sprintf("set %s [%s]", prefix, strings.Join(parts, ",")))
	case nil:
		return
	default:
		s := fmt.Sprint(t)
		if strings.TrimSpace(s) == "" {
			return // skip empty leaves (avoids invalid "set path" with no value)
		}
		if strings.ContainsAny(s, " \t#\"") {
			s = strconv.Quote(s)
		}
		*lines = append(*lines, fmt.Sprintf("set %s %s", prefix, s))
	}
}

func rewritePopSetLines(cfg *SiteConfig, lines []string) []string {
	// emitSetLines already handles pops specially; keep as-is.
	_ = cfg
	return lines
}
