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

const testCA = `-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l
Wf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc
6MF9+Yw1Yy0t
-----END CERTIFICATE-----`

// scheme and ca: top-level values are the defaults for the legacy entry and
// for profiles, like token; http stays the default; bad values are refused.
func TestLoadClientInfoConfigSchemeAndCA(t *testing.T) {
	indent := "  " + strings.ReplaceAll(testCA, "\n", "\n  ")
	cfg, err := LoadClientInfoConfigFromReader(strings.NewReader(`
host: 10.0.0.1
port: 9001
scheme: HTTPS
ca: |
` + indent + `
profiles:
  a:
    proxyIP: 10.0.0.2
    proxyPort: 9001
  b:
    proxyIP: 10.0.0.3
    proxyPort: 9001
    scheme: http
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Default.Scheme != "https" || !strings.Contains(cfg.Default.CA, "BEGIN CERTIFICATE") {
		t.Fatalf("default = %s %q", cfg.Default.Scheme, cfg.Default.CA)
	}
	if a := cfg.Profiles["a"]; a.Scheme != "https" || a.CA != cfg.Default.CA {
		t.Fatalf("profile a did not inherit scheme/ca: %+v", a)
	}
	if b := cfg.Profiles["b"]; b.Scheme != "http" || b.CA != "" {
		t.Fatalf("profile b = %+v, want plain http", b)
	}
	if e, ok := cfg.EntryFor("10.0.0.2", 9001, ""); !ok || e.Scheme != "https" {
		t.Fatalf("EntryFor = %+v %t", e, ok)
	}

	plain, err := LoadClientInfoConfigFromReader(strings.NewReader("host: 10.0.0.1\nport: 9001\n"))
	if err != nil || plain.Default.Scheme != "http" {
		t.Fatalf("default scheme = %+v, %v", plain.Default, err)
	}
	for name, y := range map[string]string{
		"scheme": "host: 10.0.0.1\nport: 9001\nscheme: ftp\n",
		"ca":     "host: 10.0.0.1\nport: 9001\nscheme: https\nca: not-a-cert\n",
	} {
		if _, err := LoadClientInfoConfigFromReader(strings.NewReader(y)); err == nil {
			t.Errorf("bad %s accepted", name)
		}
	}
}
