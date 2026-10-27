package driver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/mount-utils"
)

// The volume directory was deleted on the server before unstage: stat of the
// mount point fails with ENOENT but the mount is still listed. It must be
// unmounted, not reported as already gone.
func TestUnstageUnmountsLustreWhoseDirectoryIsGone(t *testing.T) {
	staging := filepath.Join(t.TempDir(), "globalmount") // never created: stat gives ENOENT
	fm := mount.NewFakeMounter([]mount.MountPoint{{Device: "mgs@o2ib:/fs/ns_pvc-1", Path: staging, Type: "lustre"}})
	ns, _ := newTestNodeServer(t, fm)

	var unmounted []string
	old := lustreUmount
	lustreUmount = func(_ context.Context, target string) error {
		unmounted = append(unmounted, target)
		return fm.Unmount(target)
	}
	defer func() { lustreUmount = old }()

	if _, err := ns.NodeUnstageVolume(context.Background(),
		&csi.NodeUnstageVolumeRequest{VolumeId: "ns_pvc-1", StagingTargetPath: staging}); err != nil {
		t.Fatalf("unstage: %v", err)
	}
	if len(unmounted) != 1 || unmounted[0] != staging {
		t.Fatalf("unmounted = %v, want [%s]", unmounted, staging)
	}
	if _, ok := mountedAt(fm, staging); ok {
		t.Fatal("staging mount leaked")
	}

	// Retry after the cleanup is a no-op.
	if _, err := ns.NodeUnstageVolume(context.Background(),
		&csi.NodeUnstageVolumeRequest{VolumeId: "ns_pvc-1", StagingTargetPath: staging}); err != nil || len(unmounted) != 1 {
		t.Fatalf("retry: err=%v unmounted=%v", err, unmounted)
	}
}

func TestUnpublishUnmountsBindWhoseDirectoryIsGone(t *testing.T) {
	target := filepath.Join(t.TempDir(), "mount") // never created
	fm := mount.NewFakeMounter([]mount.MountPoint{{Device: "/staging", Path: target, Type: "none", Opts: []string{"bind"}}})
	ns, _ := newTestNodeServer(t, fm)

	if _, err := ns.NodeUnpublishVolume(context.Background(),
		&csi.NodeUnpublishVolumeRequest{VolumeId: "ns_pvc-1", TargetPath: target}); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if _, ok := mountedAt(fm, target); ok {
		t.Fatal("target bind leaked")
	}
}
