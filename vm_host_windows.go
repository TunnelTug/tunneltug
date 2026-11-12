//go:build windows

package main

// TunnelTug VM host (Windows). TunnelTug creates and owns the compute system:
// it is the parent of the VM, the way wslservice owns a WSLC session VM.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/Microsoft/go-winio/vhd"
	"golang.org/x/sys/windows"

	"tunneltug/vmproto"
)

// wslToolsDir is where an installed WSL keeps the kernel and modules.vhd that the
// tug VM boots by default (copied into the VM home so TunnelTug owns its copy).
func wslToolsDir() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "WSL", "tools")
}

func processElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func currentUserSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

func tugVMProbe(o tugVMOptions) error {
	fmt.Printf("elevated:        %v\n", processElevated())
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	fmt.Printf("user sid:        %s\n", sid)

	basic, err := hcsServiceProperties(`{"PropertyTypes":["Basic"]}`)
	if err != nil {
		fmt.Printf("hcs service:     unreachable: %v\n", err)
	} else {
		var resp struct {
			Properties []struct {
				SupportedSchemaVersions []hcsVersion `json:"SupportedSchemaVersions"`
			} `json:"Properties"`
		}
		_ = json.Unmarshal([]byte(basic), &resp)
		var vs []string
		for _, p := range resp.Properties {
			for _, v := range p.SupportedSchemaVersions {
				vs = append(vs, fmt.Sprintf("%d.%d", v.Major, v.Minor))
			}
		}
		fmt.Printf("hcs schemas:     %s\n", strings.Join(vs, " "))
	}

	systems, err := hcsEnumerate("")
	if err != nil {
		fmt.Printf("hcs enumerate:   %v\n", err)
	} else {
		var list []struct {
			ID    string `json:"Id"`
			Owner string `json:"Owner"`
			State string `json:"State"`
		}
		_ = json.Unmarshal([]byte(systems), &list)
		fmt.Printf("compute systems: %d visible\n", len(list))
		for _, s := range list {
			fmt.Printf("                 %s owner=%s state=%s\n", s.ID, s.Owner, s.State)
		}
	}

	tools := wslToolsDir()
	for _, f := range []string{"kernel", "modules.vhd"} {
		p := filepath.Join(tools, f)
		if st, err := os.Stat(p); err == nil {
			fmt.Printf("wsl %-12s %s (%d bytes)\n", f+":", p, st.Size())
		} else {
			fmt.Printf("wsl %-12s missing (%v)\n", f+":", err)
		}
	}
	fmt.Printf("vm home:         %s\n", o.Home)
	switch in, known := hyperVInToken(); {
	case !known:
		fmt.Printf("hyper-v admins:  could not look up the group\n")
	case in:
		fmt.Printf("hyper-v admins:  this logon token includes the group\n")
	default:
		fmt.Printf("hyper-v admins:  %s\n", hyperVTokenHint)
	}
	return nil
}

// hcsAccessHint turns HCS_E_ACCESS_DENIED into the one-time fix.
func hcsAccessHint(err error) error {
	var he *hcsError
	if errors.As(err, &he) && he.HR == 0x8037011B {
		note := "One-time setup from an elevated PowerShell:\n" +
			"  Add-LocalGroupMember -Group \"Hyper-V Administrators\" -Member \"$env:USERNAME\"\n" +
			"then sign out and back in (or run tunneltug vm up from an elevated terminal)."
		if in, known := hyperVInToken(); known && !in {
			note = hyperVTokenHint
		}
		return fmt.Errorf("%w\n\nTunnelTug creates VMs through HCS like WSL does. %s", err, note)
	}
	return err
}

// pinKernel copies WSL's kernel + matching modules.vhd into the VM home once, so
// a WSL update never changes a TunnelTug VM underneath it (refresh: -refresh-kernel).
func pinKernel(home string, refresh bool) error {
	tools := wslToolsDir()
	for _, f := range []string{"kernel", "modules.vhd"} {
		dst := filepath.Join(home, f)
		if _, err := os.Stat(dst); err == nil && !refresh {
			continue
		}
		if err := copyFileWin(filepath.Join(tools, f), dst); err != nil {
			return fmt.Errorf("pin %s from %s: %w", f, tools, err)
		}
	}
	return nil
}

