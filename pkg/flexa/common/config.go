/*
 * Copyright 2025 Gluesys FlexA Inc.
 */

package common

import (
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

// ProxyInfo describes one Gluesys FlexA CSI proxy endpoint and VIP resolve reference.
// Host/Port are used for HTTP to the proxy; MountIP (e.g. 192.168.0.0/18) is used for VIP resolve body ip when set.
type ProxyInfo struct {
	Host    string `yaml:"host,omitempty"`
	Port    int    `yaml:"port,omitempty"`
	MountIP string `yaml:"mountIP,omitempty"`
	// Fs identifies the backend filesystem served by this proxy ("zfs" or "lustre").
	// Used by ListVolumes/GetCapacity to pick the correct list/capacity path. Empty defaults to "zfs".
	Fs string `yaml:"fs,omitempty"`
	// Token is the provisiond client token sent as "Authorization: Bearer <token>".
	// Issued on the storage with: provisiond.ctl client-token create --name csi-<cluster>.
	Token string `yaml:"token,omitempty"`
	// Scheme is "http" (default) or "https". provisiond serves https when
	// prov:config tls is on (new installs); the token then travels encrypted.
	Scheme string `yaml:"scheme,omitempty"`
	// CA is the PEM CA certificate that signs the provisiond server
	// certificate (python3.12 -m provisiond.ctl tls show-ca). Empty with https:
	// the system roots verify the server.
	CA string `yaml:"ca,omitempty"`
	// Profiles use proxyIP/proxyPort in YAML; unmarshaling is done in LoadClientInfoConfigFromReader.
}

type ClientInfoConfig struct {
	Default  *ProxyInfo
	Profiles map[string]ProxyInfo
}

type clientInfoYAML struct {
	Host    string `yaml:"host,omitempty"`
	Port    int    `yaml:"port,omitempty"`
	MountIP string `yaml:"mountIP,omitempty"`
	// Fs is the backend served by the legacy host/port proxy ("zfs" or "lustre").
	Fs string `yaml:"fs,omitempty"`
	// Token is used by the legacy host/port proxy and by profiles without their own token.
	Token string `yaml:"token,omitempty"`
	// Scheme and CA, like Token, apply to the legacy host/port proxy and to
	// profiles that do not set their own.
	Scheme string `yaml:"scheme,omitempty"`
	CA     string `yaml:"ca,omitempty"`

	Profiles map[string]struct {
		ProxyIP   string `yaml:"proxyIP"`
		ProxyPort int    `yaml:"proxyPort"`
		MountIP   string `yaml:"mountIP,omitempty"`
		Fs        string `yaml:"fs,omitempty"`
		Token     string `yaml:"token,omitempty"`
		Scheme    string `yaml:"scheme,omitempty"`
		CA        string `yaml:"ca,omitempty"`
	} `yaml:"profiles,omitempty"`
}

// connSettings checks a scheme/CA pair from client-info and fills the
// defaults: scheme "http", CA only with https and only if it parses.
func connSettings(where, scheme, ca, defScheme, defCA string) (string, string, error) {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme == "" {
		scheme = defScheme
	}
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return "", "", fmt.Errorf("%s: scheme must be http or https, not %q", where, scheme)
	}
	ca = strings.TrimSpace(ca)
	if ca == "" {
		ca = defCA
	}
	if scheme == "http" {
		return scheme, "", nil
	}
	if ca != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(ca)) {
		return "", "", fmt.Errorf("%s: ca holds no PEM certificate", where)
	}
	return scheme, ca, nil
}

