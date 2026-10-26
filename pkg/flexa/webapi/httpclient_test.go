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
