package webapi

import (
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func tlsServer(t *testing.T) (*httptest.Server, string, int, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"poolList":["p1"]},"msg":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	return srv, host, port, ca
}

// https verifies the provisiond certificate against the configured CA; a
// missing or different CA is refused, never skipped.
func TestHTTPSVerifiesServerAgainstCA(t *testing.T) {
	_, host, port, ca := tlsServer(t)
	_, _, _, otherCA := tlsServer(t)

	fep := &FEP{Ip: host, Port: port, Token: "tok", Scheme: "https", CA: ca}
	pools, err := fep.ListZfsPool()
	if err != nil || len(pools) != 1 || pools[0] != "p1" {
		t.Fatalf("https with the right CA: %v, %v", pools, err)
	}
	if got := fep.apiURL("zfs/pool/list"); got[:8] != "https://" {
		t.Fatalf("apiURL = %s", got)
	}
	for name, f := range map[string]*FEP{
		"system roots": {Ip: host, Port: port, Token: "tok", Scheme: "https"},
		"other CA":     {Ip: host, Port: port, Token: "tok", Scheme: "https", CA: otherCA},
	} {
		if _, err := f.sendRequest("", nil, url.Values{}, "zfs/pool/list"); err == nil {
			t.Errorf("%s: an unverified server was accepted", name)
		}
	}
	// Plain http to a TLS port fails too: no silent downgrade.
	if _, err := (&FEP{Ip: host, Port: port, Token: "tok"}).ListZfsPool(); err == nil {
		t.Error("http to the https port succeeded")
	}
}
