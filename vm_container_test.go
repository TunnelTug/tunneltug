package main

import (
	"os"
	"strings"
	"testing"
)

func TestExpandContainerUsesPayloadNotJobName(t *testing.T) {
	c := Container{
		Kind: "host",
		Dir:  "{apollo}",
		Argv: []string{"{bin.dawn}", "run", "{wgsl}", "{entry}", "-d", "{dispatch}"},
		Stop: map[string]string{"wgsl": "the kernel is the source"},
	}
	_, _, err := expandContainer(c, map[string]string{"apollo": "A", "bin.dawn": "dawn.exe", "entry": "comb", "dispatch": "1"}, nil)
	if err == nil || err.Error() != "the kernel is the source" {
		t.Fatalf("stop: %v", err)
	}
	dir, argv, err := expandContainer(c, map[string]string{
		"apollo": "A", "bin.dawn": "dawn.exe", "wgsl": "fabric.wgsl", "entry": "comb", "dispatch": "1",
	}, []string{"--extra"})
	if err != nil {
		t.Fatal(err)
	}
	if dir != "A" {
		t.Fatalf("dir %s", dir)
	}
	want := "dawn.exe run fabric.wgsl comb -d 1 --extra"
	got := ""
	for i, a := range argv {
		if i > 0 {
			got += " "
		}
		got += a
	}
	if got != want {
		t.Fatalf("argv %q", got)
	}
}

func TestContainerFieldsDoNotNameJobs(t *testing.T) {
	f := containerFields(map[string]any{"container": "dawn", "wgsl": "quantum.wgsl", "entry": "run"})
	if f["wgsl"] != "quantum.wgsl" || f["entry"] != "run" || f["dispatch"] != "1" {
		t.Fatalf("fields %+v", f)
	}
	if f["bin.dawn"] == "" || f["apollo"] == "" {
		t.Fatalf("tool paths %+v", f)
	}
}

func TestChipviewCatalogCallsTheBinary(t *testing.T) {
	cat, err := loadCatalog("")
	if err != nil {
		t.Fatal(err)
	}
	c, ok := cat["chipview"]
	if !ok || c.Kind != "host" || c.Out != "{run}/chipview.html" || c.Stop["netlist"] != "chipview waits on a design" {
		t.Fatalf("catalog %+v", c)
	}
	if len(c.Argv) == 0 || c.Argv[0] != "{bin.chipview}" {
		t.Fatalf("argv %v", c.Argv)
	}
	fields := containerFields(map[string]any{"netlist": "", "design": "jev_proj"})
	if _, _, err := expandContainer(c, fields, nil); err == nil || err.Error() != "chipview waits on a design" {
		t.Fatalf("stop %v", err)
	}
	fields["netlist"] = `C:\design.v`
	fields["run"] = `C:\runs\t1`
	_, argv, err := expandContainer(c, fields, nil)
	if err != nil {
		t.Fatal(err)
	}
	html, err := expandString(c.Out, fields)
	if err != nil {
		t.Fatal(err)
	}
	if html != `C:\runs\t1/chipview.html` {
		t.Fatalf("html %s", html)
	}
	if len(argv) < 4 || argv[1] != `C:\design.v` || argv[3] != html || argv[len(argv)-1] != "jev_proj" {
		t.Fatalf("argv %v", argv)
	}
	if _, err := os.Stat(argv[0]); err != nil {
		t.Fatal(err)
	}
}

func TestVMCtlCommandBuildsWasitimeArgv(t *testing.T) {
	if _, _, ok := vmctlCommand("", "", "status", nil); ok {
		t.Fatal("empty runner")
	}
	if _, _, ok := vmctlCommand("", `C:\go++\examples\vmctl\vmctl.wasm`, "status", nil); ok {
		t.Fatal("wasm without a runner")
	}
	bin, args, ok := vmctlCommand(`C:\go++\examples\wasitime\wasitime.exe`, "", "status", nil)
	if !ok || bin != `C:\go++\examples\wasitime\wasitime.exe` || strings.Join(args, " ") != "status" {
		t.Fatalf("direct %q %v", bin, args)
	}
	bin, args, ok = vmctlCommand(`C:\go++\examples\wasitime\wasitime.exe`, `C:\go++\examples\vmctl\vmctl.wasm`, "status", nil)
	if !ok || strings.Join(args, " ") != `run C:\go++\examples\vmctl\vmctl.wasm status` {
		t.Fatalf("status %q %v", bin, args)
	}
	_, args, ok = vmctlCommand(`C:\go++\examples\wasitime\wasitime.exe`, `C:\go++\examples\vmctl\vmctl.wasm`, "exec", []string{"uname"})
	if !ok || strings.Join(args, " ") != `run C:\go++\examples\vmctl\vmctl.wasm exec uname` {
		t.Fatalf("exec %v", args)
	}
}
