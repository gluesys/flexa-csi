package webapi

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestLustreCreateVolumeSendsExportOffForNativeMount(t *testing.T) {
	var bodies []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		_, _ = w.Write([]byte(`{"data":{"path":"/lustre/agent/fs1/v"},"msg":"ok"}`))
	}))
	defer srv.Close()
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	fep := &FEP{Ip: host, Port: p}

	for _, noExport := range []bool{true, false} {
		if _, err := fep.LustreCreateVolume(1<<20, "fs1", "v", "zone_v", "10.0.0.0", "255.255.255.0",
			"RW", "off", "off", "", noExport); err != nil {
			t.Fatalf("create(noExport=%v): %v", noExport, err)
		}
	}
	if len(bodies) != 2 || bodies[0]["export"] != "off" {
		t.Fatalf("native mount body = %v, want export=off", bodies)
	}
	if _, ok := bodies[1]["export"]; ok {
		t.Fatalf("nfs body has export key: %v", bodies[1])
	}
}
