package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/mount-utils"
)

// failingRemount mimics a bind whose "bind,remount,ro" step fails: the bind is
// recorded, then the error is returned, as mount-utils does on Linux.
type failingRemount struct {
	*mount.FakeMounter
}

func (f failingRemount) Mount(source, target, fstype string, options []string) error {
	if slices.Contains(options, "ro") {
		_ = f.FakeMounter.Mount(source, target, fstype, []string{"bind"})
		return errors.New("remount failed")
	}
	return f.FakeMounter.Mount(source, target, fstype, options)
}

func newTestNodeServer(t *testing.T, mounter mount.Interface, objs ...runtime.Object) (*NodeServer, string) {
	t.Helper()
	root := t.TempDir()
	d := &Driver{K8sClient: fake.NewSimpleClientset(objs...)}
	return &NodeServer{
		Driver:    d,
		Mounter:   &mount.SafeFormatAndMount{Interface: mounter},
		locks:     newVolumeLocks(),
		metaRoot:  filepath.Join(root, "meta"),
		groupRoot: filepath.Join(root, "group"),
	}, root
}

func nfsPod(name string, annotations map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Annotations: annotations}}
}

func nfsPublishReq(volId, target, pod string, readonly bool, mode csi.VolumeCapability_AccessMode_Mode) *csi.NodePublishVolumeRequest {
	return &csi.NodePublishVolumeRequest{
		VolumeId:   volId,
		TargetPath: target,
		Readonly:   readonly,
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: mode},
		},
		VolumeContext: map[string]string{
			"protocol":                         "nfs",
			"vip":                              "10.0.0.1",
			"baseDir":                          "/pool/" + volId,
			"csi.storage.k8s.io/pod.name":      pod,
			"csi.storage.k8s.io/pod.namespace": "ns",
		},
	}
}

func mountedAt(m *mount.FakeMounter, path string) (mount.MountPoint, bool) {
	mps, _ := m.List()
	for _, mp := range mps {
		if mp.Path == path {
			return mp, true
		}
	}
	return mount.MountPoint{}, false
}

func TestPublishSharesGroupMountAndLastUnpublishReleasesIt(t *testing.T) {
	fm := mount.NewFakeMounter(nil)
	ns, root := newTestNodeServer(t, fm, nfsPod("a", nil), nfsPod("b", nil))
	ctx := context.Background()
	rwx := csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER
	t1, t2 := filepath.Join(root, "t1"), filepath.Join(root, "t2")

	for _, r := range []*csi.NodePublishVolumeRequest{
		nfsPublishReq("pvc-1", t1, "a", false, rwx),
		nfsPublishReq("pvc-1", t2, "b", false, rwx),
	} {
		if _, err := ns.NodePublishVolume(ctx, r); err != nil {
			t.Fatalf("publish %s: %v", r.TargetPath, err)
		}
	}
	group := ns.getNfsGroupPath("pvc-1", nil)
	if _, ok := mountedAt(fm, group); !ok {
		t.Fatalf("group mount %s missing", group)
	}

	if _, err := ns.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "pvc-1", TargetPath: t1}); err != nil {
		t.Fatalf("unpublish t1: %v", err)
	}
	if _, ok := mountedAt(fm, group); !ok {
		t.Fatal("group mount released while t2 still uses it")
	}

	if _, err := ns.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "pvc-1", TargetPath: t2}); err != nil {
		t.Fatalf("unpublish t2: %v", err)
	}
	if _, ok := mountedAt(fm, group); ok {
		t.Fatal("group mount kept after the last unpublish")
	}
	if _, err := os.Stat(ns.metaDir("pvc-1")); !os.IsNotExist(err) {
		t.Fatalf("meta dir left behind: %v", err)
	}
	// A repeat is a no-op.
	if _, err := ns.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "pvc-1", TargetPath: t2}); err != nil {
		t.Fatalf("repeat unpublish: %v", err)
	}
}

func TestUnpublishKeepsDataWhenGroupUnmountFails(t *testing.T) {
	fm := mount.NewFakeMounter(nil)
	ns, root := newTestNodeServer(t, fm, nfsPod("a", nil))
	ctx := context.Background()
	target := filepath.Join(root, "t1")
	if _, err := ns.NodePublishVolume(ctx, nfsPublishReq("pvc-1", target, "a", false,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	group := ns.getNfsGroupPath("pvc-1", nil)
	// A file inside the NFS mount stands for user data.
	data := filepath.Join(group, "data")
	if err := os.WriteFile(data, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	fm.UnmountFunc = func(path string) error {
		if path == group {
			return syscall.EBUSY
		}
		return nil
	}
	_, err := ns.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "pvc-1", TargetPath: target})
	if status.Code(err) != codes.Internal {
		t.Fatalf("unpublish with busy group = %v, want Internal", err)
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatalf("data under the group mount was removed: %v", err)
	}

	// Once the mount is free, the retry finishes the cleanup. A real unmount
	// hides the NFS files; the fake leaves them, so drop the stand-in here.
	fm.UnmountFunc = nil
	if err := os.Remove(data); err != nil {
		t.Fatal(err)
	}
	if _, err := ns.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "pvc-1", TargetPath: target}); err != nil {
		t.Fatalf("retry unpublish: %v", err)
	}
	if _, ok := mountedAt(fm, group); ok {
		t.Fatal("group mount kept after retry")
	}
}

