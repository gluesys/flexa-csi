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

// The node fetches the MGS NID list for a Lustre mount with its own FEP; it
// must use the same scheme and CA as the controller.
func TestNodeProxyFEPCarriesSchemeAndCA(t *testing.T) {
	d := &Driver{}
	d.SetClientInfoConfig(&common.ClientInfoConfig{Profiles: map[string]common.ProxyInfo{
		"lustre": {Host: "10.0.0.3", Port: 9001, Token: "tok", Scheme: "https", CA: "PEM"},
	}})
	fep, err := (&NodeServer{Driver: d}).resolveProxyFEP(map[string]string{volAttrProxyIP: "10.0.0.3", volAttrProxyPort: "9001", volAttrProxyProfile: "lustre"})
	if err != nil {
		t.Fatal(err)
	}
	if fep.Scheme != "https" || fep.CA != "PEM" || fep.Token != "tok" {
		t.Fatalf("node FEP = %+v", fep)
	}
}
