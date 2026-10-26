package driver

import (
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSnapshotIDRoundTrip(t *testing.T) {
	for _, profile := range []string{"zfs", ""} {
		id := snapshotID(profile, "csiPool_A", "pvc-1", "snapshot-2")
		ref, ok := parseSnapshotID(id)
		want := snapshotRef{profile, "csiPool_A", "pvc-1", "snapshot-2"}
		if !ok || ref != want {
			t.Fatalf("parse(%q) = %+v %v", id, ref, ok)
		}
	}
	for _, bad := range []string{"", "pvc-1@s", "zfs:p", "zfs:p:v@s", "zfs:x:p:v", "zfs:x::v@s",
		"zfs:x:p:@s", "zfs:x:p:v@", "zfs:x:p:v@a/b"} {
		if _, ok := parseSnapshotID(bad); ok {
			t.Errorf("parse(%q) accepted", bad)
		}
	}
}

func TestSnapNameKeepsValidNamesAndHashesOthers(t *testing.T) {
	if got := snapName("snapshot-0a1b"); got != "snapshot-0a1b" {
		t.Errorf("got %q", got)
	}
	a, b := snapName("my snap/1"), snapName("my snap/1")
	if a != b || !backendSnapName.MatchString(a) || !strings.HasPrefix(a, "snapshot-") {
		t.Errorf("hashed name %q / %q", a, b)
	}
}

func snapSource(id string) *csi.VolumeContentSource {
	return &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{
		Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: id}}}
}

func TestRestoreSource(t *testing.T) {
	vol, snap, err := restoreSource(snapSource(snapshotID("zfs", "p1", "pvc-a", "snapshot-1")), "zfs", "p1", "zfs")
	if err != nil || vol != "pvc-a" || snap != "snapshot-1" {
		t.Fatalf("got %q %q %v", vol, snap, err)
	}
	if v, s, err := restoreSource(nil, "zfs", "p1", "zfs"); v != "" || s != "" || err != nil {
		t.Errorf("nil source: %q %q %v", v, s, err)
	}
	clone := &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Volume{
		Volume: &csi.VolumeContentSource_VolumeSource{VolumeId: "pvc-a"}}}
	cases := []struct {
		src           *csi.VolumeContentSource
		fs, pool, prf string
		code          codes.Code
	}{
		{clone, "zfs", "p1", "zfs", codes.InvalidArgument},
		{snapSource(snapshotID("zfs", "p1", "pvc-a", "s1")), "lustre", "", "zfs", codes.InvalidArgument},
		{snapSource(snapshotID("zfs", "p2", "pvc-a", "s1")), "zfs", "p1", "zfs", codes.InvalidArgument},
		{snapSource(snapshotID("zfsB", "p1", "pvc-a", "s1")), "zfs", "p1", "zfs", codes.InvalidArgument},
		{snapSource("other-driver-id"), "zfs", "p1", "zfs", codes.NotFound},
	}
	for i, c := range cases {
		if _, _, err := restoreSource(c.src, c.fs, c.pool, c.prf); status.Code(err) != c.code {
			t.Errorf("case %d: %v, want %v", i, err, c.code)
		}
	}
}
