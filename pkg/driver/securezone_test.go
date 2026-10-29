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

func TestSecureZoneName(t *testing.T) {
	cases := []struct{ addr, sub, want string }{
		{"172.16.0.0", "255.255.192.0", "zone_csi_172-16-0-0_18"},
		{"10.0.0.0", "255.0.0.0", "zone_csi_10-0-0-0_8"},
		{"192.0.2.0", "255.255.255.0", "zone_csi_192-0-2-0_24"},
		// 연속되지 않은 마스크는 길이로 못 줄이니 그대로 쓴다.
		{"10.0.0.0", "255.0.255.0", "zone_csi_10-0-0-0_255-0-255-0"},
		{"fd00::", "64", "zone_csi_fd00--_64"},
	}
	for _, c := range cases {
		if got := secureZoneName(c.addr, c.sub); got != c.want {
			t.Errorf("secureZoneName(%q,%q) = %q, want %q", c.addr, c.sub, got, c.want)
		}
	}
	// 같은 네트워크는 같은 이름이다(PVC 와 관계없이).
	if secureZoneName("10.0.0.0", "255.255.255.0") != secureZoneName(" 10.0.0.0", "255.255.255.0 ") {
		t.Error("same network must give the same zone name")
	}
}
