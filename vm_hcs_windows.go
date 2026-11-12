//go:build windows

package main

// Host Compute Service bindings (computecore.dll), the Go side of WSL's
// src/windows/common/hcs.cpp. Every call is synchronous: create an operation,
// issue the call, wait for its result document.

import (
	"encoding/json"
	"fmt"
	"strings"
	"unsafe"

	"github.com/Microsoft/go-winio/pkg/guid"
	"golang.org/x/sys/windows"
)

var (
	modComputeCore = windows.NewLazySystemDLL("computecore.dll")

	procHcsCreateOperation            = modComputeCore.NewProc("HcsCreateOperation")
	procHcsCloseOperation             = modComputeCore.NewProc("HcsCloseOperation")
	procHcsWaitForOperationResult     = modComputeCore.NewProc("HcsWaitForOperationResult")
	procHcsCreateComputeSystem        = modComputeCore.NewProc("HcsCreateComputeSystem")
	procHcsOpenComputeSystem          = modComputeCore.NewProc("HcsOpenComputeSystem")
	procHcsStartComputeSystem         = modComputeCore.NewProc("HcsStartComputeSystem")
	procHcsShutDownComputeSystem      = modComputeCore.NewProc("HcsShutDownComputeSystem")
	procHcsTerminateComputeSystem     = modComputeCore.NewProc("HcsTerminateComputeSystem")
	procHcsCloseComputeSystem         = modComputeCore.NewProc("HcsCloseComputeSystem")
	procHcsModifyComputeSystem        = modComputeCore.NewProc("HcsModifyComputeSystem")
	procHcsGetComputeSystemProperties = modComputeCore.NewProc("HcsGetComputeSystemProperties")
	procHcsEnumerateComputeSystems    = modComputeCore.NewProc("HcsEnumerateComputeSystems")
	procHcsGetServiceProperties       = modComputeCore.NewProc("HcsGetServiceProperties")
	procHcsGrantVmAccess              = modComputeCore.NewProc("HcsGrantVmAccess")
	procHcsRevokeVmAccess             = modComputeCore.NewProc("HcsRevokeVmAccess")
)

const hcsInfinite = 0xFFFFFFFF

type hcsSystem uintptr

// hcsError carries the HRESULT and the result document HCS returns with it.
type hcsError struct {
	Op     string
	HR     uint32
	Result string
}

func (e *hcsError) Error() string {
	msg := windows.Errno(e.HR).Error() // FormatMessage knows the HCS_E_* HRESULTs
	if e.Result != "" {
		return fmt.Sprintf("%s: hr=0x%08X (%s) %s", e.Op, e.HR, msg, e.Result)
	}
	return fmt.Sprintf("%s: hr=0x%08X (%s)", e.Op, e.HR, msg)
}

func hcsFailed(r uintptr) bool { return int32(r) < 0 }

func utf16Ptr(s string) *uint16 {
	if s == "" {
		return nil
	}
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		panic(err) // only NUL bytes; callers pass validated text
	}
	return p
}

// takeHcsString copies and LocalFree's an HCS-allocated PWSTR.
func takeHcsString(p *uint16) string {
	if p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(p)))
	return s
}

// hcsRun creates an operation, runs call(op), and waits for the result document.
func hcsRun(name string, call func(op uintptr) uintptr) (string, error) {
	if err := modComputeCore.Load(); err != nil {
		return "", fmt.Errorf("%s: computecore.dll not available (enable Virtual Machine Platform): %w", name, err)
	}
	op, _, _ := procHcsCreateOperation.Call(0, 0)
	if op == 0 {
		return "", fmt.Errorf("%s: HcsCreateOperation failed", name)
	}
	defer procHcsCloseOperation.Call(op)
	if r := call(op); hcsFailed(r) {
		return "", &hcsError{Op: name, HR: uint32(r)}
	}
	var doc *uint16
	r, _, _ := procHcsWaitForOperationResult.Call(op, hcsInfinite, uintptr(unsafe.Pointer(&doc)))
	result := takeHcsString(doc)
	if hcsFailed(r) {
		return result, &hcsError{Op: name, HR: uint32(r), Result: result}
	}
	return result, nil
}

func hcsCreateComputeSystem(id, document string) (hcsSystem, error) {
	var sys hcsSystem
	_, err := hcsRun("HcsCreateComputeSystem", func(op uintptr) uintptr {
		r, _, _ := procHcsCreateComputeSystem.Call(
			uintptr(unsafe.Pointer(utf16Ptr(id))), uintptr(unsafe.Pointer(utf16Ptr(document))),
			op, 0, uintptr(unsafe.Pointer(&sys)))
		return r
	})
	return sys, err
}

