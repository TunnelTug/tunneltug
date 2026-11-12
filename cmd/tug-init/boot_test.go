//go:build linux

package main

import "testing"

// The host writes tug.* with tugVMKernelCmdLine (vm_schema.go); this is the other half.
func TestParseCmdline(t *testing.T) {
	cfg := parseCmdline("panic=-1 rdinit=/init nr_cpus=8 console=hvc0 tug.disks=modules,data tug.ctl=50100 " +
		"tug.share=wasmapollo:50110:/mnt/wasmapollo:rw tug.share=openviaduct:50111:/mnt/openviaduct:ro tug.name=eda")
	if cfg.Name != "eda" || cfg.CtlPort != 50100 || len(cfg.Disks) != 2 || cfg.Disks[1] != "data" {
		t.Fatalf("cfg %+v", cfg)
	}
	if len(cfg.Shares) != 2 || cfg.Shares[1] != (shareSpec{Name: "openviaduct", Port: 50111, Mount: "/mnt/openviaduct", RO: true}) {
		t.Fatalf("shares %+v", cfg.Shares)
	}
	if bad := parseCmdline("tug.share=broken tug.ctl=x"); len(bad.Shares) != 0 || bad.CtlPort != 50100 {
		t.Fatalf("malformed keys not ignored: %+v", bad)
	}
}
