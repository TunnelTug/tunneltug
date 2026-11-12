//go:build linux

package main

import "testing"

func TestUnescapeMount(t *testing.T) {
	if got := unescapeMount(`/mnt/c/Users/grego/My\040Documents`); got != "/mnt/c/Users/grego/My Documents" {
		t.Fatal(got)
	}
	if got := unescapeMount("/mnt/c/Users/grego/WASMApollo"); got != "/mnt/c/Users/grego/WASMApollo" {
		t.Fatal(got)
	}
}
