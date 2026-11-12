package vmproto

import (
	"fmt"
	"strings"
)

// WSLPath converts between a drvfs path and a Windows path the way wslpath does.
// The OpenViaduct logic proof calls `wslpath -w` so Dawn receives a path the
// Windows process can open. TunnelTug has no WSL interop socket, so the guest
// answers that call locally.
//
//	mode "w": /mnt/c/Users/a → C:\Users\a
//	mode "u": C:\Users\a    → /mnt/c/Users/a
func WSLPath(mode, p string) (string, error) {
	switch mode {
	case "w":
		if !strings.HasPrefix(p, "/mnt/") || len(p) < 6 {
			return "", fmt.Errorf("wslpath: %s is not under /mnt/<drive>", p)
		}
		drive := p[5]
		if drive >= 'A' && drive <= 'Z' {
			drive += 'a' - 'A'
		}
		if drive < 'a' || drive > 'z' {
			return "", fmt.Errorf("wslpath: bad drive in %s", p)
		}
		rest := ""
		if len(p) > 6 {
			if p[6] != '/' {
				return "", fmt.Errorf("wslpath: bad drive in %s", p)
			}
			rest = strings.ReplaceAll(p[7:], "/", `\`)
		}
		return string(drive-'a'+'A') + `:\` + rest, nil
	case "u":
		p = strings.ReplaceAll(p, "/", `\`)
		if len(p) < 2 || p[1] != ':' {
			return "", fmt.Errorf("wslpath: %s is not a drive path", p)
		}
		drive := p[0]
		if drive >= 'A' && drive <= 'Z' {
			drive += 'a' - 'A'
		}
		if drive < 'a' || drive > 'z' {
			return "", fmt.Errorf("wslpath: bad drive in %s", p)
		}
		rest := ""
		if len(p) > 2 {
			if p[2] != '\\' && p[2] != '/' {
				return "", fmt.Errorf("wslpath: bad drive in %s", p)
			}
			rest = strings.ReplaceAll(p[2:], `\`, "/")
		}
		return "/mnt/" + string(drive) + rest, nil
	default:
		return "", fmt.Errorf("wslpath: mode %q", mode)
	}
}