func TestReadOnlyPublishNeverEndsUpWritable(t *testing.T) {
	fm := mount.NewFakeMounter(nil)
	ns, root := newTestNodeServer(t, failingRemount{fm}, nfsPod("a", nil))
	ctx := context.Background()
	target := filepath.Join(root, "t1")
	req := nfsPublishReq("pvc-1", target, "a", false, csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY)

	if _, err := ns.NodePublishVolume(ctx, req); err == nil {
		t.Fatal("publish succeeded although the read-only remount failed")
	}
	if _, ok := mountedAt(fm, target); ok {
		t.Fatal("writable bind left on the target after a failed read-only remount")
	}

	// With a working mounter the retry produces a read-only bind.
	ns.Mounter.Interface = fm
	if _, err := ns.NodePublishVolume(ctx, req); err != nil {
		t.Fatalf("retry publish: %v", err)
	}
	if !ns.mountIsReadOnly(target) {
		t.Fatal("target is not read-only")
	}
}

func TestReadOnlyPublishRedoesWritableLeftover(t *testing.T) {
	fm := mount.NewFakeMounter(nil)
	ns, root := newTestNodeServer(t, fm, nfsPod("a", nil))
	target := filepath.Join(root, "t1")
	if err := os.MkdirAll(target, 0750); err != nil {
		t.Fatal(err)
	}
	// A rw bind from an earlier, half-finished attempt.
	if err := fm.Mount("/somewhere", target, "", []string{"bind"}); err != nil {
		t.Fatal(err)
	}
	if err := ns.publishBind("/src", target, true); err != nil {
		t.Fatalf("publishBind: %v", err)
	}
	if !ns.mountIsReadOnly(target) {
		t.Fatal("leftover rw bind was accepted as a read-only publish")
	}
}

func TestPublishRemountsBrokenGroupMount(t *testing.T) {
	fm := mount.NewFakeMounter(nil)
	ns, root := newTestNodeServer(t, fm, nfsPod("a", nil))
	ctx := context.Background()
	group := ns.getNfsGroupPath("pvc-1", nil)
	if err := os.MkdirAll(group, 0750); err != nil {
		t.Fatal(err)
	}
	if err := fm.Mount("10.0.0.1:/pool/pvc-1", group, "nfs", nil); err != nil {
		t.Fatal(err)
	}
	fm.MountCheckErrors = map[string]error{group: syscall.ESTALE}

	req := nfsPublishReq("pvc-1", filepath.Join(root, "t1"), "a", false,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)
	if _, err := ns.NodePublishVolume(ctx, req); err != nil {
		t.Fatalf("publish over a stale group mount: %v", err)
	}
	unmounts, mounts := 0, 0
	for _, a := range fm.GetLog() {
		if a.Target != group {
			continue
		}
		switch a.Action {
		case mount.FakeActionUnmount:
			unmounts++
		case mount.FakeActionMount:
			mounts++
		}
	}
	if unmounts != 1 || mounts != 2 {
		t.Fatalf("group unmounts=%d mounts=%d, want 1 and 2 (stale mount replaced)", unmounts, mounts)
	}
}

func TestPublishUsesCapabilityMountFlags(t *testing.T) {
	fm := mount.NewFakeMounter(nil)
	ns, root := newTestNodeServer(t, fm, nfsPod("a", map[string]string{"flexa.io/mountOptions": "hard, nfsvers=3"}))
	req := nfsPublishReq("pvc-1", filepath.Join(root, "t1"), "a", false,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER)
	req.VolumeCapability.GetMount().MountFlags = []string{"nfsvers=3", "noatime"}

	if _, err := ns.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatalf("publish: %v", err)
	}
	want := []string{"nfsvers=3", "noatime", "hard"}
	mp, ok := mountedAt(fm, ns.getNfsGroupPath("pvc-1", want))
	if !ok {
		t.Fatal("group mount not found under the merged options")
	}
	if !slices.Equal(mp.Opts, want) {
		t.Fatalf("mount options = %v, want %v", mp.Opts, want)
	}
}

func TestPublishRejectsBlockAndMissingFields(t *testing.T) {
	ns, root := newTestNodeServer(t, mount.NewFakeMounter(nil))
	ctx := context.Background()
	block := nfsPublishReq("pvc-1", filepath.Join(root, "t"), "a", false,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)
	block.VolumeCapability.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
	noCap := nfsPublishReq("pvc-1", filepath.Join(root, "t"), "a", false,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)
	noCap.VolumeCapability = nil
	noID := nfsPublishReq("", filepath.Join(root, "t"), "a", false,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER)

	for name, r := range map[string]*csi.NodePublishVolumeRequest{"block": block, "no capability": noCap, "no id": noID} {
		if _, err := ns.NodePublishVolume(ctx, r); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
}

func TestVolumeLockAbortsConcurrentRequest(t *testing.T) {
	l := newVolumeLocks()
	release, err := l.acquire("pvc-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.acquire("pvc-1"); status.Code(err) != codes.Aborted {
		t.Fatalf("second acquire = %v, want Aborted", err)
	}
	if r, err := l.acquire("pvc-2"); err != nil {
		t.Fatalf("other volume blocked: %v", err)
	} else {
		r()
	}
	release()
	if r, err := l.acquire("pvc-1"); err != nil {
		t.Fatalf("acquire after release: %v", err)
	} else {
		r()
	}
}
