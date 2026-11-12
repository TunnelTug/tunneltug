package main

// TunnelTug VM runtime — HCS compute-system document.
//
// TunnelTug owns the VM: it boots a Linux utility VM through the Windows Host
// Compute Service the same way WSL's WSLC session VM does, and the VM is the
// node TunnelTug schedules containers onto. The document shape below is forked
// from microsoft/WSL src/windows/common/hcs_schema.h and the settings from
// src/windows/service/exe/HcsVirtualMachine.cpp (schema 2.7, LinuxKernelDirect,
// SCSI boot disks, HvSocket security, Plan9 shares). Field names are the HCS
// JSON schema names and must not be renamed.

import (
	"encoding/json"
	"fmt"
	"strings"
)

type hcsVersion struct {
	Major int `json:"Major"`
	Minor int `json:"Minor"`
}

type hcsComputeSystem struct {
	Owner                             string            `json:"Owner"`
	ShouldTerminateOnLastHandleClosed bool              `json:"ShouldTerminateOnLastHandleClosed"`
	SchemaVersion                     hcsVersion        `json:"SchemaVersion"`
	VirtualMachine                    hcsVirtualMachine `json:"VirtualMachine"`
}

type hcsVirtualMachine struct {
	StopOnReset     bool            `json:"StopOnReset"`
	Chipset         hcsChipset      `json:"Chipset"`
	ComputeTopology hcsTopology     `json:"ComputeTopology"`
	Devices         hcsDevices      `json:"Devices"`
	DebugOptions    hcsDebugOptions `json:"DebugOptions"`
}

type hcsChipset struct {
	UseUtc            bool                  `json:"UseUtc"`
	LinuxKernelDirect *hcsLinuxKernelDirect `json:"LinuxKernelDirect,omitempty"`
}

type hcsLinuxKernelDirect struct {
	KernelFilePath string `json:"KernelFilePath"`
	InitRdPath     string `json:"InitRdPath"`
	KernelCmdLine  string `json:"KernelCmdLine"`
}

type hcsTopology struct {
	Processor hcsProcessor `json:"Processor"`
	Memory    hcsMemory    `json:"Memory"`
}

type hcsProcessor struct {
	Count int `json:"Count"`
}

type hcsMemory struct {
	SizeInMB              uint64 `json:"SizeInMB"`
	AllowOvercommit       bool   `json:"AllowOvercommit"`
	EnableDeferredCommit  bool   `json:"EnableDeferredCommit"`
	EnableColdDiscardHint bool   `json:"EnableColdDiscardHint"`
	// HostingProcessNameSuffix names the vmmem process (vmmem-tunneltug in Task Manager).
	HostingProcessNameSuffix string `json:"HostingProcessNameSuffix,omitempty"`
}

type hcsDevices struct {
	VirtioSerial *hcsVirtioSerial      `json:"VirtioSerial,omitempty"`
	ComPorts     map[string]hcsComPort `json:"ComPorts"`
	Plan9        hcsPlan9              `json:"Plan9"`
	Battery      struct{}              `json:"Battery"`
	HvSocket     hcsHvSocket           `json:"HvSocket"`
	Scsi         map[string]hcsScsi    `json:"Scsi"`
}

type hcsVirtioSerial struct {
	Ports map[string]hcsVirtioSerialPort `json:"Ports"`
}

type hcsVirtioSerialPort struct {
	Name           string `json:"Name"`
	NamedPipe      string `json:"NamedPipe"`
	ConsoleSupport bool   `json:"ConsoleSupport"`
}

type hcsComPort struct {
	NamedPipe string `json:"NamedPipe"`
}

type hcsPlan9 struct {
	Shares []hcsPlan9Share `json:"Shares,omitempty"`
}

// Plan9 share flags (hcs_schema.h Plan9ShareFlags).
const (
	plan9ReadOnly      = 0x1
	plan9LinuxMetadata = 0x4
	plan9CaseSensitive = 0x8
	plan9AllowOptions  = 0x20
	plan9AllowSubPaths = 0x40
)

type hcsPlan9Share struct {
	Name       string `json:"Name"`
	AccessName string `json:"AccessName"`
	Path       string `json:"Path"`
	Port       uint32 `json:"Port"`
	Flags      uint32 `json:"Flags"`
}

type hcsHvSocket struct {
	HvSocketConfig hcsHvSocketSystemConfig `json:"HvSocketConfig"`
}

type hcsHvSocketSystemConfig struct {
	DefaultBindSecurityDescriptor    string `json:"DefaultBindSecurityDescriptor"`
	DefaultConnectSecurityDescriptor string `json:"DefaultConnectSecurityDescriptor"`
}

