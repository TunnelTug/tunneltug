//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// discoverUbuntuVHDX is the default WSL distro's ext4 disk. The EDA toolchain
// (Yosys, OpenROAD, ~/eda, the PDKs) lives there. The VM mounts it read-only.
func discoverUbuntuVHDX() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Lxss`, registry.QUERY_VALUE)
	if err != nil {
		return "", fmt.Errorf("WSL registry: %w", err)
	}
	defer k.Close()
	id, _, err := k.GetStringValue("DefaultDistribution")
	if err != nil {
		return "", fmt.Errorf("no default WSL distro: %w", err)
	}
	sub, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Lxss\`+id, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer sub.Close()
	base, _, err := sub.GetStringValue("BasePath")
	if err != nil {
		return "", err
	}
	name, _, _ := sub.GetStringValue("DistributionName")
	base = strings.TrimPrefix(base, `\\?\`)
	p := filepath.Join(base, "ext4.vhdx")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("WSL %s disk: %w", name, err)
	}
	return p, nil
}

// releaseWSLDisk asks WSL to let go of the distro disk so HCS can attach it.
// The guest mounts the disk read-only; Ubuntu comes back when this VM is down.
func releaseWSLDisk(vhdx string) error {
	if _, err := os.Stat(vhdx); err != nil {
		return fmt.Errorf("ubuntu disk %s: %w", vhdx, err)
	}
	fmt.Printf("vm eda: shutting WSL down so %s can be mounted read-only\n", vhdx)
	out, err := exec.Command("wsl.exe", "--shutdown").CombinedOutput()
	if err != nil {
		return fmt.Errorf("wsl --shutdown: %v %s", err, bytesTrim(out))
	}
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for {
		last = exclusiveOpen(vhdx)
		if last == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ubuntu disk still busy after wsl --shutdown: %w", last)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func exclusiveOpen(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

func bytesTrim(b []byte) string {
	return strings.TrimSpace(string(b))
}

func hyperVAdminSID() (*windows.SID, error) {
	account, err := windows.UTF16PtrFromString("Hyper-V Administrators")
	if err != nil {
		return nil, err
	}
	var sidLen, domLen, use uint32
	err = windows.LookupAccountName(nil, account, nil, &sidLen, nil, &domLen, &use)
	if err != windows.ERROR_INSUFFICIENT_BUFFER {
		if err == nil {
			return nil, fmt.Errorf("LookupAccountName returned no size")
		}
		return nil, err
	}
	sidBuf := make([]byte, sidLen)
	domBuf := make([]uint16, domLen)
	sid := (*windows.SID)(unsafe.Pointer(&sidBuf[0]))
	err = windows.LookupAccountName(nil, account, sid, &sidLen, &domBuf[0], &domLen, &use)
	if err != nil {
		return nil, err
	}
	return sid.Copy()
}

// hyperVInToken reports whether this logon can create VMs. known is false when
// the group SID could not be resolved. Membership is applied at logon, so an
// account that was just added is a member of the group and still not in the token.
func hyperVInToken() (in, known bool) {
	sid, err := hyperVAdminSID()
	if err != nil {
		return false, false
	}
	groups, err := windows.GetCurrentProcessToken().GetTokenGroups()
	if err != nil {
		return false, false
	}
	for _, g := range groups.AllGroups() {
		if g.Sid != nil && g.Sid.Equals(sid) && g.Attributes&windows.SE_GROUP_ENABLED != 0 {
			return true, true
		}
	}
	return false, true
}

const hyperVTokenHint = "This logon token does not include Hyper-V Administrators. Sign out and back in, or run tunneltug from an elevated terminal."
