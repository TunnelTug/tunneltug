package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParsePyBlocks(t *testing.T) {
	src := `
BLOCKS = (
    "jev_kuramoto",
    "jev4",
    "jev_dot_pipe",
)
OTHER = ("nope",)
`
	got, err := parsePyBlocks(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"jev_kuramoto", "jev4", "jev_dot_pipe"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("blocks %v", got)
	}
}

func TestImportBlocksMatchRepo(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "WASMApollo", "pnr", "flow", "import_pdk.py"))
	if err != nil {
		t.Skip(err)
	}
	got, err := parsePyBlocks(string(b))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"jev_kuramoto", "jev4", "jev_dot_pipe", "jev_dot_spi", "jev_tile_2x2", "jev_mesh_2x2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("BLOCKS drifted from import_pdk.py: %v", got)
	}
}

func TestEDAJobArgv(t *testing.T) {
	got := edaJobArgv("/mnt/c/Users/grego/WASMApollo/pnr/flow/import.sh", "ihp-sg13g2", "jev4", "drt", "5", true)
	want := "/init eda -- /bin/bash /mnt/c/Users/grego/WASMApollo/pnr/flow/import.sh ihp-sg13g2 jev4 --through drt --prerun-size --sta-gate 5"
	if strings.Join(got, " ") != want {
		t.Fatalf("%s", strings.Join(got, " "))
	}
	name, err := edaVMName("jev_dot_pipe")
	if err != nil || name != "eda-jev_dot_pipe" {
		t.Fatalf("%s %v", name, err)
	}
	if _, err := edaVMName("a/b"); err == nil {
		t.Fatal("slash in a design name")
	}
}

func TestWSLGuestMount(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive paths")
	}
	got, err := wslGuestMount(`C:\Users\grego\WASMApollo`)
	if err != nil || got != "/mnt/c/Users/grego/WASMApollo" {
		t.Fatalf("%s %v", got, err)
	}
	shares, err := edaShares(`C:\Users\grego\WASMApollo`, `C:\Users\grego\OpenViaduct`)
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 2 || !shares[1].ReadOnly || shares[1].Mount != "/mnt/c/Users/grego/OpenViaduct" {
		t.Fatalf("%+v", shares)
	}
}

func TestHostExecAllowed(t *testing.T) {
	dir := t.TempDir()
	shares := []tugVMShare{{HostPath: dir}}
	dawn := filepath.Join(dir, "dawn", "dawn.exe")
	if !hostExecAllowed(dawn, shares) {
		t.Fatal("dawn under the share")
	}
	if hostExecAllowed(filepath.Join(dir+"-evil", "dawn.exe"), shares) {
		t.Fatal("a longer prefix is not the share")
	}
	if hostExecAllowed(filepath.Join(filepath.Dir(dir), "cmd.exe"), shares) {
		t.Fatal("outside the share")
	}
}
