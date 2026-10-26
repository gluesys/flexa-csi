/*
Copyright 2026 Gluesys FlexA Inc.
*/

package driver

import "testing"

func TestDefaultSecureZone(t *testing.T) {
	cases := []struct {
		addr, sub, mountIP string
		wantAddr, wantSub  string
		wantErr            bool
	}{
		{"10.0.0.0", "255.0.0.0", "172.16.0.0/18", "10.0.0.0", "255.0.0.0", false},
		{"", "", "172.16.0.0/18", "172.16.0.0", "255.255.192.0", false},
		{"", "", "172.16.10.31/18", "172.16.0.0", "255.255.192.0", false},
		{"", "", "10.0.0.10", "10.0.0.0", "255.255.255.0", false},
		{"10.0.0.0", "", "172.16.0.0/18", "", "", true},
		{"", "", "", "", "", true},
		{"", "", "bad", "", "", true},
		{"", "", "fd00::/64", "", "", true},
	}
	for _, c := range cases {
		a, s, err := defaultSecureZone(c.addr, c.sub, c.mountIP)
		if (err != nil) != c.wantErr || a != c.wantAddr || s != c.wantSub {
			t.Errorf("defaultSecureZone(%q,%q,%q) = %q,%q,%v", c.addr, c.sub, c.mountIP, a, s, err)
		}
	}
}
