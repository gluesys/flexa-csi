package driver

import (
	"testing"

	"github.com/gluesys/flexa-csi/pkg/flexa/common"
)

// An existing volume keeps only proxyIP/proxyPort; the token, scheme and CA
// come from client-info, so switching a proxy to https covers old volumes.
func TestProxyForVolumeTakesSchemeAndCAFromClientInfo(t *testing.T) {
	d := &Driver{}
	d.SetClientInfoConfig(&common.ClientInfoConfig{Profiles: map[string]common.ProxyInfo{
		"zfs": {Host: "10.0.0.2", Port: 9001, Token: "tok", Scheme: "https", CA: "PEM"},
	}})
	p, err := d.ProxyForVolume(map[string]string{volAttrProxyIP: "10.0.0.2", volAttrProxyPort: "9001", volAttrProxyProfile: "zfs"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Token != "tok" || p.Scheme != "https" || p.CA != "PEM" {
		t.Fatalf("proxy = %+v", p)
	}
	p, err = d.ProxyForVolume(map[string]string{volAttrProxyIP: "10.9.9.9", volAttrProxyPort: "9001"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Token != "" || p.Scheme != "" {
		t.Fatalf("unknown address got settings: %+v", p)
	}
}