type hcsScsi struct {
	Attachments map[string]hcsAttachment `json:"Attachments"`
}

type hcsAttachment struct {
	Type                     string `json:"Type"` // VirtualDisk
	Path                     string `json:"Path"`
	ReadOnly                 bool   `json:"ReadOnly"`
	SupportCompressedVolumes bool   `json:"SupportCompressedVolumes"`
	AlwaysAllowSparseFiles   bool   `json:"AlwaysAllowSparseFiles"`
	SupportEncryptedFiles    bool   `json:"SupportEncryptedFiles"`
}

type hcsDebugOptions struct {
	BugcheckSavedStateFileName string `json:"BugcheckSavedStateFileName,omitempty"`
}

// hcsModifyRequest is ModifySettingRequest<T> (HcsModifyComputeSystem).
type hcsModifyRequest struct {
	ResourcePath string `json:"ResourcePath"`
	RequestType  string `json:"RequestType"` // Add | Remove | Update
	Settings     any    `json:"Settings,omitempty"`
}

// Guest-visible hvsocket ports. WSL uses 50000-50006 for its own init; TunnelTug
// keeps a separate block so a tug VM never speaks WSL's init protocol by accident.
const (
	tugVMControlPort  = 50100 // guest agent → host control channel (yamux)
	tugVMConsolePort  = 50101 // reserved: guest log stream
	tugVMPlan9PortMin = 50110 // Plan9 shares: 50110, 50111, …
)

// tugVMDisk is one SCSI attachment in boot order (LUN = index).
type tugVMDisk struct {
	Path     string
	ReadOnly bool
	// Role tells the guest what to do with the disk (modules, data, ubuntu).
	Role string
}

// tugVMShare is a host directory exposed to the guest over Plan9-on-hvsocket.
type tugVMShare struct {
	Name     string // AccessName (aname= in the guest mount)
	HostPath string
	Mount    string // guest mount point
	ReadOnly bool
}

// tugVMSpec is everything TunnelTug decides about a VM before HCS sees it.
type tugVMSpec struct {
	Name        string
	CPUs        int
	MemoryMB    uint64
	KernelPath  string
	InitrdPath  string
	Disks       []tugVMDisk
	Shares      []tugVMShare
	UserSID     string // HvSocket bind/connect ACL principal (besides SYSTEM)
	ConsolePipe string // \\.\pipe\… for hvc0, empty = none
	ExtraCmd    string // appended to the kernel command line
}

func (s tugVMSpec) validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("vm: name is required")
	}
	if s.CPUs <= 0 {
		return fmt.Errorf("vm: cpus must be > 0")
	}
	if s.MemoryMB < 512 {
		return fmt.Errorf("vm: memory %d MB is below 512 MB", s.MemoryMB)
	}
	if s.KernelPath == "" || s.InitrdPath == "" {
		return fmt.Errorf("vm: kernel and initrd are required")
	}
	// The guest root is the initramfs TunnelTug builds (immutable, like an image);
	// disks only carry kernel modules and persistent state.
	for _, d := range s.Disks {
		switch d.Role {
		case "modules", "data", "ubuntu":
		default:
			return fmt.Errorf("vm: disk %s has unknown role %q (modules|data|ubuntu)", d.Path, d.Role)
		}
	}
	if s.UserSID == "" {
		return fmt.Errorf("vm: user SID is required for the hvsocket ACL")
	}
	seen := map[string]bool{}
	for _, sh := range s.Shares {
		if sh.Name == "" || sh.HostPath == "" || !strings.HasPrefix(sh.Mount, "/") {
			return fmt.Errorf("vm: share %q needs name, host path and an absolute guest mount", sh.Name)
		}
		if seen[sh.Name] {
			return fmt.Errorf("vm: duplicate share %q", sh.Name)
		}
		seen[sh.Name] = true
	}
	return nil
}

