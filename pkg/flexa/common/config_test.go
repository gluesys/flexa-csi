package common

import (
	"strings"
	"testing"
)

func TestLoadClientInfoConfigParsesLegacyDefaultFs(t *testing.T) {
	cfg, err := LoadClientInfoConfigFromReader(strings.NewReader(`
host: 10.0.0.1
port: 9001
fs: lustre
profiles:
  zfs:
    proxyIP: 10.0.0.2
    proxyPort: 9001
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Default == nil || cfg.Default.Fs != "lustre" {
		t.Fatalf("legacy default fs = %+v, want lustre", cfg.Default)
	}
	if cfg.Profiles["zfs"].Fs != "" {
		t.Fatalf("profile fs must stay empty (zfs by default), got %q", cfg.Profiles["zfs"].Fs)
	}

	cfg, err = LoadClientInfoConfigFromReader(strings.NewReader("host: 10.0.0.1\nport: 9001\n"))
	if err != nil {
		t.Fatalf("load legacy-only: %v", err)
	}
	if cfg.Default == nil || cfg.Default.Fs != "" {
		t.Fatalf("legacy default without fs must keep Fs empty, got %+v", cfg.Default)
	}
}

func TestClientInfoTokenIsInheritedAndFoundByAddress(t *testing.T) {
	cfg, err := LoadClientInfoConfigFromReader(strings.NewReader(`
host: 10.0.0.1
port: 9001
token: top
profiles:
  same:
    proxyIP: 10.0.0.1
    proxyPort: 9001
  other:
    proxyIP: 10.0.0.2
    proxyPort: 9001
    token: own
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Default.Token != "top" || cfg.Profiles["same"].Token != "top" || cfg.Profiles["other"].Token != "own" {
		t.Fatalf("tokens = default %q, same %q, other %q", cfg.Default.Token,
			cfg.Profiles["same"].Token, cfg.Profiles["other"].Token)
	}
	cases := []struct {
		host, profile, want string
	}{
		{"10.0.0.2", "other", "own"},
		{"10.0.0.2", "", "own"},     // volume attributes without a profile
		{"10.0.0.2", "same", "own"}, // profile now points elsewhere: match by address
		{"10.0.0.1", "", "top"},
		{"10.0.0.9", "", ""},
	}
	for _, c := range cases {
		if got := cfg.TokenFor(c.host, 9001, c.profile); got != c.want {
			t.Errorf("TokenFor(%s, %q) = %q, want %q", c.host, c.profile, got, c.want)
		}
	}
}
