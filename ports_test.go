package main

import "testing"

func TestHostnameOrdinal(t *testing.T) {
	cases := []struct {
		host string
		want int
		ok   bool
	}{
		{"tunneltug-barge-0", 0, true},
		{"tunneltug-barge-2", 2, true},
		{"tunneltug-barge-2.tunneltug.svc.cluster.local", 2, true},
		{"foo", 0, false},
		{"foo-", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, err := hostnameOrdinal(tc.host)
		if tc.ok {
			if err != nil {
				t.Fatalf("hostnameOrdinal(%q): %v", tc.host, err)
			}
			if got != tc.want {
				t.Fatalf("hostnameOrdinal(%q)=%d, want %d", tc.host, got, tc.want)
			}
		} else if err == nil {
			t.Fatalf("hostnameOrdinal(%q) expected error", tc.host)
		}
	}
}

func TestPortForIndex(t *testing.T) {
	got, err := portForIndex("9001", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != "9003" {
		t.Fatalf("got %s, want 9003", got)
	}
	got, err = portForIndex("8445", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got != "8455" {
		t.Fatalf("got %s, want 8455", got)
	}
	if _, err := portForIndex("65530", 10, 1); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestApplyIndexFromHostname(t *testing.T) {
	resetFlags(t)
	*indexFromHostname = false
	*controlPort = "9001"
	if err := applyIndexFromHostname(); err != nil {
		t.Fatal(err)
	}
	if *controlPort != "9001" {
		t.Fatalf("disabled path should not change ports")
	}
}
