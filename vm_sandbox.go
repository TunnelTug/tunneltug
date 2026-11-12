package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"
)

// applySiteSandbox overlays the site sandbox for pop onto o.
// Flags the operator passed win over the sandbox block.
func applySiteSandbox(o *tugVMOptions, fs *flag.FlagSet, sitePath, pop string) error {
	sitePath = strings.TrimSpace(sitePath)
	if sitePath == "" {
		return nil
	}
	cfg, err := loadSiteAny(sitePath)
	if err != nil {
		return err
	}
	plan, err := ExpandSite(cfg, pop, filepath.Dir(sitePath))
	if err != nil {
		return err
	}
	if plan.Sandbox == nil {
		if strings.TrimSpace(pop) == "" {
			return fmt.Errorf("site %s has no sandbox", sitePath)
		}
		return fmt.Errorf("site %s has no sandbox for pop %s", sitePath, pop)
	}
	visited := map[string]bool{}
	if fs != nil {
		fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	}
	fillVMOptionsFromSandbox(o, plan.Sandbox, visited)
	return nil
}

func fillVMOptionsFromSandbox(o *tugVMOptions, sb *SiteSandbox, visited map[string]bool) {
	if sb == nil || o == nil {
		return
	}
	if visited == nil {
		visited = map[string]bool{}
	}
	if !visited["name"] && sb.Name != "" {
		o.Name = sb.Name
	}
	if !visited["home"] && strings.TrimSpace(sb.Home) != "" {
		o.Home = sb.Home
	}
	if !visited["cpus"] && sb.CPUs > 0 {
		o.CPUs = sb.CPUs
	}
	if !visited["memory"] && sb.MemoryMB > 0 {
		o.MemoryMB = sb.MemoryMB
	}
	if !visited["data-gb"] && sb.DataGB > 0 {
		o.DataGB = sb.DataGB
	}
	if !visited["kube-port"] && sb.KubePort > 0 {
		o.KubePort = sb.KubePort
	}
	if !visited["share"] && len(sb.Shares) > 0 {
		o.Shares = nil
		for _, sh := range sb.Shares {
			o.Shares = append(o.Shares, tugVMShare{
				Name:     sh.Name,
				HostPath: sh.Host,
				Mount:    sh.Mount,
				ReadOnly: sh.ReadOnly,
			})
		}
	}
}
