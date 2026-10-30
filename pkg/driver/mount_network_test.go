// Copyright 2025 FlexA Inc.

package driver

import (
	"testing"

	"github.com/gluesys/flexa-csi/pkg/flexa/common"
)

func TestProxyForClass(t *testing.T) {
	base := &common.ProxyInfo{Host: "192.0.2.221", Port: 9001, MountIP: "192.0.2.0/24", Token: "t"}

	got, err := proxyForClass(base, map[string]string{})
	if err != nil || got != base {
		t.Fatalf("no mountNetwork: got %+v, %v; want the same proxy", got, err)
	}

	got, err = proxyForClass(base, map[string]string{paramMountNetwork: " 198.51.100.0/24 "})
	if err != nil {
		t.Fatal(err)
	}
	if got.MountIP != "198.51.100.0/24" || got.Host != base.Host || got.Port != base.Port || got.Token != base.Token {
		t.Fatalf("mountNetwork: got %+v", got)
	}
	if base.MountIP != "192.0.2.0/24" {
		t.Fatalf("shared proxy changed: %+v", base)
	}

	if got, err = proxyForClass(base, map[string]string{paramMountNetwork: "198.51.100.221"}); err != nil || got.MountIP != "198.51.100.221" {
		t.Fatalf("single IP: got %+v, %v", got, err)
	}
	for _, bad := range []string{"ib0", "fd00::/64"} {
		if _, err = proxyForClass(base, map[string]string{paramMountNetwork: bad}); err == nil {
			t.Fatalf("invalid mountNetwork %q accepted", bad)
		}
	}
}

func TestCheckMountNetwork(t *testing.T) {
	for _, c := range []struct {
		network, vip string
		ok           bool
	}{
		{"", "192.0.2.221", true},
		{"198.51.100.0/24", "198.51.100.221", true},
		{"198.51.100.5", "198.51.100.221", true}, // a single IP means its /24
		{"198.51.100.0/24", "192.0.2.221", false},
		{"198.51.100.0/24", "", false},
	} {
		err := checkMountNetwork(map[string]string{paramMountNetwork: c.network}, c.vip)
		if (err == nil) != c.ok {
			t.Errorf("checkMountNetwork(%q, %q) = %v, want ok=%v", c.network, c.vip, err, c.ok)
		}
	}
}
