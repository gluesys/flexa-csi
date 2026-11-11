package webapi

import (
	"errors"
	"testing"
)

func TestIsMissingMatchesOnlyTheNamedObject(t *testing.T) {
	legacy := func(status int, msg string) error { return &APIError{Status: status, Msg: msg} }
	cases := []struct {
		err  error
		what []string
		want bool
	}{
		{legacy(409, "Failed to Delete Volume in Pool(p1)\n Error : Volume(pvc-1) does not exist"), []string{"Volume(pvc-1)"}, true},
		{legacy(409, "Failed to Delete Share in Volume(pvc-1)\n Error : pvc-1 does not exist"), []string{"pvc-1"}, true},
		{legacy(404, "Share(pvc-1) does not exist"), []string{"Share(pvc-1)"}, true},
		// A missing pool says nothing about the volume.
		{legacy(409, "Failed to Delete Volume in Pool(p1)\n Error : Pool(p1) does not exist"), []string{"Volume(pvc-1)"}, false},
		{legacy(409, "Volume(pvc-2) does not exist"), []string{"Volume(pvc-1)"}, false},
		{legacy(404, "Not Found"), []string{"Volume(pvc-1)"}, false},
		{legacy(500, "Volume(pvc-1) does not exist"), []string{"Volume(pvc-1)"}, false},
		{errors.New("Volume(pvc-1) does not exist"), []string{"Volume(pvc-1)"}, false},
	}
	for i, c := range cases {
		if got := IsMissing(c.err, c.what...); got != c.want {
			t.Errorf("case %d: IsMissing(%v, %v) = %t, want %t", i, c.err, c.what, got, c.want)
		}
	}
}

func TestNewAPIErrorKeepsProxyMessage(t *testing.T) {
	e := newAPIError(409, []byte(`{"data":null,"msg":"pvc-1 already exists"}`), "share/x")
	if e.Msg != "pvc-1 already exists" || !IsAlreadyExists(e) {
		t.Fatalf("APIError = %+v", e)
	}
	raw := newAPIError(502, []byte("bad gateway"), "zfs/x")
	if raw.Msg != "bad gateway" {
		t.Fatalf("non-JSON body lost: %+v", raw)
	}
}

// The share conflicts are matched in the messages exactly as the proxies
// send them; other 409 answers keep their meaning.
func TestIsShareConflict(t *testing.T) {
	legacyFail := func(msg string) string {
		return "Failed to Create Share in Volume(pvc-1)\n Error : " + msg
	}
	lost := " (root share k8s-pool1-zone_csi_10-0-0-0_8 was removed outside provisiond and the pool is no longer CSI-only)"
	yes := []string{
		"Share(pvc-1) already exists with different settings",
		"Secure zone(zone_csi_10-0-0-0_8) already exists with a different network (10.1.0.0/255.255.0.0)",
		"Root share(k8s-pool1-zone_csi_10-0-0-0_8) uses different NFS options {'access': 'RW', 'optionNoRootSquashing': 'off', 'optionInsecure': 'off'}",
		"Root share(k8s-pool1-zone_csi_10-0-0-0_8) has NFS disabled or no zone zone_csi_10-0-0-0_8",
		"Share(k8s-pool1-zone_csi_10-0-0-0_8) exists but is not the CSI root export of Pool(pool1)",
		"Pool(pool1) has OpenStack volumes (cdr-1, mnl-2); CSI exports the whole pool and needs a pool of its own",
		"Pool(pool1) has OpenStack volumes (cdr-1); CSI exports the whole pool and needs a pool of its own" + lost,
		legacyFail("pvc-1 already exists with different NFS options"),
	}
	no := []string{
		"Volume(pvc-1) does not exist in Pool(pool1)",
		"Share(pvc-1) belongs to a different volume",
		"pvc-1 already exists",
		legacyFail("pvc-1 already exists"),
	}
	for _, m := range yes {
		if !IsShareConflict(&APIError{Status: 409, Msg: m}) {
			t.Errorf("409 %q: not a conflict", m)
		}
		if IsShareConflict(&APIError{Status: 500, Msg: m}) {
			t.Errorf("500 %q: taken for a conflict", m)
		}
	}
	for _, m := range no {
		if IsShareConflict(&APIError{Status: 409, Msg: m}) {
			t.Errorf("409 %q: taken for a conflict", m)
		}
	}
	if IsShareConflict(errors.New(yes[0])) {
		t.Error("an error without a proxy answer is taken for a conflict")
	}
}