func copyFileWin(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// ensureGuestImage rebuilds initrd.img when missing or built from another agent.
func ensureGuestImage(home string) error {
	agent, err := tugGuestInit()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(agent)
	if b, err := os.ReadFile(filepath.Join(home, "initrd.json")); err == nil {
		var m tugImageManifest
		if json.Unmarshal(b, &m) == nil && m.Agent == hex.EncodeToString(sum[:]) {
			if _, err := os.Stat(filepath.Join(home, "initrd.img")); err == nil {
				return nil
			}
		}
	}
	_, err = buildTugGuestImage(defaultTugImageSpec(), tugCacheDir(), home, func(f string, a ...any) {
		log.Printf("vm image: "+f, a...)
	})
	return err
}

func tugVMUp(o tugVMOptions) error {
	if err := os.MkdirAll(o.Home, 0o700); err != nil {
		return err
	}
	if st, err := readTugVMState(o.Home); err == nil && processAlive(st.PID) {
		return fmt.Errorf("vm %s is already up (pid %d, api %s)", o.Name, st.PID, st.API)
	}
	if _, err := hcsServiceProperties(`{"PropertyTypes":["Basic"]}`); err != nil {
		return hcsAccessHint(err)
	}
	if err := pinKernel(o.Home, o.RefreshKernel); err != nil {
		return err
	}
	if err := ensureGuestImage(o.Home); err != nil {
		return err
	}
	dataPath := filepath.Join(o.Home, "data.vhdx")
	if _, err := os.Stat(dataPath); os.IsNotExist(err) {
		log.Printf("vm %s: creating %d GB data disk %s", o.Name, o.DataGB, dataPath)
		if err := vhd.CreateVhdx(dataPath, o.DataGB, 1); err != nil {
			return fmt.Errorf("create data disk: %w", err)
		}
	}
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	vmGUID, err := guid.NewV4()
	if err != nil {
		return err
	}
	vmID := strings.ToUpper(vmGUID.String())

	consoleName := `\\.\pipe\tunneltug-vm-` + o.Name + `-` + vmID
	disks := []tugVMDisk{
		{Path: filepath.Join(o.Home, "modules.vhd"), ReadOnly: true, Role: "modules"},
		{Path: dataPath, Role: "data"},
	}
	if o.UbuntuVHDX != "" {
		if err := releaseWSLDisk(o.UbuntuVHDX); err != nil {
			return err
		}
		disks = append(disks, tugVMDisk{Path: o.UbuntuVHDX, ReadOnly: true, Role: "ubuntu"})
	}
	spec := tugVMSpec{
		Name:        o.Name,
		CPUs:        o.CPUs,
		MemoryMB:    o.MemoryMB,
		KernelPath:  filepath.Join(o.Home, "kernel"),
		InitrdPath:  filepath.Join(o.Home, "initrd.img"),
		Disks:       disks,
		Shares:      o.Shares,
		UserSID:     sid,
		ConsolePipe: consoleName,
		ExtraCmd:    "tug.name=" + o.Name,
	}
	doc, err := buildTugVMDocument(spec)
	if err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(o.Home, "hcs.json"), []byte(doc), 0o600)

	// The VM worker opens these files as the VM's own account.
	granted := []string{spec.KernelPath, spec.InitrdPath}
	for _, d := range spec.Disks {
		granted = append(granted, d.Path)
	}
	for _, p := range granted {
		if err := hcsGrantVMAccess(vmID, p); err != nil {
			return hcsAccessHint(err)
		}
	}
	defer func() {
		for _, p := range granted {
			_ = hcsRevokeVMAccess(vmID, p)
		}
	}()

	// The console pipe is named in the HCS document, so it exists before create.
	consoleL, err := winio.ListenPipe(consoleName, nil)
	if err != nil {
		return fmt.Errorf("console pipe: %w", err)
	}
	defer consoleL.Close()
	consoleLog, err := os.Create(filepath.Join(o.Home, "console.log"))
	if err != nil {
		return err
	}
	defer consoleLog.Close()
	go func() {
		c, err := consoleL.Accept()
		if err != nil {
			return
		}
		var w io.Writer = consoleLog
		if o.Console {
			w = io.MultiWriter(consoleLog, os.Stderr)
		}
		_, _ = io.Copy(w, c)
	}()

	log.Printf("vm %s: creating compute system %s (%d vCPU, %d MB)", o.Name, vmID, o.CPUs, o.MemoryMB)
	sys, err := hcsCreateComputeSystem(vmID, doc)
	if err != nil {
		return hcsAccessHint(err)
	}
	defer sys.close()

	// Same order as WSL's WslCoreVm: create → listen on the partition's RuntimeId → start.
	// The hvsocket VM address does not exist until the compute system does.
	runtimeID, err := sys.runtimeID()
	if err != nil {
		_ = sys.terminate()
		return err
	}
	ctlL, err := winio.ListenHvsock(&winio.HvsockAddr{VMID: runtimeID, ServiceID: winio.VsockServiceID(vmproto.ControlPort)})
	if err != nil {
		_ = sys.terminate()
		return fmt.Errorf("hvsock control listener: %w", err)
	}
	defer ctlL.Close()
	netL, err := winio.ListenHvsock(&winio.HvsockAddr{VMID: runtimeID, ServiceID: winio.VsockServiceID(vmproto.NetworkPort)})
	if err != nil {
		_ = sys.terminate()
		return fmt.Errorf("hvsock network listener: %w", err)
	}
	defer netL.Close()
	vnet, err := newTugVirtualNetwork(o)
	if err != nil {
		_ = sys.terminate()
		return err
	}
	go func() { _ = http.Serve(netL, vnet.Mux()) }()

	if err := sys.start(); err != nil {
		_ = sys.terminate()
		return err
	}
	bootStart := time.Now()
	log.Printf("vm %s: started; waiting for tug-init", o.Name)

	type accepted struct {
		c   net.Conn
		err error
	}
	acc := make(chan accepted, 1)
	go func() { c, err := ctlL.Accept(); acc <- accepted{c, err} }()
	var ctl net.Conn
	select {
	case a := <-acc:
		if a.err != nil {
			_ = sys.terminate()
			return fmt.Errorf("control accept: %w", a.err)
		}
		ctl = a.c
	case <-time.After(o.BootTimeout):
		_ = sys.terminate()
		return fmt.Errorf("guest did not connect within %s (see %s)", o.BootTimeout, filepath.Join(o.Home, "console.log"))
	}
	host, err := newTugVMHost(o, ctl)
	if err != nil {
		_ = sys.terminate()
		return err
	}
	log.Printf("vm %s: guest up in %s: kernel %s, agent %s, boot %d ms, disks %v, shares %v",
		o.Name, time.Since(bootStart).Round(time.Millisecond), host.hello.Kernel, host.hello.Agent,
		host.hello.BootMS, host.hello.Disks, host.hello.Shares)
	for _, w := range host.hello.Warnings {
		log.Printf("vm %s: guest warning: %s", o.Name, w)
	}

	state, err := host.serveLocal(o, vmID)
	if err != nil {
		_ = sys.terminate()
		return err
	}
	if err := writeTugVMState(o.Home, state); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(o.Home, "vm.json"))
	log.Printf("vm %s: api %s  kube-api %s  kubeconfig %s", o.Name, state.API, state.KubeAPI, state.Kubeconfig)

	if len(o.Job) > 0 {
		log.Printf("vm %s: %s", o.Name, strings.Join(o.Job, " "))
		code, err := host.exec(o.Job, os.Stdout)
		log.Printf("vm %s: job exited %d", o.Name, code)
		host.stopGuest(45 * time.Second)
		host.closeLocal()
		_ = sys.terminate()
		if err != nil {
			return err
		}
		if code != 0 {
			return &tugVMJobError{Code: code, Name: o.Name}
		}
		log.Printf("vm %s: down", o.Name)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	select {
	case <-ctx.Done():
		log.Printf("vm %s: stopping", o.Name)
		host.stopGuest(45 * time.Second)
	case <-host.stopped:
		log.Printf("vm %s: stop requested", o.Name)
		host.stopGuest(45 * time.Second)
	case <-host.session.CloseChan():
		log.Printf("vm %s: guest closed the control session", o.Name)
	}
	_ = sys.terminate() // no-op error once the guest powered off
	log.Printf("vm %s: down", o.Name)
	return nil
}

func tugVMDown(o tugVMOptions) error {
	st, err := readTugVMState(o.Home)
	if err != nil {
		return fmt.Errorf("vm %s is not up: %w", o.Name, err)
	}
	return tugVMAPICall(st, http.MethodPost, "/stop", nil, os.Stdout)
}

func tugVMStatus(o tugVMOptions) error {
	st, err := readTugVMState(o.Home)
	if err != nil {
		fmt.Printf("vm %s: down\n", o.Name)
		return nil
	}
	return tugVMAPICall(st, http.MethodGet, "/status", nil, os.Stdout)
}

func tugVMExec(o tugVMOptions, argv []string) (int, error) {
	st, err := readTugVMState(o.Home)
	if err != nil {
		return 1, fmt.Errorf("vm %s is not up: %w", o.Name, err)
	}
	return tugVMAPIExec(st, argv, os.Stdout)
}

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}
