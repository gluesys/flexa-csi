package webapi

import (
	"encoding/json"
	"testing"
)

func TestZfsNfsShareName(t *testing.T) {
	if got := ZfsNfsShareName("pvc-1234"); got != "pvc-1234" {
		t.Fatalf("new CSI share name = %q, want %q", got, "pvc-1234")
	}
	if got := ZfsNfsShareName(""); got != LegacyZfsNfsShareName {
		t.Fatalf("empty volume ID share name = %q, want legacy %q",
			got, LegacyZfsNfsShareName)
	}
}

func TestZfsPoolInfoAcceptsQuotedAndBareNumbers(t *testing.T) {
	// pool_info builds "total" with str() while "used"/"free" pass through
	// from Redis, so both forms must decode to the same MB values.
	body := []byte(`{"poolName":"p1","total":"20480","used":8192,"free":"12288","status":"normal"}`)
	var got ZfsPoolInfo
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Total != 20480 || got.Used != 8192 || got.Free != 12288 {
		t.Fatalf("pool sizes = %d/%d/%d, want 20480/8192/12288", got.Total, got.Used, got.Free)
	}
	var empty ZfsPoolInfo
	if err := json.Unmarshal([]byte(`{"poolName":"p2","total":"","free":null}`), &empty); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if empty.Total != 0 || empty.Free != 0 {
		t.Fatalf("empty sizes = %d/%d, want 0/0", empty.Total, empty.Free)
	}
	for _, body := range []string{`{"free":"lots"}`, `{"free":"NaN"}`, `{"free":"Inf"}`} {
		var bad ZfsPoolInfo
		if err := json.Unmarshal([]byte(body), &bad); err == nil {
			t.Fatalf("expected error for %s", body)
		}
	}
}
