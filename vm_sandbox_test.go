package main

import (
	"flag"
	"path/filepath"
	"testing"
)

func TestFillVMOptionsFromSandbox(t *testing.T) {
	o := tugVMOptions{Name: "other", CPUs: 4, MemoryMB: 12288}
	sb := &SiteSandbox{
		Name:     "sandbox",
		CPUs:     8,
		MemoryMB: 4096,
		DataGB:   32,
		KubePort: 16443,
		Shares: []SiteSandboxShare{{
			Name:  "work",
			Host:  `C:\src\share`,
			Mount: "/mnt/work",
		}},
	}
	fillVMOptionsFromSandbox(&o, sb, nil)
	if o.Name != "sandbox" || o.CPUs != 8 || o.MemoryMB != 4096 || o.DataGB != 32 || o.KubePort != 16443 {
		t.Fatalf("%+v", o)
	}
	if len(o.Shares) != 1 || o.Shares[0].HostPath != `C:\src\share` || o.Shares[0].Mount != "/mnt/work" {
		t.Fatalf("%+v", o.Shares)
	}

	kept := tugVMOptions{Name: "kept", MemoryMB: 2048}
	fillVMOptionsFromSandbox(&kept, sb, map[string]bool{"name": true, "memory": true})
	if kept.Name != "kept" || kept.MemoryMB != 2048 || kept.CPUs != 8 {
		t.Fatalf("%+v", kept)
	}
}

func TestApplySiteSandboxExample(t *testing.T) {
	fs := flag.NewFlagSet("vm", flag.ContinueOnError)
	var o tugVMOptions
	var sitePath, sitePop string
	fs.StringVar(&sitePath, "config", "", "")
	fs.StringVar(&sitePop, "pop", "", "")
	fs.StringVar(&o.Name, "name", "other", "")
	fs.Uint64Var(&o.MemoryMB, "memory", 12288, "")
	if err := fs.Parse([]string{"-config", filepath.Join("config", "site.sandbox.example.yaml"), "-pop", "sfo"}); err != nil {
		t.Fatal(err)
	}
	if err := applySiteSandbox(&o, fs, sitePath, sitePop); err != nil {
		t.Fatal(err)
	}
	if o.Name != "sandbox" || o.CPUs != 8 || o.MemoryMB != 12288 || o.KubePort != 16443 {
		t.Fatalf("%+v", o)
	}
	if len(o.Shares) != 1 || o.Shares[0].Name != "work" || o.Shares[0].Mount != "/mnt/work" {
		t.Fatalf("%+v", o.Shares)
	}
}
