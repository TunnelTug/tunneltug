package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func testVMSpec() tugVMSpec {
	return tugVMSpec{
		Name:       "eda",
		CPUs:       8,
		MemoryMB:   12289,
		KernelPath: `C:\tug\vm\kernel`,
		InitrdPath: `C:\tug\vm\initrd.img`,
		Disks: []tugVMDisk{
			{Path: `C:\tug\vm\modules.vhd`, ReadOnly: true, Role: "modules"},
			{Path: `C:\tug\vm\data.vhdx`, Role: "data"},
		},
		Shares: []tugVMShare{
			{Name: "wasmapollo", HostPath: `C:\Users\grego\WASMApollo`, Mount: "/mnt/wasmapollo"},
			{Name: "openviaduct", HostPath: `C:\Users\grego\OpenViaduct`, Mount: "/mnt/openviaduct", ReadOnly: true},
		},
		UserSID: "S-1-5-21-1-2-3-1001",
	}
}

func TestBuildTugVMDocumentShape(t *testing.T) {
	raw, err := buildTugVMDocument(testVMSpec())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	vm := doc["VirtualMachine"].(map[string]any)
	mem := vm["ComputeTopology"].(map[string]any)["Memory"].(map[string]any)
	if mem["SizeInMB"].(float64) != 12288 {
		t.Fatalf("memory not rounded to 2 MB: %v", mem["SizeInMB"])
	}
	scsi := vm["Devices"].(map[string]any)["Scsi"].(map[string]any)["0"].(map[string]any)["Attachments"].(map[string]any)
	if len(scsi) != 2 || scsi["0"].(map[string]any)["ReadOnly"] != true || scsi["1"].(map[string]any)["ReadOnly"] != false {
		t.Fatalf("scsi attachments wrong: %v", scsi)
	}
	shares := vm["Devices"].(map[string]any)["Plan9"].(map[string]any)["Shares"].([]any)
	ro := shares[1].(map[string]any)
	if ro["Port"].(float64) != tugVMPlan9PortMin+1 || uint32(ro["Flags"].(float64))&plan9ReadOnly == 0 {
		t.Fatalf("read-only share wrong: %v", ro)
	}
	sd := vm["Devices"].(map[string]any)["HvSocket"].(map[string]any)["HvSocketConfig"].(map[string]any)["DefaultBindSecurityDescriptor"]
	if sd != "D:P(A;;FA;;;SY)(A;;FA;;;S-1-5-21-1-2-3-1001)" {
		t.Fatalf("hvsocket SD: %v", sd)
	}
	if strings.Contains(raw, "VirtioSerial") {
		t.Fatal("no console pipe was set, VirtioSerial must be omitted")
	}
}

func TestTugVMKernelCmdLine(t *testing.T) {
	s := testVMSpec()
	s.ConsolePipe = `\\.\pipe\tug-eda-console`
	got := tugVMKernelCmdLine(s)
	for _, want := range []string{
		"rdinit=/init", "nr_cpus=8", "console=hvc0",
		"tug.disks=modules,data", "tug.ctl=50100",
		"tug.share=wasmapollo:50110:/mnt/wasmapollo:rw",
		"tug.share=openviaduct:50111:/mnt/openviaduct:ro",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("cmdline missing %q: %s", want, got)
		}
	}
}

func TestTugVMUbuntuDisk(t *testing.T) {
	s := testVMSpec()
	s.Disks = append(s.Disks, tugVMDisk{Path: `C:\wsl\ext4.vhdx`, ReadOnly: true, Role: "ubuntu"})
	raw, err := buildTugVMDocument(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tugVMKernelCmdLine(s), "tug.disks=modules,data,ubuntu") {
		t.Fatal(tugVMKernelCmdLine(s))
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	vm := doc["VirtualMachine"].(map[string]any)
	scsi := vm["Devices"].(map[string]any)["Scsi"].(map[string]any)["0"].(map[string]any)["Attachments"].(map[string]any)
	disk := scsi["2"].(map[string]any)
	if disk["Path"] != `C:\wsl\ext4.vhdx` || disk["ReadOnly"] != true {
		t.Fatalf("ubuntu disk: %v", disk)
	}
}

func TestTugVMSpecValidate(t *testing.T) {
	s := testVMSpec()
	s.Disks[0].Role = "root"
	if _, err := buildTugVMDocument(s); err == nil {
		t.Fatal("unknown disk role accepted")
	}
	s = testVMSpec()
	s.Shares = append(s.Shares, s.Shares[0])
	if _, err := buildTugVMDocument(s); err == nil {
		t.Fatal("duplicate share accepted")
	}
}

func TestHvsockServiceID(t *testing.T) {
	if got := hvsockServiceID(50100); got != "0000C3B4-FACB-11E6-BD58-64006A7986D3" {
		t.Fatal(got)
	}
}
