package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// A container is one tool the iEDA playbook can ask the VM to run.
// The playbook names the container and fills {tokens} from the job payload.
// Job names are not part of this file.
type Container struct {
	Kind string            `json:"kind"` // host, guest, vm
	Dir  string            `json:"dir,omitempty"`
	Argv []string          `json:"argv"`
	Stop map[string]string `json:"stop,omitempty"` // empty field -> this sentence, before the tool starts
	Out  string            `json:"out,omitempty"`  // expanded path recorded as html
}

type Catalog map[string]Container

var tokenRE = regexp.MustCompile(`\{([A-Za-z0-9_.]+)\}`)

func defaultCatalogPath() string {
	if v := os.Getenv("IEDA_CONTAINERS"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "WASMApollo", "eda", "containers.json")
}

func loadCatalog(path string) (Catalog, error) {
	if path == "" {
		path = defaultCatalogPath()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cat Catalog
	if err := json.Unmarshal(b, &cat); err != nil {
		return nil, fmt.Errorf("containers %s: %w", path, err)
	}
	return cat, nil
}

// containerFields copies string payload fields and fills tool paths the
// catalog refers to. A missing playbook field stays empty so Stop can see it.
func containerFields(payload map[string]any) map[string]string {
	f := map[string]string{}
	for k, v := range payload {
		if s, ok := v.(string); ok {
			f[k] = s
		}
	}
	home, _ := os.UserHomeDir()
	apollo := filepath.Join(home, "WASMApollo")
	set := func(k, v string) {
		if f[k] == "" {
			f[k] = v
		}
	}
	set("apollo", apollo)
	set("viaduct", filepath.Join(home, "OpenViaduct"))
	set("out", filepath.Join(apollo, "eda", "wgsl-out"))
	set("dispatch", "1")
	set("bin.dawn", filepath.Join(apollo, "dawn", "dawn.exe"))
	set("bin.apollo", filepath.Join(apollo, "eda", "apollo-eda.exe"))
	set("bin.route", filepath.Join(home, "OpenViaduct", "route", "route.exe"))
	set("bin.chipview", filepath.Join(apollo, "eda", "chipview", "dist", "chipview-windows-amd64.exe"))
	set("logic", "/mnt/c/Users/grego/OpenViaduct/route/test/logic/run.sh")
	// IEDA_RUN is the pipedash run directory for this tag. A hand invoke
	// with no run falls back to the convert output directory.
	if f["run"] == "" {
		if v := os.Getenv("IEDA_RUN"); v != "" {
			f["run"] = v
		} else {
			f["run"] = f["out"]
		}
	}
	if f["vm"] == "" {
		f["vm"] = "eda"
	}
	return f
}

func extraArgs(payload map[string]any) []string {
	raw, ok := payload["args"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// expandString substitutes {tokens}. An empty token is an error.
func expandString(s string, fields map[string]string) (string, error) {
	var missing []string
	out := tokenRE.ReplaceAllStringFunc(s, func(m string) string {
		k := m[1 : len(m)-1]
		v := fields[k]
		if v == "" {
			missing = append(missing, k)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// expandContainer substitutes {tokens}. An empty token is an error unless
// the container's Stop map already explained it.
func expandContainer(c Container, fields map[string]string, extra []string) (dir string, argv []string, err error) {
	for k, sentence := range c.Stop {
		if fields[k] == "" {
			return "", nil, fmt.Errorf("%s", sentence)
		}
	}
	if c.Dir != "" {
		dir, err = expandString(c.Dir, fields)
		if err != nil {
			return "", nil, err
		}
	}
	for _, a := range c.Argv {
		s, err := expandString(a, fields)
		if err != nil {
			return "", nil, err
		}
		argv = append(argv, s)
	}
	argv = append(argv, extra...)
	if len(argv) == 0 {
		return "", nil, fmt.Errorf("container has no argv")
	}
	return dir, argv, nil
}

// vmctlCommand builds the go++ argv that asks WASMNix.
// IEDA_VMCTL is wasitime (or a go++ exe that already speaks status|path|exec).
// IEDA_VMCTL_WASM is vmctl.wasm. wasitime forwards the tail as the guest argv,
// so the op is the next argument after the wasm path.
// An empty runner skips the call. The job still runs through tugVMExec.
func vmctlCommand(bin, wasm, op string, argv []string) (string, []string, bool) {
	if bin == "" {
		return "", nil, false
	}
	args := []string{op}
	if wasm != "" {
		args = []string{"run", wasm, op}
	}
	if op == "exec" {
		args = append(args, argv...)
	}
	return bin, args, true
}

func executeContainer(ctx context.Context, catalogPath string, payload map[string]any) (map[string]any, error) {
	name, _ := payload["container"].(string)
	if name == "" {
		return nil, fmt.Errorf("payload has no container")
	}
	cat, err := loadCatalog(catalogPath)
	if err != nil {
		return nil, err
	}
	c, ok := cat[name]
	if !ok {
		return nil, fmt.Errorf("unknown container %q", name)
	}
	fields := containerFields(payload)
	dir, argv, err := expandContainer(c, fields, extraArgs(payload))
	if err != nil {
		return map[string]any{"pass": false, "container": name, "error": err.Error()}, err
	}
	res := map[string]any{"container": name, "kind": c.Kind, "argv": argv}
	if c.Out != "" {
		html, err := expandString(c.Out, fields)
		if err != nil {
			return map[string]any{"pass": false, "container": name, "error": err.Error()}, err
		}
		if err := os.MkdirAll(filepath.Dir(html), 0o755); err != nil {
			return map[string]any{"pass": false, "container": name, "error": err.Error()}, err
		}
		res["html"] = html
	}
	var out []byte
	var code int
	switch c.Kind {
	case "host", "":
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		if dir != "" {
			cmd.Dir = dir
		}
		out, err = cmd.CombinedOutput()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = 127
			}
		}
	case "guest":
		// go++ vmctl asks WASMNix for the WSL VM (status, path, exec).
		// The job itself still runs in the eda VM.
		var note []byte
		if bin, args, ok := vmctlCommand(os.Getenv("IEDA_VMCTL"), os.Getenv("IEDA_VMCTL_WASM"), "status", nil); ok {
			cmd := exec.CommandContext(ctx, bin, args...)
			note, _ = cmd.CombinedOutput()
		}
		o := tugVMOptions{Name: fields["vm"]}
		if o.Home == "" {
			o.Home = defaultTugVMHome(o.Name)
		}
		code, err = tugVMExec(o, argv)
		if err != nil {
			out = []byte(err.Error())
		}
		if len(note) > 0 {
			out = append(append(bytes.TrimSpace(note), '\n'), out...)
		}
	case "vm":
		code = runVMEda(argv)
		if code != 0 {
			err = fmt.Errorf("vm eda exited %d", code)
		}
	default:
		return nil, fmt.Errorf("container %s: unknown kind %q", name, c.Kind)
	}
	text := strings.TrimSpace(string(out))
	if len(text) > 32<<10 {
		text = text[:32<<10] + "…"
	}
	res["output"] = text
	res["code"] = code
	res["pass"] = code == 0 && err == nil
	if err != nil && res["pass"] == false {
		res["error"] = err.Error()
	}
	return res, err
}

func runVMContainer(args []string) int {
	fs := flag.NewFlagSet("vm container", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	payloadPath := fs.String("payload", "", "job payload JSON (default stdin)")
	catalogPath := fs.String("catalog", "", "containers.json (default IEDA_CONTAINERS or WASMApollo/eda/containers.json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var raw []byte
	var err error
	if *payloadPath != "" {
		raw, err = os.ReadFile(*payloadPath)
	} else {
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "vm container: %v\n", err)
		return 2
	}
	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &payload); err != nil {
		fmt.Fprintf(os.Stderr, "vm container: payload: %v\n", err)
		return 2
	}
	res, err := executeContainer(context.Background(), *catalogPath, payload)
	if res == nil {
		res = map[string]any{"pass": false, "error": err.Error()}
	}
	enc, _ := json.Marshal(res)
	fmt.Println(string(enc))
	if err != nil {
		return 1
	}
	if pass, _ := res["pass"].(bool); !pass {
		return 1
	}
	return 0
}
