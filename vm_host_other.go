//go:build !windows

package main

import "fmt"

// On Linux hosts TunnelTug drives an existing k3s/k8s node directly (-mode stack,
// -mode barge); the owned VM runtime is the Windows (HCS) path.
var errTugVMWindowsOnly = fmt.Errorf("the TunnelTug VM runtime needs a Windows host (HCS); on Linux point -k3s-kubeconfig at the node")

func tugVMProbe(tugVMOptions) error  { return errTugVMWindowsOnly }
func tugVMUp(tugVMOptions) error     { return errTugVMWindowsOnly }
func tugVMDown(tugVMOptions) error   { return errTugVMWindowsOnly }
func tugVMStatus(tugVMOptions) error { return errTugVMWindowsOnly }

func tugVMExec(tugVMOptions, []string) (int, error) { return 1, errTugVMWindowsOnly }
