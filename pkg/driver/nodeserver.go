/*
Copyright 2025 Gluesys FlexA Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package driver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/mount-utils"

	"github.com/gluesys/flexa-csi/pkg/flexa/service"
	"github.com/gluesys/flexa-csi/pkg/flexa/webapi"
	"github.com/gluesys/flexa-csi/pkg/interfaces"
	"github.com/gluesys/flexa-csi/pkg/models"
)

const (
	defaultNfsMetaRoot  = "/var/lib/flexa/meta"
	defaultNfsGroupRoot = "/mnt/flexa/global"
)

type NodeServer struct {
	csi.UnimplementedNodeServer
	Driver     *Driver
	Mounter    *mount.SafeFormatAndMount
	FlxService interfaces.FlexAService
	Client     clientset.Interface

	locks *volumeLocks
	// metaRoot and groupRoot are the NFS bookkeeping directories; tests point
	// them at a temporary directory.
	metaRoot  string
	groupRoot string
}

// isReadOnlyAccessMode reports whether the requested access mode is a read-only mode
// (SINGLE_NODE_READER_ONLY / MULTI_NODE_READER_ONLY, i.e. a ROX PVC). The driver
// enforces a read-only mount for these regardless of the request's readonly flag.
func isReadOnlyAccessMode(vc *csi.VolumeCapability) bool {
	if vc == nil {
		return false
	}
	switch vc.GetAccessMode().GetMode() {
	case csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY:
		return true
	}
	return false
}

// checkVolumeID rejects IDs that would leave the node's bookkeeping directories:
// the ID is used as a path element of the meta and group mount paths.
func checkVolumeID(id string) error {
	if id == "" {
		return status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if strings.ContainsAny(id, "/\x00") || id == "." || id == ".." {
		return status.Errorf(codes.InvalidArgument, "invalid volume ID %q", id)
	}
	return nil
}

// checkNodeCapability rejects capabilities a file-system volume cannot serve.
func checkNodeCapability(vc *csi.VolumeCapability) error {
	if vc == nil {
		return status.Error(codes.InvalidArgument, "Volume capability missing in request")
	}
	if vc.GetBlock() != nil {
		return status.Error(codes.InvalidArgument, "block access is not supported")
	}
	return nil
}

func (ns *NodeServer) metaDir(volId string) string {
	root := ns.metaRoot
	if root == "" {
		root = defaultNfsMetaRoot
	}
	return filepath.Join(root, volId)
}

func (ns *NodeServer) metaFile(volId string, targetPath string) string {
	targetHash := sha256.Sum256([]byte(targetPath))
	encode := base64.RawURLEncoding.EncodeToString(targetHash[:])
	return filepath.Join(ns.metaDir(volId), encode+".json")
}

func (ns *NodeServer) saveNfsVolMeta(volId string, targetPath string, stagingGroupPath string, source string, options []string) error {
	metaFile := ns.metaFile(volId, targetPath)

	metaData := &models.NfsVolMeta{
		VolumeId:         volId,
		Source:           source,
		TargetPath:       targetPath,
		StagingGroupPath: stagingGroupPath,
		MountOptions:     options,
	}

	jsonData, err := json.MarshalIndent(metaData, "", "  ")
	if err != nil {
		return err
	}

	log.Infof("Save Volume Meta Path : %s", metaFile)
	log.Infof("Save Volume Meta : %v", metaData)

	if err := os.MkdirAll(ns.metaDir(volId), 0755); err != nil {
		return err
	}

	// Write a temporary file and rename it, so a crash never leaves a
	// truncated meta behind for unpublish to trip over.
	tmp, err := os.CreateTemp(ns.metaDir(volId), ".meta-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(jsonData); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), metaFile); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func (ns *NodeServer) loadNfsVolMeta(volId string, targetPath string) (*models.NfsVolMeta, error) {
	metaFile := ns.metaFile(volId, targetPath)
	jsonData, err := os.ReadFile(metaFile)
	if err != nil {
		return nil, err
	}

	var data models.NfsVolMeta

	log.Infof("Load Volume Meta Path : %v", metaFile)

	if err := json.Unmarshal(jsonData, &data); err != nil {
		return nil, err
	}

	log.Infof("Load Volume Meta : %v", data)

	return &data, nil
}

func (ns *NodeServer) deleteNfsVolMeta(volId string, targetPath string) error {
	metaFile := ns.metaFile(volId, targetPath)

	if err := os.Remove(metaFile); err != nil && !os.IsNotExist(err) {
		return err
	}

	log.Infof("Delete Volume Meta Path : %s", metaFile)

	// The directory goes once its last target is gone; Remove fails while it is not empty.
	_ = os.Remove(ns.metaDir(volId))

	return nil
}

// otherGroupUsers counts the other published targets of volId that still sit on
// groupPath. The meta files are the record of which targets were bound from
// which group mount; a target that is no longer mounted does not count.
func (ns *NodeServer) otherGroupUsers(volId string, targetPath string, groupPath string) (int, error) {
	entries, err := os.ReadDir(ns.metaDir(volId))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	users := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(ns.metaDir(volId), e.Name()))
		if err != nil {
			return 0, err
		}
		var m models.NfsVolMeta
		if err := json.Unmarshal(raw, &m); err != nil {
			log.Warnf("NodeServer : skip unreadable meta %s: %v", e.Name(), err)
			continue
		}
		if m.TargetPath == targetPath || m.StagingGroupPath != groupPath {
			continue
		}
		mounted, err := ns.Mounter.Interface.IsMountPoint(m.TargetPath)
		if err != nil && !os.IsNotExist(err) {
			// A broken mount is still a user until its own unpublish runs.
			mounted = true
		}
		if mounted {
			users++
		}
	}
	return users, nil
}

// releaseGroup unmounts groupPath once no target other than targetPath uses it.
func (ns *NodeServer) releaseGroup(volId string, targetPath string, groupPath string) error {
	if groupPath == "" {
		return nil
	}
	users, err := ns.otherGroupUsers(volId, targetPath, groupPath)
	if err != nil {
		return fmt.Errorf("failed to read volume meta: %w", err)
	}
	log.Infof("NodeServer : group(%s) other users -> %d", groupPath, users)
	if users > 0 {
		return nil
	}

	log.Infof("NodeServer : Unmount stagingGroupPath(%s)", groupPath)
	if err := mount.CleanupMountPoint(groupPath, ns.Mounter.Interface, true); err != nil {
		return fmt.Errorf("failed to unmount group path %s: %w", groupPath, err)
	}
	// Drop the now-empty <volume>/<options-hash> parents too; Remove leaves
	// anything that is not empty alone.
	hashDir := filepath.Dir(groupPath)
	_ = os.Remove(hashDir)
	_ = os.Remove(filepath.Dir(hashDir))
	return nil
}

// releaseUnusedGroups releases every group mount of volId (current layout) that
// no readable meta of a still-mounted target refers to.
func (ns *NodeServer) releaseUnusedGroups(volId string) error {
	root := ns.groupRoot
	if root == "" {
		root = defaultNfsGroupRoot
	}
	groups, err := filepath.Glob(filepath.Join(root, volId, "*", "mount"))
	if err != nil {
		return err
	}
	for _, g := range groups {
		if err := ns.releaseGroup(volId, "", g); err != nil {
			return err
		}
	}
	return nil
}

// mountOptions returns the NFS mount options: the capability's mount_flags
// (StorageClass/PV mountOptions) followed by the pod annotation.
func (ns *NodeServer) mountOptions(ctx context.Context, vc *csi.VolumeCapability, podName string, podNS string) ([]string, error) {
	var options []string
	for _, o := range vc.GetMount().GetMountFlags() {
		if o = strings.TrimSpace(o); o != "" {
			options = append(options, o)
		}
	}

	pod, err := ns.Driver.K8sClient.CoreV1().Pods(podNS).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	for _, o := range strings.Split(pod.Annotations["flexa.io/mountOptions"], ",") {
		if o = strings.TrimSpace(o); o != "" && !slices.Contains(options, o) {
			options = append(options, o)
		}
	}
	return options, nil
}

func (ns *NodeServer) getServiceVIP(ctx context.Context, podName string, podNS string) (string, error) {
	pod, err := ns.Driver.K8sClient.CoreV1().Pods(podNS).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	svip := pod.Annotations["flexa.io/serviceVIP"]

	return svip, nil
}

// getNfsGroupPath is the node-wide NFS mount one volume's targets bind from.
// It is keyed by volume ID (not PVC name), so a PVC re-created under the same
// name never lands on a leftover mount of the old volume.
func (ns *NodeServer) getNfsGroupPath(volumeId string, options []string) string {
	sorted := slices.Clone(options)
	sort.Strings(sorted)
	hashOpts := sha256.Sum256([]byte(strings.Join(sorted, "_")))
	encode := base64.RawURLEncoding.EncodeToString(hashOpts[:])

	root := ns.groupRoot
	if root == "" {
		root = defaultNfsGroupRoot
	}
	return filepath.Join(root, volumeId, encode, "mount")
}

// prepareMountPoint makes path ready to be mounted on. It reports whether a
// mount is already there; a broken (stale NFS, disconnected Lustre) mount is
// taken down first so the caller mounts it afresh.
func (ns *NodeServer) prepareMountPoint(path string) (bool, error) {
	mounted, err := ns.Mounter.Interface.IsMountPoint(path)
	switch {
	case err == nil:
		return mounted, nil
	case os.IsNotExist(err):
		return false, os.MkdirAll(path, 0750)
	case mount.IsCorruptedMnt(err):
		log.Warnf("NodeServer : %s is a broken mount (%v); unmounting it", path, err)
		if uerr := ns.Mounter.Interface.Unmount(path); uerr != nil {
			return false, fmt.Errorf("unmount broken mount %s: %w", path, uerr)
		}
		return false, nil
	}
	return false, err
}

// mountIsReadOnly reports whether the topmost mount on path is read-only.
func (ns *NodeServer) mountIsReadOnly(path string) bool {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	mps, err := ns.Mounter.Interface.List()
	if err != nil {
		return false
	}
	ro := false
	for _, mp := range mps {
		if mp.Path == path {
			ro = slices.Contains(mp.Opts, "ro")
		}
	}
	return ro
}

// publishBind binds source onto target, read-only when asked. An existing
// target mount counts as done only if its access matches: a writable bind left
// by a failed earlier attempt is redone so a read-only publish never ends up rw.
func (ns *NodeServer) publishBind(source string, target string, readonly bool) error {
	mounted, err := ns.prepareMountPoint(target)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to prepare target path %s: %v", target, err)
	}
	if mounted {
		if !readonly || ns.mountIsReadOnly(target) {
			return nil
		}
		log.Warnf("NodeServer : %s is mounted read-write but must be read-only; remounting", target)
		if err := ns.Mounter.Interface.Unmount(target); err != nil {
			return status.Errorf(codes.Internal, "failed to unmount %s: %v", target, err)
		}
	}

	// mount-utils binds first and then remounts "bind,remount,..." with the rest,
	// so "ro" applies to this bind only, not to the shared source mount.
	opts := []string{"bind", "rprivate"}
	if readonly {
		opts = append(opts, "ro")
	}
	if err := ns.Mounter.Interface.Mount(source, target, "", opts); err != nil {
		// The bind may have succeeded and only the remount failed; leaving it
		// would hand a writable mount to the next retry.
		if m, _ := ns.Mounter.Interface.IsMountPoint(target); m {
			if uerr := ns.Mounter.Interface.Unmount(target); uerr != nil {
				log.Errorf("NodeServer : failed to undo bind on %s: %v", target, uerr)
			}
		}
		return status.Errorf(codes.Internal, "bind mount %s -> %s failed: %v", source, target, err)
	}
	return nil
}

func (ns *NodeServer) mountNfsGroup(source string, stagingGroupPath string, options []string) error {
	mounted, err := ns.prepareMountPoint(stagingGroupPath)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	if mounted {
		log.Infof("mountNfsGroup : %s is already mounted", stagingGroupPath)
		return nil
	}

	log.Infof("mountNFsGroup: mountGroupPath(%s) options(%v)", stagingGroupPath, options)
	err = ns.Mounter.Mount(source, stagingGroupPath, "nfs", options)
	if err != nil {
		if os.IsPermission(err) {
			return status.Error(codes.PermissionDenied, err.Error())
		}
		if strings.Contains(err.Error(), "Invalid argument") {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		return status.Error(codes.Internal, err.Error())
	}

	// The export root belongs to the server: with root_squash or a read-only
	// mount the chmod is refused, and the mount is usable all the same.
	if err := chmodIfPermissionMismatch(stagingGroupPath, os.FileMode(0750)); err != nil {
		log.Warnf("mountNfsGroup : chmod %s: %v", stagingGroupPath, err)
	}

	log.Infof("NFS Group Mount %s on %s succeeded", source, stagingGroupPath)

	return nil
}

// Lustre is mounted in the host mount namespace so it survives plugin restarts.
// The command is bound to the request context: kubelet gives up on the RPC at
// its deadline, and so does the mount helper.
func lustreMountViaHost(ctx context.Context, source, target string, options []string) error {
	args := []string{"--target", "1", "--mount", "--", "mount", "-t", "lustre"}
	for _, o := range options {
		args = append(args, "-o", o)
	}
	args = append(args, source, target)

	log.Infof("lustreMountViaHost: nsenter %v", args)
	output, err := exec.CommandContext(ctx, "nsenter", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nsenter lustre mount failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func lustreUmountViaHost(ctx context.Context, target string) error {
	args := []string{"--target", "1", "--mount", "--", "umount", target}

	log.Infof("lustreUmountViaHost: nsenter %v", args)
	output, err := exec.CommandContext(ctx, "nsenter", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nsenter lustre umount failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (ns *NodeServer) resolveProxyFEP(volCtx map[string]string) (*webapi.FEP, error) {
	proxy, ok := proxyFromVolumeAttributes(volCtx)
	if !ok {
		proxyProfile := strings.TrimSpace(volCtx[volAttrProxyProfile])
		p, err := ns.Driver.SelectProxy(proxyProfile)
		if err != nil {
			return nil, err
		}
		proxy = p
	}
	return &webapi.FEP{Ip: proxy.Host, Port: proxy.Port, MountIP: proxy.MountIP}, nil
}

func (ns *NodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	volumeId := req.GetVolumeId()
	stagingPath := req.GetStagingTargetPath()
	if err := checkVolumeID(volumeId); err != nil {
		return nil, err
	}
	if stagingPath == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path missing in request")
	}
	if err := checkNodeCapability(req.GetVolumeCapability()); err != nil {
		return nil, err
	}

	// NFS volumes share a node-wide group mount set up in NodePublishVolume.
	if req.VolumeContext["protocol"] != "lustre" {
		return &csi.NodeStageVolumeResponse{}, nil
	}

	clusterName := req.VolumeContext["clusterName"]
	if clusterName == "" {
		return nil, status.Error(codes.InvalidArgument, "clusterName missing in volume context for Lustre mount")
	}

	release, err := ns.locks.acquire(volumeId)
	if err != nil {
		return nil, err
	}
	defer release()

	mounted, err := ns.Mounter.Interface.IsMountPoint(stagingPath)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		if mkErr := os.MkdirAll(stagingPath, 0750); mkErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to create staging path %s: %v", stagingPath, mkErr)
		}
	case mount.IsCorruptedMnt(err):
		log.Warnf("NodeStageVolume: %s is a broken mount (%v); remounting", stagingPath, err)
		if uerr := lustreUmountViaHost(ctx, stagingPath); uerr != nil {
			return nil, status.Errorf(codes.Internal, "failed to unmount broken staging path %s: %v", stagingPath, uerr)
		}
	default:
		return nil, status.Errorf(codes.Internal, "failed to check mount point %s: %v", stagingPath, err)
	}
	if mounted {
		log.Infof("NodeStageVolume: %s is already mounted, skipping", stagingPath)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	fep, err := ns.resolveProxyFEP(req.VolumeContext)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resolve proxy: %v", err)
	}

	nids, err := fep.LustreMgsNid()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get MGS NID: %v", err)
	}

	mgsSpec := strings.Join(nids, ":")
	source := fmt.Sprintf("%s:/%s/%s", mgsSpec, clusterName, volumeId)

	log.Infof("NodeStageVolume: mounting Lustre source(%s) on stagingPath(%s)", source, stagingPath)
	if err := lustreMountViaHost(ctx, source, stagingPath, req.GetVolumeCapability().GetMount().GetMountFlags()); err != nil {
		return nil, status.Errorf(codes.Internal, "Lustre mount failed: source=%s target=%s err=%v", source, stagingPath, err)
	}

	log.Infof("NodeStageVolume: Lustre mount succeeded source(%s) stagingPath(%s)", source, stagingPath)
	return &csi.NodeStageVolumeResponse{}, nil
}

func (ns *NodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	volumeId := req.GetVolumeId()
	stagingPath := req.GetStagingTargetPath()
	if err := checkVolumeID(volumeId); err != nil {
		return nil, err
	}
	if stagingPath == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path missing in request")
	}

	release, err := ns.locks.acquire(volumeId)
	if err != nil {
		return nil, err
	}
	defer release()

	mounted, err := ns.Mounter.IsMountPoint(stagingPath)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		return &csi.NodeUnstageVolumeResponse{}, nil
	case mount.IsCorruptedMnt(err):
		log.Warnf("NodeUnstageVolume: %s is a broken mount (%v); unmounting", stagingPath, err)
		mounted = true
	default:
		return nil, status.Errorf(codes.Internal, "failed to check mount point %s: %v", stagingPath, err)
	}

	if mounted {
		log.Infof("NodeUnstageVolume: unmounting stagingPath(%s)", stagingPath)
		if err := lustreUmountViaHost(ctx, stagingPath); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to unmount staging path %s: %v", stagingPath, err)
		}
	}

	// Remove only the empty directory. Never RemoveAll: if the unmount did not
	// really take effect, it would delete the volume's data.
	if err := os.Remove(stagingPath); err != nil && !os.IsNotExist(err) {
		log.Warnf("NodeUnstageVolume: failed to remove staging path %s: %v", stagingPath, err)
	}

	log.Infof("NodeUnstageVolume: successfully unmounted stagingPath(%s)", stagingPath)
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *NodeServer) nodePublishLustre(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	stagingPath := req.GetStagingTargetPath()
	targetPath := req.GetTargetPath()
	volumeId := req.GetVolumeId()

	if stagingPath == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path missing in request")
	}

	log.Infof("NodePublishVolume[lustre]: volumeId(%s) stagingPath(%s) targetPath(%s)", volumeId, stagingPath, targetPath)

	readonly := req.GetReadonly() || isReadOnlyAccessMode(req.GetVolumeCapability())
	if err := ns.publishBind(stagingPath, targetPath, readonly); err != nil {
		return nil, err
	}

	log.Infof("NodePublishVolume[lustre]: bind mount succeeded (%s) -> (%s) readonly=%t", stagingPath, targetPath, readonly)
	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *NodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volumeId, targetPath := req.GetVolumeId(), req.GetTargetPath()
	if err := checkVolumeID(volumeId); err != nil {
		return nil, err
	}
	if targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}
	if err := checkNodeCapability(req.GetVolumeCapability()); err != nil {
		return nil, err
	}

	release, err := ns.locks.acquire(volumeId)
	if err != nil {
		return nil, err
	}
	defer release()

	if req.VolumeContext["protocol"] == "lustre" {
		return ns.nodePublishLustre(ctx, req)
	}

	podName := req.VolumeContext["csi.storage.k8s.io/pod.name"]
	podNS := req.VolumeContext["csi.storage.k8s.io/pod.namespace"]
	stagingOptions, err := ns.mountOptions(ctx, req.GetVolumeCapability(), podName, podNS)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	svip, err := ns.getServiceVIP(ctx, podName, podNS)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if strings.TrimSpace(svip) == "" {
		if v := req.VolumeContext["vip"]; v != "" {
			svip = v
		}
	}
	if strings.TrimSpace(svip) == "" {
		return nil, status.Error(codes.InvalidArgument, "NFS server address missing: set flexa.io/serviceVIP on pod or ensure VolumeContext.vip is set")
	}

	readonly := req.GetReadonly() || slices.Contains(stagingOptions, "ro") || isReadOnlyAccessMode(req.GetVolumeCapability())

	// A repeated publish of a target that is still bound keeps the group it is
	// bound to (an older layout, or other options at the time); recording a new
	// group here would leave the real one behind at unpublish.
	if meta, err := ns.loadNfsVolMeta(volumeId, targetPath); err == nil && meta.StagingGroupPath != "" {
		if mounted, err := ns.Mounter.Interface.IsMountPoint(targetPath); err == nil && mounted {
			if err := ns.publishBind(meta.StagingGroupPath, targetPath, readonly); err != nil {
				return nil, err
			}
			log.Infof("NodePublishVolume: %s already bound to %s", targetPath, meta.StagingGroupPath)
			return &csi.NodePublishVolumeResponse{}, nil
		}
	}

	source := fmt.Sprintf("%s:%s", svip, req.VolumeContext["baseDir"])
	stagingGroupPath := ns.getNfsGroupPath(volumeId, stagingOptions)

	log.Infof("volumeId(%s) targetPath(%s) stagingGroupPath(%s) options(%v)", volumeId, targetPath, stagingGroupPath, stagingOptions)

	if err := ns.mountNfsGroup(source, stagingGroupPath, stagingOptions); err != nil {
		return nil, err
	}

	// The meta is written before the bind so an unpublish always knows which
	// group mount this target belongs to.
	if err := ns.saveNfsVolMeta(volumeId, targetPath, stagingGroupPath, source, stagingOptions); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if err := ns.publishBind(stagingGroupPath, targetPath, readonly); err != nil {
		return nil, err
	}

	log.Infof("NodePublishVolume: Success Bind (%s) <---> (%s) readonly=%t", targetPath, stagingGroupPath, readonly)

	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *NodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	volumeId, targetPath := req.GetVolumeId(), req.GetTargetPath()
	if err := checkVolumeID(volumeId); err != nil {
		return nil, err
	}
	if targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "target path missing in request")
	}

	release, err := ns.locks.acquire(volumeId)
	if err != nil {
		return nil, err
	}
	defer release()

	// Unmounts the target (also a broken one) and removes the empty directory;
	// a missing or unmounted target is fine, so retries converge.
	if err := mount.CleanupMountPoint(targetPath, ns.Mounter.Interface, true); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount target path %s: %v", targetPath, err)
	}

	nfsVolMeta, metaErr := ns.loadNfsVolMeta(volumeId, targetPath)
	switch {
	case os.IsNotExist(metaErr):
		// No NFS meta: a Lustre bind (NodeUnstageVolume releases the Lustre mount)
		// or an NFS target whose cleanup already finished.
		log.Infof("NodeUnpublishVolume: target(%s) unmounted", targetPath)
		return &csi.NodeUnpublishVolumeResponse{}, nil
	case metaErr != nil:
		// An unreadable meta (e.g. left half-written by an older driver) must not
		// block teardown forever: drop it and release every group mount of this
		// volume that no remaining target uses.
		log.Warnf("NodeUnpublishVolume: dropping unreadable meta of %s: %v", targetPath, metaErr)
		if err := ns.deleteNfsVolMeta(volumeId, targetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to remove broken meta: %v", err)
		}
		if err := ns.releaseUnusedGroups(volumeId); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to release group mounts: %v", err)
		}
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}

	// The meta is removed last: if releasing the group mount fails, the retry
	// still finds it and finishes the job.
	if err := ns.releaseGroup(volumeId, targetPath, nfsVolMeta.StagingGroupPath); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	if err := ns.deleteNfsVolMeta(volumeId, targetPath); err != nil {
		log.Warnf("NodeServer : Delete Fail %s meta file: %v", volumeId, err)
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (ns *NodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	log.Debugf("Using default NodeGetInfo, ns.Driver.nodeID = [%s]", ns.Driver.nodeID)

	return &csi.NodeGetInfoResponse{
		NodeId: ns.Driver.nodeID,
	}, nil
}

func (ns *NodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: ns.Driver.nsCap,
	}, nil
}

func (ns *NodeServer) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	volumeId, volumePath := req.GetVolumeId(), req.GetVolumePath()
	if volumeId == "" || volumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "Invalid Argument")
	}

	// The local check comes first: it is cheap and NOT_FOUND must mean the path,
	// not a proxy that happened to be unreachable.
	isMount, err := ns.Mounter.IsMountPoint(volumePath)
	switch {
	case os.IsNotExist(err):
		return nil, status.Errorf(codes.NotFound, "Volume[%s] does not exist on the %s", volumeId, volumePath)
	case err != nil:
		return nil, status.Errorf(codes.Internal, "failed to check %s: %v", volumePath, err)
	case !isMount:
		return nil, status.Errorf(codes.NotFound, "Volume[%s] is not mounted on the %s", volumeId, volumePath)
	}

	attrs, found, err := lookupPVAttributes(ctx, ns.Driver.K8sClient, volumeId)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "failed to look up PV for volume %s: %v", volumeId, err)
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "PV for volume %s not found", volumeId)
	}

	fs := volumeFsFromAttrs(attrs)
	container, err := backendKey(fs, attrs)
	if err != nil {
		return nil, err
	}

	proxy, ok := proxyFromVolumeAttributes(attrs)
	if !ok {
		p, err := ns.Driver.SelectProxy(strings.TrimSpace(attrs[volAttrProxyProfile]))
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		proxy = p
	}
	reqService := service.NewFlexAService()
	reqService.SetFep(proxy)

	k8sVolume, err := reqService.GetVolume(fs, container, volumeId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to look up volume %s: %v", volumeId, err)
	}
	if k8sVolume == nil {
		return nil, status.Errorf(codes.NotFound, "Volume[%s] is not found", volumeId)
	}

	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Total:     k8sVolume.Size,
				Available: k8sVolume.Free,
				Used:      k8sVolume.Used,
				Unit:      csi.VolumeUsage_BYTES,
			},
		},
	}, nil
}

// NodeExpandVolume is not advertised in NodeGetCapabilities: the driver mounts NFS
// and Lustre, which have no node-side resize step, and ControllerExpandVolume always
// returns NodeExpansionRequired=false.
func (ns *NodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "NodeExpandVolume is not supported")
}