// tugVMKernelCmdLine mirrors WSL's AppendCommonKernelCommandLine and hands the
// guest agent its layout through tug.* parameters (read from /proc/cmdline).
func tugVMKernelCmdLine(s tugVMSpec) string {
	var b strings.Builder
	b.WriteString("panic=-1 rdinit=/init")
	fmt.Fprintf(&b, " nr_cpus=%d", s.CPUs)
	b.WriteString(" hv_utils.timesync_implicit=1 printk.devkmsg=on page_reporting.page_reporting_order=5")
	if s.CPUs > 16 {
		b.WriteString(" hv_storvsc.storvsc_max_hw_queues=16")
	}
	if s.ConsolePipe != "" {
		b.WriteString(" console=hvc0")
	}
	roles := make([]string, len(s.Disks))
	for i, d := range s.Disks {
		roles[i] = d.Role
	}
	fmt.Fprintf(&b, " tug.disks=%s tug.ctl=%d", strings.Join(roles, ","), tugVMControlPort)
	for i, sh := range s.Shares {
		ro := "rw"
		if sh.ReadOnly {
			ro = "ro"
		}
		fmt.Fprintf(&b, " tug.share=%s:%d:%s:%s", sh.Name, tugVMPlan9PortMin+i, sh.Mount, ro)
	}
	if x := strings.TrimSpace(s.ExtraCmd); x != "" {
		b.WriteString(" " + x)
	}
	return b.String()
}

// buildTugVMDocument turns a spec into the HCS JSON for HcsCreateComputeSystem.
func buildTugVMDocument(s tugVMSpec) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	scsi := hcsScsi{Attachments: map[string]hcsAttachment{}}
	for lun, d := range s.Disks {
		scsi.Attachments[fmt.Sprintf("%d", lun)] = hcsAttachment{
			Type:                     "VirtualDisk",
			Path:                     d.Path,
			ReadOnly:                 d.ReadOnly,
			SupportCompressedVolumes: true,
			AlwaysAllowSparseFiles:   true,
			SupportEncryptedFiles:    true,
		}
	}
	var shares []hcsPlan9Share
	for i, sh := range s.Shares {
		flags := uint32(plan9LinuxMetadata | plan9CaseSensitive | plan9AllowOptions | plan9AllowSubPaths)
		if sh.ReadOnly {
			flags |= plan9ReadOnly
		}
		shares = append(shares, hcsPlan9Share{
			Name: sh.Name, AccessName: sh.Name, Path: sh.HostPath,
			Port: uint32(tugVMPlan9PortMin + i), Flags: flags,
		})
	}
	// Same ACL as WSLC: SYSTEM + the launching user, full access, protected DACL.
	sd := fmt.Sprintf("D:P(A;;FA;;;SY)(A;;FA;;;%s)", s.UserSID)

	doc := hcsComputeSystem{
		Owner:                             "TunnelTug-" + s.Name,
		ShouldTerminateOnLastHandleClosed: true,
		SchemaVersion:                     hcsVersion{Major: 2, Minor: 7},
		VirtualMachine: hcsVirtualMachine{
			StopOnReset: true,
			Chipset: hcsChipset{
				UseUtc: true,
				LinuxKernelDirect: &hcsLinuxKernelDirect{
					KernelFilePath: s.KernelPath,
					InitRdPath:     s.InitrdPath,
					KernelCmdLine:  tugVMKernelCmdLine(s),
				},
			},
			ComputeTopology: hcsTopology{
				Processor: hcsProcessor{Count: s.CPUs},
				Memory: hcsMemory{
					SizeInMB:                 s.MemoryMB &^ 1, // HCS enforces 2 MB granularity
					AllowOvercommit:          true,
					EnableDeferredCommit:     true,
					EnableColdDiscardHint:    true,
					HostingProcessNameSuffix: sanitizeVmmemSuffix("tunneltug-" + s.Name),
				},
			},
			Devices: hcsDevices{
				ComPorts: map[string]hcsComPort{},
				Plan9:    hcsPlan9{Shares: shares},
				HvSocket: hcsHvSocket{HvSocketConfig: hcsHvSocketSystemConfig{
					DefaultBindSecurityDescriptor:    sd,
					DefaultConnectSecurityDescriptor: sd,
				}},
				Scsi: map[string]hcsScsi{"0": scsi},
			},
		},
	}
	if s.ConsolePipe != "" {
		doc.VirtualMachine.Devices.VirtioSerial = &hcsVirtioSerial{Ports: map[string]hcsVirtioSerialPort{
			"0": {Name: "hvc0", NamedPipe: s.ConsolePipe, ConsoleSupport: true},
		}}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// sanitizeVmmemSuffix keeps [A-Za-z0-9-] like WSL's SanitizeHostingProcessNameSuffix.
func sanitizeVmmemSuffix(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// hvsockServiceID is the hvsocket service GUID that Linux AF_VSOCK port N maps to
// (VSOCK template 00000000-FACB-11E6-BD58-64006A7986D3 with Data1 = port).
func hvsockServiceID(port uint32) string {
	return fmt.Sprintf("%08X-FACB-11E6-BD58-64006A7986D3", port)
}
