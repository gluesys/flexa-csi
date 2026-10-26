package driver

import (
	"testing"

	"github.com/gluesys/flexa-csi/pkg/flexa/common"
)

func TestCapacityProfilesAreSortedWithLegacyDefaultLast(t *testing.T) {
	cfg := &common.ClientInfoConfig{
		Default: &common.ProxyInfo{Host: "10.0.0.1", Port: 9001},
		Profiles: map[string]common.ProxyInfo{
			"zfs-b":  {Host: "10.0.0.2", Port: 9001},
			"lustre": {Host: "10.0.0.3", Port: 9001, Fs: "Lustre"},
			"zfs-a":  {Host: "10.0.0.4", Port: 9001, Fs: "zfs"},
		},
	}
	got := capacityProfiles(cfg)
	want := []capacityProfile{
		{name: "lustre", fs: "lustre"},
		{name: "zfs-a", fs: "zfs"},
		{name: "zfs-b", fs: "zfs"},
		{name: "", fs: "zfs"},
	}
	if len(got) != len(want) {
		t.Fatalf("profiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("profiles[%d] = %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}
	// The same input must always yield the same order (offset tokens depend on it).
	for i := 0; i < 20; i++ {
		again := capacityProfiles(cfg)
		for j := range want {
			if again[j] != want[j] {
				t.Fatalf("iteration %d: profiles[%d] = %v, want %v", i, j, again[j], want[j])
			}
		}
	}
	if len(capacityProfiles(nil)) != 0 {
		t.Fatalf("nil config must yield no profiles")
	}
}

func TestNormalizeFs(t *testing.T) {
	for in, want := range map[string]string{"": "zfs", "zfs": "zfs", "ZFS": "zfs", " lustre ": "lustre", "Lustre": "lustre", "nfs": "zfs"} {
		if got := normalizeFs(in); got != want {
			t.Fatalf("normalizeFs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCapacityProfilesSkipsLegacyDefaultThatDuplicatesAProfile(t *testing.T) {
	cfg := &common.ClientInfoConfig{
		Default: &common.ProxyInfo{Host: "10.0.0.2", Port: 9001},
		Profiles: map[string]common.ProxyInfo{
			"zfs":    {Host: "10.0.0.2", Port: 9001},
			"lustre": {Host: "10.0.0.3", Port: 9001, Fs: "lustre"},
		},
	}
	got := capacityProfiles(cfg)
	if len(got) != 2 || got[0].name != "lustre" || got[1].name != "zfs" {
		t.Fatalf("legacy default pointing at profile zfs must be dropped, got %v", got)
	}

	// Same host but a different backend is not a duplicate.
	cfg.Default.Fs = "lustre"
	cfg.Default.Host = "10.0.0.2"
	got = capacityProfiles(cfg)
	if len(got) != 3 || got[2].name != "" || got[2].fs != "lustre" {
		t.Fatalf("legacy default with a different fs must be kept, got %v", got)
	}

	// Different port is not a duplicate either.
	cfg.Default = &common.ProxyInfo{Host: "10.0.0.2", Port: 9002}
	if got = capacityProfiles(cfg); len(got) != 3 || got[2].name != "" {
		t.Fatalf("legacy default on another port must be kept, got %v", got)
	}
}
