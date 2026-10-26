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
