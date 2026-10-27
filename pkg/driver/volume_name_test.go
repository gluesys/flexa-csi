package driver

import (
	"strings"
	"testing"
)

func TestRenderVolumeName(t *testing.T) {
	const pv = "pvc-1a2b3c4d-0000-1111-2222-333344445555"
	cases := []struct {
		format, fs, ns, pvc string
		want                string
		wantErr             bool
	}{
		{"", "lustre", "gluesys05", "home", pv, false},
		{"", "zfs", "gluesys05", "home", pv, false},
		{"{namespace}_{pvname}", "lustre", "gluesys05", "home", "gluesys05_" + pv, false},
		{"{namespace}.{pvname}", "lustre", "gluesys05", "home", "gluesys05." + pv, false},
		{"{namespace}_{pvname}", "zfs", "gluesys05", "home", "gluesys05_" + pv, false},
		{"{namespace}.{pvname}", "zfs", "gluesys05", "home", "", true},     // zfs: no '.'
		{"{namespace}_{pvname}", "zfs", "0ns", "home", "", true},           // zfs: must start with a letter
		{"{namespace}/{pvname}", "lustre", "gluesys05", "home", "", true},  // no '/'
		{"{namespace}_{pvcname}", "lustre", "gluesys05", "home", "", true}, // {pvname} required
		{"{pvname}-{uid}", "lustre", "gluesys05", "home", "", true},        // unknown placeholder
		{"{namespace}_{pvname}", "lustre", "", "", "", true},               // no PVC metadata
		{"", "lustre", "", "", pv, false},
		{strings.Repeat("a", 90) + "{pvname}", "lustre", "ns", "home", "", true}, // > 128
	}
	for _, c := range cases {
		got, err := renderVolumeName(c.format, c.fs, pv, c.ns, c.pvc)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("renderVolumeName(%q, %s, %s) = %q, %v; want %q, err=%v",
				c.format, c.fs, c.ns, got, err, c.want, c.wantErr)
		}
	}
}