func LoadClientInfoConfigFromReader(r io.Reader) (*ClientInfoConfig, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	in := clientInfoYAML{}
	if err := yaml.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("failed to parse client-info: %w", err)
	}

	cfg := &ClientInfoConfig{
		Default:  nil,
		Profiles: map[string]ProxyInfo{},
	}

	defToken := strings.TrimSpace(in.Token)
	defScheme := strings.ToLower(strings.TrimSpace(in.Scheme))
	defCA := strings.TrimSpace(in.CA)
	if strings.TrimSpace(in.Host) != "" && in.Port != 0 {
		scheme, ca, err := connSettings("client-info", in.Scheme, in.CA, "", "")
		if err != nil {
			return nil, err
		}
		cfg.Default = &ProxyInfo{
			Host:    strings.TrimSpace(in.Host),
			Port:    in.Port,
			MountIP: strings.TrimSpace(in.MountIP),
			Fs:      strings.TrimSpace(in.Fs),
			Token:   defToken,
			Scheme:  scheme,
			CA:      ca,
		}
	}

	for name, p := range in.Profiles {
		n := strings.TrimSpace(name)
		if n == "" {
			continue
		}
		ip := strings.TrimSpace(p.ProxyIP)
		if ip == "" || p.ProxyPort == 0 {
			return nil, fmt.Errorf("profiles.%s must have proxyIP and proxyPort", n)
		}
		token := strings.TrimSpace(p.Token)
		if token == "" {
			token = defToken
		}
		scheme, ca, err := connSettings("profiles."+n, p.Scheme, p.CA, defScheme, defCA)
		if err != nil {
			return nil, err
		}
		cfg.Profiles[n] = ProxyInfo{
			Host:    ip,
			Port:    p.ProxyPort,
			MountIP: strings.TrimSpace(p.MountIP),
			Fs:      strings.TrimSpace(p.Fs),
			Token:   token,
			Scheme:  scheme,
			CA:      ca,
		}
	}

	log.Infof("Gluesys FlexA Call(LoadClientInfoConfig) : default=%v profiles=%d", cfg.Default != nil, len(cfg.Profiles))
	if cfg.Default != nil && cfg.Default.Token == "" {
		log.Warnf("client-info: legacy host/port has no token; provisiond will reject its requests (401)")
	}
	for name, p := range cfg.Profiles {
		if p.Token == "" {
			log.Warnf("client-info: profile %q has no token; provisiond will reject its requests (401)", name)
		}
	}
	return cfg, nil
}

// EntryFor returns the client-info entry for the proxy at host:port. Volume
// attributes keep only proxyIP/proxyPort, so the token, scheme and CA are
// looked up here, and a rotated token or a switch to https in client-info
// takes effect for existing volumes too. The named profile wins when several
// entries point to the same proxy; otherwise one with a token is preferred.
func (cfg *ClientInfoConfig) EntryFor(host string, port int, profile string) (ProxyInfo, bool) {
	if cfg == nil {
		return ProxyInfo{}, false
	}
	if p, ok := cfg.Profiles[profile]; ok && p.Host == host && p.Port == port {
		return p, true
	}
	if d := cfg.Default; d != nil && d.Host == host && d.Port == port {
		return *d, true
	}
	var found *ProxyInfo
	for n := range cfg.Profiles {
		p := cfg.Profiles[n]
		if p.Host != host || p.Port != port {
			continue
		}
		if p.Token != "" {
			return p, true
		}
		if found == nil {
			found = &p
		}
	}
	if found != nil {
		return *found, true
	}
	return ProxyInfo{}, false
}

// TokenFor returns the token configured for the proxy at host:port.
func (cfg *ClientInfoConfig) TokenFor(host string, port int, profile string) string {
	e, _ := cfg.EntryFor(host, port, profile)
	return e.Token
}

func LoadClientInfoConfig(configPath string) (*ClientInfoConfig, error) {
	f, err := os.Open(configPath)
	if err != nil {
		log.Errorf("Unable to open config file: %v", err)
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return LoadClientInfoConfigFromReader(f)
}

// LoadConfig is legacy: returns the single default proxy from client-info.yml.
// This is kept for backward compatibility with existing code paths.
func LoadConfig(configPath string) (*ProxyInfo, error) {
	cfg, err := LoadClientInfoConfig(configPath)
	if err != nil {
		return nil, err
	}
	if cfg.Default == nil {
		return nil, fmt.Errorf("client-info has no legacy host/port default")
	}
	return cfg.Default, nil
}
