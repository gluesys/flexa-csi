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
	Host     string `yaml:"host,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	MountIP  string `yaml:"mountIP,omitempty"`
	// Profiles use proxyIP/proxyPort in YAML; unmarshaling is done in LoadClientInfoConfigFromReader.
}

type ClientInfoConfig struct {
	Default  *ProxyInfo
	Profiles map[string]ProxyInfo
}

type clientInfoYAML struct {
	Host     string `yaml:"host,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	MountIP  string `yaml:"mountIP,omitempty"`

	Profiles map[string]struct {
		ProxyIP   string `yaml:"proxyIP"`
		ProxyPort int    `yaml:"proxyPort"`
		MountIP   string `yaml:"mountIP,omitempty"`
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

	if strings.TrimSpace(in.Host) != "" && in.Port != 0 {
		cfg.Default = &ProxyInfo{
			Host:     strings.TrimSpace(in.Host),
			Port:     in.Port,
			MountIP:  strings.TrimSpace(in.MountIP),
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
		cfg.Profiles[n] = ProxyInfo{
			Host:     ip,
			Port:     p.ProxyPort,
			MountIP:  strings.TrimSpace(p.MountIP),
		}
	}

	log.Infof("Gluesys FlexA Call(LoadClientInfoConfig) : default=%v profiles=%d", cfg.Default != nil, len(cfg.Profiles))
	return cfg, nil
}

func LoadClientInfoConfig(configPath string) (*ClientInfoConfig, error) {
	f, err := os.Open(configPath)
	if err != nil {
		log.Errorf("Unable to open config file: %v", err)
		return nil, err
	}
	defer f.Close()
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
