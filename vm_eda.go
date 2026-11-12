package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// tugVMJobError is a guest job that ran and exited non-zero. Boot failures are
// plain errors so a series of designs stops instead of repeating a dead host.
type tugVMJobError struct {
	Code int
	Name string
}

func (e *tugVMJobError) Error() string {
	return fmt.Sprintf("vm %s: job exited %d", e.Name, e.Code)
}

// edaShare is one host tree the import scripts already address as a drvfs path.
type edaShare struct {
	Name     string
	HostPath string
	ReadOnly bool
}

func wslGuestMount(hostPath string) (string, error) {
	hostPath = filepath.Clean(hostPath)
	vol := filepath.VolumeName(hostPath)
	if len(vol) < 2 || vol[1] != ':' {
		return "", fmt.Errorf("eda share %s: need a drive path", hostPath)
	}
	rest := strings.TrimPrefix(hostPath, vol)
	rest = strings.ReplaceAll(rest, `\`, "/")
	return "/mnt/" + strings.ToLower(vol[:1]) + rest, nil
}

func edaShares(apollo, viaduct string) ([]tugVMShare, error) {
	specs := []edaShare{
		{Name: "wasmapollo", HostPath: apollo},
		{Name: "openviaduct", HostPath: viaduct, ReadOnly: true},
	}
	var out []tugVMShare
	for _, s := range specs {
		mnt, err := wslGuestMount(s.HostPath)
		if err != nil {
			return nil, err
		}
		out = append(out, tugVMShare{Name: s.Name, HostPath: s.HostPath, Mount: mnt, ReadOnly: s.ReadOnly})
	}
	return out, nil
}

func edaVMName(design string) (string, error) {
	if design == "" || strings.ContainsAny(design, `/\ :`) {
		return "", fmt.Errorf("bad design name %q", design)
	}
	return "eda-" + design, nil
}

// edaJobArgv runs the existing import.sh for one design. The guest chroots into
// the Ubuntu overlay and execs this argv. bash is named so a Plan9 mount that
// drops the executable bit still runs the script.
func edaJobArgv(script, pdk, design, through, staGate string, prerunSize bool) []string {
	argv := []string{"/init", "eda", "--", "/bin/bash", script, pdk, design, "--through", through}
	if prerunSize {
		argv = append(argv, "--prerun-size")
	}
	if staGate != "" {
		argv = append(argv, "--sta-gate", staGate)
	}
	return argv
}

var pyQuoted = regexp.MustCompile(`"([^"]+)"`)

// parsePyBlocks reads BLOCKS from import_pdk.py so the VM list cannot drift
// from the script the guest runs.
func parsePyBlocks(src string) ([]string, error) {
	i := strings.Index(src, "BLOCKS = (")
	if i < 0 {
		return nil, errors.New("import_pdk.py has no BLOCKS tuple")
	}
	rest := src[i+len("BLOCKS = ("):]
	j := strings.Index(rest, ")")
	if j < 0 {
		return nil, errors.New("import_pdk.py BLOCKS tuple is not closed")
	}
	var names []string
	for _, m := range pyQuoted.FindAllStringSubmatch(rest[:j], -1) {
		names = append(names, m[1])
	}
	if len(names) == 0 {
		return nil, errors.New("import_pdk.py BLOCKS is empty")
	}
	return names, nil
}

func loadEDABlocks(apollo string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(apollo, "pnr", "flow", "import_pdk.py"))
	if err != nil {
		return nil, err
	}
	return parsePyBlocks(string(b))
}

func defaultEDATrees() (apollo, viaduct string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	apollo = filepath.Join(home, "WASMApollo")
	viaduct = filepath.Join(home, "OpenViaduct")
	if _, err := os.Stat(filepath.Join(apollo, "pnr", "flow", "import.sh")); err != nil {
		return "", "", fmt.Errorf("WASMApollo import.sh: %w", err)
	}
	if _, err := os.Stat(filepath.Join(viaduct, "route", "prerun")); err != nil {
		return "", "", fmt.Errorf("OpenViaduct prerun: %w", err)
	}
	return apollo, viaduct, nil
}

