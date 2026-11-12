package vmproto

import "testing"

func TestWSLPath(t *testing.T) {
	win, err := WSLPath("w", "/mnt/c/Users/grego/WASMApollo/dawn/x.wgsl")
	if err != nil || win != `C:\Users\grego\WASMApollo\dawn\x.wgsl` {
		t.Fatalf("to windows: %q %v", win, err)
	}
	unix, err := WSLPath("u", `C:\Users\grego\OpenViaduct\route\prerun`)
	if err != nil || unix != "/mnt/c/Users/grego/OpenViaduct/route/prerun" {
		t.Fatalf("to unix: %q %v", unix, err)
	}
	if _, err := WSLPath("w", "/home/grego/eda"); err == nil {
		t.Fatal("a distro path is not a Windows path")
	}
	if _, err := WSLPath("u", "relative"); err == nil {
		t.Fatal("a relative path is not a drive path")
	}
}
