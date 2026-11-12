package main

// Docker-like runtime verbs. TunnelTug owns a Linux VM (HCS on Windows, forked
// from WSL's WSLC session VM) and schedules containers onto the k8s node inside it.
//
//	tunneltug vm probe            HCS reachability, elevation, kernel/modules sources
//	tunneltug vm image            build the guest initramfs (Alpine + k3s + tug-init), verified
//	tunneltug vm up   [-name eda]  build the guest image if needed, boot, serve control
//	tunneltug vm down [-name eda]
//	tunneltug vm status
//	tunneltug vm eda  <pdk> [design...]   one VM per OpenViaduct import job

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

func isRuntimeVerb(v string) bool {
	switch v {
	case "vm":
		return true
	}
	return false
}

func runRuntimeCLI(verb string, args []string) int {
	switch verb {
	case "vm":
		return runVMCLI(args)
	}
	fmt.Fprintf(os.Stderr, "tunneltug: unknown verb %q\n", verb)
	return 2
}

// tugVMOptions are the vm verb flags shared by up/down/status.
type tugVMOptions struct {
	Name     string
	Home     string // state root (%LOCALAPPDATA%\TunnelTug\vm\<name>)
	CPUs     int
	MemoryMB uint64
	Shares   tugShareList
	Console  bool

	DataGB        uint32        // data.vhdx size (dynamic, grows on use)
	KubePort      int           // host 127.0.0.1 port forwarded to the guest's k3s API
	BootTimeout   time.Duration // kernel start → tug-init hello
	RefreshKernel bool          // re-copy kernel + modules.vhd from the installed WSL
	UbuntuVHDX    string        // WSL ext4.vhdx, attached read-only (role ubuntu)
	Job           []string      // run in the guest after boot, then shut the VM down
}

type tugShareList []tugVMShare

func (l *tugShareList) String() string { return fmt.Sprint(*l) }

// Set parses name=C:\host\path:/guest/mount[:ro]
func (l *tugShareList) Set(v string) error {
	sh, err := parseTugShare(v)
	if err != nil {
		return err
	}
	*l = append(*l, sh)
	return nil
}

func parseTugShare(v string) (tugVMShare, error) {
	eq := -1
	for i, r := range v {
		if r == '=' {
			eq = i
			break
		}
	}
	if eq <= 0 {
		return tugVMShare{}, fmt.Errorf("share %q: want name=HOSTPATH:/guest/mount[:ro]", v)
	}
	name, rest := v[:eq], v[eq+1:]
	ro := false
	if len(rest) > 3 && rest[len(rest)-3:] == ":ro" {
		ro, rest = true, rest[:len(rest)-3]
	}
	// The guest mount is the last ":/"-prefixed part (host paths carry a drive colon).
	cut := -1
	for i := len(rest) - 2; i >= 0; i-- {
		if rest[i] == ':' && rest[i+1] == '/' {
			cut = i
			break
		}
	}
	if cut <= 0 {
		return tugVMShare{}, fmt.Errorf("share %q: missing :/guest/mount", v)
	}
	return tugVMShare{Name: name, HostPath: rest[:cut], Mount: rest[cut+1:], ReadOnly: ro}, nil
}

func defaultTugVMHome(name string) string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		if h, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(h, ".tunneltug")
		}
	}
	return filepath.Join(base, "TunnelTug", "vm", name)
}

func tugCacheDir() string {
	return filepath.Join(filepath.Dir(filepath.Dir(defaultTugVMHome("x"))), "cache")
}

func runVMCLI(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tunneltug vm probe|image|up|down|status|exec|eda|container [flags]")
		return 2
	}
	sub := args[0]
	if sub == "eda" {
		return runVMEda(args[1:])
	}
	if sub == "container" {
		return runVMContainer(args[1:])
	}
	fs := flag.NewFlagSet("vm "+sub, flag.ContinueOnError)
	var o tugVMOptions
	var sitePath, sitePop string
	fs.StringVar(&sitePath, "config", "", "site YAML; the sandbox for -pop is this VM")
	fs.StringVar(&sitePop, "pop", "", "site pop id")
	fs.StringVar(&o.Name, "name", "eda", "VM name")
	fs.StringVar(&o.Home, "home", "", "VM state directory (default %LOCALAPPDATA%\\TunnelTug\\vm\\<name>)")
	fs.IntVar(&o.CPUs, "cpus", runtime.NumCPU(), "virtual processors")
	fs.Uint64Var(&o.MemoryMB, "memory", 12288, "memory in MB (OpenROAD place/route needs ~8 GB)")
	fs.Var(&o.Shares, "share", "host directory: name=C:\\path:/guest/mount[:ro] (repeatable)")
	fs.BoolVar(&o.Console, "console", true, "stream the guest console (hvc0) to stderr")
	var dataGB uint
	fs.UintVar(&dataGB, "data-gb", 256, "data disk size in GB (dynamic VHDX: k3s, images, runs)")
	fs.IntVar(&o.KubePort, "kube-port", 16443, "host port for the guest k3s API (127.0.0.1 only)")
	fs.DurationVar(&o.BootTimeout, "boot-timeout", 2*time.Minute, "time for tug-init to connect after start")
	fs.BoolVar(&o.RefreshKernel, "refresh-kernel", false, "re-pin kernel + modules.vhd from the installed WSL")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	o.DataGB = uint32(dataGB)
	if err := applySiteSandbox(&o, fs, sitePath, sitePop); err != nil {
		fmt.Fprintf(os.Stderr, "tunneltug vm %s: %v\n", sub, err)
		return 1
	}
	if o.Home == "" {
		o.Home = defaultTugVMHome(o.Name)
	}
	var err error
	switch sub {
	case "probe":
		err = tugVMProbe(o)
	case "image":
		_, err = buildTugGuestImage(defaultTugImageSpec(), tugCacheDir(), o.Home, func(f string, a ...any) {
			fmt.Printf("vm image: "+f+"\n", a...)
		})
	case "up":
		err = tugVMUp(o)
	case "down":
		err = tugVMDown(o)
	case "status":
		err = tugVMStatus(o)
	case "exec": // tunneltug vm exec [-name eda] -- argv…  (guest root namespace, for bring-up)
		code, xerr := tugVMExec(o, fs.Args())
		if xerr != nil {
			fmt.Fprintf(os.Stderr, "tunneltug vm exec: %v\n", xerr)
		}
		return code
	default:
		fmt.Fprintf(os.Stderr, "tunneltug vm: unknown command %q\n", sub)
		return 2
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "tunneltug vm %s: %v\n", sub, err)
		return 1
	}
	return 0
}