func hcsOpenComputeSystem(id string) (hcsSystem, error) {
	var sys hcsSystem
	r, _, _ := procHcsOpenComputeSystem.Call(uintptr(unsafe.Pointer(utf16Ptr(id))), windows.GENERIC_ALL, uintptr(unsafe.Pointer(&sys)))
	if hcsFailed(r) {
		return 0, &hcsError{Op: "HcsOpenComputeSystem", HR: uint32(r)}
	}
	return sys, nil
}

func (s hcsSystem) start() error {
	_, err := hcsRun("HcsStartComputeSystem", func(op uintptr) uintptr {
		r, _, _ := procHcsStartComputeSystem.Call(uintptr(s), op, 0)
		return r
	})
	return err
}

func (s hcsSystem) shutdown() error {
	_, err := hcsRun("HcsShutDownComputeSystem", func(op uintptr) uintptr {
		r, _, _ := procHcsShutDownComputeSystem.Call(uintptr(s), op, 0)
		return r
	})
	return err
}

func (s hcsSystem) terminate() error {
	_, err := hcsRun("HcsTerminateComputeSystem", func(op uintptr) uintptr {
		r, _, _ := procHcsTerminateComputeSystem.Call(uintptr(s), op, 0)
		return r
	})
	return err
}

func (s hcsSystem) modify(document string) error {
	_, err := hcsRun("HcsModifyComputeSystem", func(op uintptr) uintptr {
		r, _, _ := procHcsModifyComputeSystem.Call(uintptr(s), op, uintptr(unsafe.Pointer(utf16Ptr(document))), 0)
		return r
	})
	return err
}

func (s hcsSystem) properties(query string) (string, error) {
	return hcsRun("HcsGetComputeSystemProperties", func(op uintptr) uintptr {
		r, _, _ := procHcsGetComputeSystemProperties.Call(uintptr(s), op, uintptr(unsafe.Pointer(utf16Ptr(query))))
		return r
	})
}

// runtimeID is the partition GUID hvsocket addresses (WSL's hcs::GetRuntimeId).
func (s hcsSystem) runtimeID() (guid.GUID, error) {
	doc, err := s.properties("")
	if err != nil {
		return guid.GUID{}, err
	}
	var p struct {
		RuntimeID string `json:"RuntimeId"`
	}
	if err := json.Unmarshal([]byte(doc), &p); err != nil || p.RuntimeID == "" {
		return guid.GUID{}, fmt.Errorf("compute system properties have no RuntimeId: %s", doc)
	}
	return guid.FromString(strings.Trim(p.RuntimeID, "{}"))
}

func (s hcsSystem) close() {
	if s != 0 {
		procHcsCloseComputeSystem.Call(uintptr(s))
	}
}

// hcsEnumerate lists compute systems (query "" = all visible to the caller).
func hcsEnumerate(query string) (string, error) {
	return hcsRun("HcsEnumerateComputeSystems", func(op uintptr) uintptr {
		r, _, _ := procHcsEnumerateComputeSystems.Call(uintptr(unsafe.Pointer(utf16Ptr(query))), op)
		return r
	})
}

// hcsServiceProperties answers service queries such as {"PropertyTypes":["Basic"]}
// (supported schema versions). It needs no VM and is the cheapest HCS reachability probe.
func hcsServiceProperties(query string) (string, error) {
	if err := modComputeCore.Load(); err != nil {
		return "", fmt.Errorf("computecore.dll not available (enable Virtual Machine Platform): %w", err)
	}
	var doc *uint16
	r, _, _ := procHcsGetServiceProperties.Call(uintptr(unsafe.Pointer(utf16Ptr(query))), uintptr(unsafe.Pointer(&doc)))
	result := takeHcsString(doc)
	if hcsFailed(r) {
		return result, &hcsError{Op: "HcsGetServiceProperties", HR: uint32(r), Result: result}
	}
	return result, nil
}

// hcsGrantVMAccess lets the VM worker process open a host file (disks, kernel).
func hcsGrantVMAccess(vmID, path string) error {
	r, _, _ := procHcsGrantVmAccess.Call(uintptr(unsafe.Pointer(utf16Ptr(vmID))), uintptr(unsafe.Pointer(utf16Ptr(path))))
	if hcsFailed(r) {
		return &hcsError{Op: "HcsGrantVmAccess(" + path + ")", HR: uint32(r)}
	}
	return nil
}

func hcsRevokeVMAccess(vmID, path string) error {
	r, _, _ := procHcsRevokeVmAccess.Call(uintptr(unsafe.Pointer(utf16Ptr(vmID))), uintptr(unsafe.Pointer(utf16Ptr(path))))
	if hcsFailed(r) {
		return &hcsError{Op: "HcsRevokeVmAccess(" + path + ")", HR: uint32(r)}
	}
	return nil
}
