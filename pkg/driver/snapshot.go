/*
Copyright 2026 Gluesys FlexA Inc.

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
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gluesys/flexa-csi/pkg/flexa/service"
	"github.com/gluesys/flexa-csi/pkg/flexa/webapi"
)

// Snapshots are ZFS only. The snapshot ID is "zfs:<profile>:<pool>:<volume>@<snap>":
// the source PV gives the proxy, and the ID alone still finds the right proxy
// profile and names the snapshot when that PV is gone.
const snapshotIDPrefix = "zfs:"

// backendSnapName must match provisiond's snapshot name rule.
var backendSnapName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func snapshotID(profile, pool, volume, snap string) string {
	return fmt.Sprintf("%s%s:%s:%s@%s", snapshotIDPrefix, profile, pool, volume, snap)
}

type snapshotRef struct {
	profile, pool, volume, snap string
}

// parseSnapshotID splits a snapshot ID made by snapshotID.
func parseSnapshotID(id string) (snapshotRef, bool) {
	rest, found := strings.CutPrefix(id, snapshotIDPrefix)
	if !found {
		return snapshotRef{}, false
	}
	var r snapshotRef
	if r.profile, rest, found = strings.Cut(rest, ":"); !found {
		return snapshotRef{}, false
	}
	if r.pool, rest, found = strings.Cut(rest, ":"); !found {
		return snapshotRef{}, false
	}
	r.volume, r.snap, found = strings.Cut(rest, "@")
	if !found || r.pool == "" || r.volume == "" || !backendSnapName.MatchString(r.snap) {
		return snapshotRef{}, false
	}
	return r, true
}

// snapName turns a CSI snapshot name (external-snapshotter: "snapshot-<uid>")
// into a ZFS snapshot name; other names are hashed so they stay valid and stable.
func snapName(name string) string {
	if backendSnapName.MatchString(name) {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return "snapshot-" + hex.EncodeToString(sum[:16])
}

func (cs *ControllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	name, srcID := req.GetName(), req.GetSourceVolumeId()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot name is required")
	}
	if srcID == "" {
		return nil, status.Error(codes.InvalidArgument, "source volume id is required")
	}

	attrs, found, err := cs.volumeAttributes(ctx, srcID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "source volume %s does not exist", srcID)
	}
	if fs := volumeFsFromAttrs(attrs); fs != "zfs" {
		return nil, status.Errorf(codes.InvalidArgument, "snapshots are supported for zfs volumes only, not %s", fs)
	}
	pool, err := backendKey("zfs", attrs)
	if err != nil {
		return nil, err
	}
	profile := strings.TrimSpace(attrs[volAttrProxyProfile])
	if strings.ContainsAny(profile, ":@") {
		return nil, status.Errorf(codes.InvalidArgument, "proxy profile %q cannot be put in a snapshot id", profile)
	}
	reqService, err := cs.serviceForAttrs(attrs)
	if err != nil {
		return nil, err
	}

	// Same lock as delete/expand of the source volume.
	release, err := cs.locks.acquire(srcID)
	if err != nil {
		return nil, err
	}
	defer release()

	snap := snapName(name)
	created, err := reqService.CreateZfsSnapshot(pool, srcID, snap)
	if webapi.IsMissing(err, "Volume("+srcID+")") {
		return nil, status.Errorf(codes.NotFound, "source volume %s does not exist: %v", srcID, err)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create snapshot %s of %s: %v", name, srcID, err)
	}

	// restoreSize: a restored volume must be at least as large as the source was.
	var size int64
	if mb, err := strconv.ParseInt(created.SizeMb, 10, 64); err == nil && mb > 0 {
		size = mb << 20
	} else if vol, err := reqService.GetVolume("zfs", pool, srcID); err != nil {
		log.Warnf("CreateSnapshot: size of %s unknown: %v", srcID, err)
	} else if vol != nil {
		size = vol.Size
	}

	createdAt := time.Now()
	if sec, err := strconv.ParseInt(created.CreateTime, 10, 64); err == nil {
		createdAt = time.Unix(sec, 0)
	}
	log.Infof("CreateSnapshot: %s -> %s/%s@%s", name, pool, srcID, snap)
	return &csi.CreateSnapshotResponse{
		Snapshot: &csi.Snapshot{
			SnapshotId:     snapshotID(profile, pool, srcID, snap),
			SourceVolumeId: srcID,
			CreationTime:   timestamppb.New(createdAt),
			ReadyToUse:     true,
			SizeBytes:      size,
		},
	}, nil
}

func (cs *ControllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	id := req.GetSnapshotId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot id is required")
	}
	ref, ok := parseSnapshotID(id)
	if !ok {
		// Not an ID this driver makes, so there is nothing of ours to delete.
		log.Warnf("DeleteSnapshot: unknown snapshot id %q; treating as deleted", id)
		return &csi.DeleteSnapshotResponse{}, nil
	}
	volume := ref.volume

	attrs, found, err := cs.volumeAttributes(ctx, volume)
	if err != nil {
		return nil, err
	}
	var reqService *service.FlexAService
	if found {
		if reqService, err = cs.serviceForAttrs(attrs); err != nil {
			return nil, err
		}
	} else {
		// The source PV is gone (its volume is kept while snapshots exist, but the
		// PV object may have been removed by hand): use the profile in the ID.
		proxy, err := cs.Driver.SelectProxy(ref.profile)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		reqService = service.NewFlexAService()
		reqService.SetFep(proxy)
	}

	release, err := cs.locks.acquire(volume)
	if err != nil {
		return nil, err
	}
	defer release()

	if err := reqService.DeleteZfsSnapshot(ref.pool, volume, ref.snap); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete snapshot %s: %v", id, err)
	}
	log.Infof("DeleteSnapshot: %s deleted", id)
	return &csi.DeleteSnapshotResponse{}, nil
}

func (cs *ControllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListSnapshots is not supported")
}

// restoreSource checks a CreateVolume content source. Only a snapshot in the
// same ZFS pool of the same proxy can be restored (the copy stays in the pool).
func restoreSource(src *csi.VolumeContentSource, fs, poolName, profile string) (volume, snap string, err error) {
	if src == nil {
		return "", "", nil
	}
	s := src.GetSnapshot()
	if s == nil {
		return "", "", status.Errorf(codes.InvalidArgument,
			"volume content source %T is not supported; restore from a VolumeSnapshot", src.GetType())
	}
	if fs != "zfs" {
		return "", "", status.Errorf(codes.InvalidArgument, "snapshot restore is supported for zfs only, not %s", fs)
	}
	ref, ok := parseSnapshotID(s.GetSnapshotId())
	if !ok {
		return "", "", status.Errorf(codes.NotFound, "snapshot %s does not exist", s.GetSnapshotId())
	}
	if ref.pool != poolName || ref.profile != profile {
		return "", "", status.Errorf(codes.InvalidArgument,
			"snapshot %s is in pool %s (proxy profile %q); the StorageClass uses pool %s (profile %q)",
			s.GetSnapshotId(), ref.pool, ref.profile, poolName, profile)
	}
	return ref.volume, ref.snap, nil
}

// sourceShareDir is the exported directory of a snapshot's source volume: the
// last element of its PV's baseDir. Without the PV, the directory this driver
// gives every volume (its share name, the volume ID) is assumed.
func (cs *ControllerServer) sourceShareDir(ctx context.Context, srcVolume string) (string, error) {
	attrs, found, err := cs.volumeAttributes(ctx, srcVolume)
	if err != nil {
		return "", err
	}
	if found {
		if base := strings.TrimSpace(attrs["baseDir"]); base != "" {
			return path.Base(base), nil
		}
	}
	return webapi.ZfsNfsShareName(srcVolume), nil
}
