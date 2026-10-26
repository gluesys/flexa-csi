package webapi

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
)

func TestEveryRequestCarriesBearerToken(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{},"msg":"ok"}`))
	}))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	fep := &FEP{Ip: host, Port: port, Token: "tok"}
	fep.sendRequest("", nil, url.Values{}, "zfs/pool/list")
	fep.sendRequest("x", nil, url.Values{"a": {"b"}}, "zfs/pool/p/volume/v/create")
	fep.ResolveLustreVip("10.0.0.1")
	fep.lustrePost("lustre/cluster/c/volume/v/expand", map[string]string{})
	want := []string{"GET /csi/zfs/pool/list", "POST /csi/zfs/pool/p/volume/v/create",
		"POST /csi/lustre/vip/resolve", "POST /csi/lustre/cluster/c/volume/v/expand"}
	for _, k := range want {
		if seen[k] != "Bearer tok" {
			t.Errorf("%s Authorization = %q, want Bearer tok", k, seen[k])
		}
	}

	// Without a token no Authorization header is sent.
	seen = map[string]string{}
	(&FEP{Ip: host, Port: port}).sendRequest("", nil, url.Values{}, "zfs/pool/list")
	if v, ok := seen["GET /csi/zfs/pool/list"]; !ok || v != "" {
		t.Errorf("tokenless request Authorization = %q (seen=%v)", v, ok)
	}
}