// hostExecAllowed is the set of Windows programs a guest may ask the host to
// run. Dawn lives on the WASMApollo share. Anything else stays on the host.
func hostExecAllowed(path string, shares []tugVMShare) bool {
	path = filepath.Clean(path)
	for _, sh := range shares {
		root := filepath.Clean(sh.HostPath)
		if path == root {
			return true
		}
		prefix := root + string(os.PathSeparator)
		if len(path) > len(prefix) && strings.EqualFold(path[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

func runVMEda(args []string) int {
	fs := flag.NewFlagSet("vm eda", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: tunneltug vm eda [flags] <pdk> [design...]")
		fmt.Fprintln(os.Stderr, "  one TunnelTug VM per design. With no designs, every BLOCKS entry in import_pdk.py.")
		fs.PrintDefaults()
	}
	name := fs.String("name", "", "VM name for a single design (default eda-<design>)")
	home := fs.String("home", "", "VM state directory (default %LOCALAPPDATA%\\TunnelTug\\vm\\<name>)")
	cpus := fs.Int("cpus", runtime.NumCPU(), "virtual processors")
	memory := fs.Uint64("memory", 12288, "memory in MB (OpenROAD place/route needs ~8 GB)")
	ubuntu := fs.String("ubuntu", "", "WSL ext4.vhdx (default: the default distro's disk)")
	through := fs.String("through", "grt", "import_pdk --through: grt or drt")
	prerunSize := fs.Bool("prerun-size", false, "pass --prerun-size to import.sh")
	staGate := fs.String("sta-gate", "", "pass --sta-gate NS to import.sh")
	dataGB := fs.Uint("data-gb", 256, "data disk size in GB")
	boot := fs.Duration("boot-timeout", 2*time.Minute, "time for tug-init to connect after start")
	console := fs.Bool("console", true, "stream the guest console (hvc0) to stderr")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fs.Usage()
		return 2
	}
	if *through != "grt" && *through != "drt" {
		fmt.Fprintln(os.Stderr, "vm eda: --through is grt or drt")
		return 2
	}
	pdk := rest[0]
	designs := rest[1:]
	apollo, viaduct, err := defaultEDATrees()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vm eda: %v\n", err)
		return 1
	}
	if len(designs) == 0 {
		designs, err = loadEDABlocks(apollo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vm eda: %v\n", err)
			return 1
		}
	}
	if *name != "" && len(designs) != 1 {
		fmt.Fprintln(os.Stderr, "vm eda: -name applies to one design")
		return 2
	}
	shares, err := edaShares(apollo, viaduct)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vm eda: %v\n", err)
		return 1
	}
	disk := *ubuntu
	if disk == "" {
		disk, err = discoverUbuntuVHDX()
		if err != nil {
			fmt.Fprintf(os.Stderr, "vm eda: %v\n", err)
			return 1
		}
	}
	script := shares[0].Mount + "/pnr/flow/import.sh"
	var failed []string
	for _, design := range designs {
		vmName := *name
		if vmName == "" {
			vmName, err = edaVMName(design)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vm eda: %v\n", err)
				return 2
			}
		}
		vmHome := *home
		if vmHome == "" {
			vmHome = defaultTugVMHome(vmName)
		}
		fmt.Printf("vm eda: %s %s as %s (ubuntu %s)\n", pdk, design, vmName, disk)
		err := tugVMUp(tugVMOptions{
			Name:        vmName,
			Home:        vmHome,
			CPUs:        *cpus,
			MemoryMB:    *memory,
			Shares:      shares,
			Console:     *console,
			DataGB:      uint32(*dataGB),
			KubePort:    16443,
			BootTimeout: *boot,
			UbuntuVHDX:  disk,
			Job:         edaJobArgv(script, pdk, design, *through, *staGate, *prerunSize),
		})
		if err == nil {
			continue
		}
		var je *tugVMJobError
		if errors.As(err, &je) {
			fmt.Fprintf(os.Stderr, "vm eda: %s\n", err)
			failed = append(failed, design)
			continue
		}
		fmt.Fprintf(os.Stderr, "vm eda: %s: %v\n", design, err)
		return 1
	}
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "vm eda: failed: %s\n", strings.Join(failed, " "))
		return 1
	}
	return 0
}
