//go:build !windows

package main

func discoverUbuntuVHDX() (string, error) { return "", errTugVMWindowsOnly }
func releaseWSLDisk(string) error         { return errTugVMWindowsOnly }
