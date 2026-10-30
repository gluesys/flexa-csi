/*
 * Copyright 2025 Gluesys FlexA Inc.
 */

package common

import (
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

	Profiles map[string]struct {
		ProxyIP   string `yaml:"proxyIP"`
		ProxyPort int    `yaml:"proxyPort"`
		MountIP   string `yaml:"mountIP,omitempty"`
		Fs        string `yaml:"fs,omitempty"`
		Token     string `yaml:"token,omitempty"`
	} `yaml:"profiles,omitempty"`
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
	if strings.TrimSpace(in.Host) != "" && in.Port != 0 {
		cfg.Default = &ProxyInfo{
			Host:    strings.TrimSpace(in.Host),
			Port:    in.Port,
			MountIP: strings.TrimSpace(in.MountIP),
			Fs:      strings.TrimSpace(in.Fs),
			Token:   defToken,
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
		cfg.Profiles[n] = ProxyInfo{
			Host:    ip,
			Port:    p.ProxyPort,
			MountIP: strings.TrimSpace(p.MountIP),
			Fs:      strings.TrimSpace(p.Fs),
			Token:   token,
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

// TokenFor returns the token configured for the proxy at host:port. Volume
// attributes keep only proxyIP/proxyPort, so the token is looked up here and a
// rotated token in client-info takes effect for existing volumes too.
// The named profile wins when several entries point to the same proxy.
func (cfg *ClientInfoConfig) TokenFor(host string, port int, profile string) string {
	if cfg == nil {
		return ""
	}
	if p, ok := cfg.Profiles[profile]; ok && p.Host == host && p.Port == port {
		return p.Token
	}
	if d := cfg.Default; d != nil && d.Host == host && d.Port == port {
		return d.Token
	}
	for _, p := range cfg.Profiles {
		if p.Host == host && p.Port == port && p.Token != "" {
			return p.Token
		}
	}
	return ""
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
