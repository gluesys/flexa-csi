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
	"fmt"
	//"sort"
	//"strconv"
	"sort"
	"strconv"
	"strings"
	//"time"

	log "github.com/sirupsen/logrus"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	//"google.golang.org/protobuf/types/known/timestamppb"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gluesys/flexa-csi/pkg/flexa/common"
	"github.com/gluesys/flexa-csi/pkg/flexa/service"
	"github.com/gluesys/flexa-csi/pkg/flexa/webapi"
	"github.com/gluesys/flexa-csi/pkg/interfaces"
	"github.com/gluesys/flexa-csi/pkg/models"
	"github.com/gluesys/flexa-csi/pkg/utils"
)

const (
	volAttrProxyProfile = "proxyProfile"
	volAttrProxyIP      = "proxyIP"
	volAttrProxyPort    = "proxyPort"
	volAttrMountIP      = "mountIP"
	volAttrPvcQos       = "flexa.io/pvcQos"
)

func proxyFromVolumeAttributes(attrs map[string]string) (*common.ProxyInfo, bool) {
	if attrs == nil {
		return nil, false
	}
	ip := strings.TrimSpace(attrs[volAttrProxyIP])
	portStr := strings.TrimSpace(attrs[volAttrProxyPort])
	mountIP := strings.TrimSpace(attrs[volAttrMountIP])
	if ip == "" || portStr == "" {
		return nil, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port == 0 {
		return nil, false
	}
	return &common.ProxyInfo{Host: ip, Port: port, MountIP: mountIP}, true
}

type ControllerServer struct {
	csi.UnimplementedControllerServer
	Driver     *Driver
	FlxService interfaces.FlexAService
	locks      *volumeLocks
}

// defaultVolumeSize is used when the request names no size, and is the floor
// for small requests as long as limit_bytes allows it.
const defaultVolumeSize = 1 * utils.UNIT_GB

// getSizeByCapacityRange picks the size to provision. The proxy takes whole MiB,
// so required_bytes is rounded up; rounding down would hand out less than asked.
func getSizeByCapacityRange(capRange *csi.CapacityRange) (int64, error) {
	required := capRange.GetRequiredBytes()
	limit := capRange.GetLimitBytes()
	if required < 0 || limit < 0 {
		return 0, status.Error(codes.InvalidArgument, "Invalid input: capacity range must not be negative")
	}
	if 0 < limit && limit < required {
		return 0, status.Error(codes.InvalidArgument, "Invalid input: limitBytes is smaller than requiredBytes")
	}

	size := utils.BytesToMBCeil(required) * utils.UNIT_MB
	if size < defaultVolumeSize {
		size = defaultVolumeSize
	}
	if 0 < limit && limit < size {
		// The 1GiB floor or the MiB rounding overshot the limit: take the largest
		// whole MiB that still fits.
		size = (limit / utils.UNIT_MB) * utils.UNIT_MB
		if size == 0 || size < required {
			return 0, status.Errorf(codes.OutOfRange,
				"no whole-MiB size between requiredBytes %d and limitBytes %d", required, limit)
		}
	}
	return size, nil
}

// parseFs normalizes the StorageClass "fs" parameter ("" means zfs).
func parseFs(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "zfs":
		return "zfs", nil
	case "lustre":
		return "lustre", nil
	}
	return "", fmt.Errorf("unsupported fs %q: use zfs or lustre", v)
}

func (cs *ControllerServer) isVolumeAccessModeSupport(mode csi.VolumeCapability_AccessMode_Mode) bool {
	for _, accessMode := range cs.Driver.getVolumeCapabilityAccessModes() {
		if mode == accessMode.Mode {
			return true
		}
	}

	return false
}

// checkVolumeCapabilities reports why caps cannot be served: the volumes are
// NFS or Lustre file systems, so only mount access with a known mode works.
func (cs *ControllerServer) checkVolumeCapabilities(caps []*csi.VolumeCapability) error {
	if len(caps) == 0 {
		return fmt.Errorf("no volume capabilities are provided")
	}
	for _, c := range caps {
		if c == nil {
			return fmt.Errorf("volume capability is empty")
		}
		if c.GetBlock() != nil {
			return fmt.Errorf("block access is not supported")
		}
		if c.GetMount() == nil {
			return fmt.Errorf("volume capability has no access type")
		}
		if !cs.isVolumeAccessModeSupport(c.GetAccessMode().GetMode()) {
			return fmt.Errorf("access mode %v is not supported", c.GetAccessMode().GetMode())
		}
	}
	return nil
}

// volumeAttributes looks up the PV of volumeID. An API failure is UNAVAILABLE so
// the sidecar retries instead of acting on a missing answer.
func (cs *ControllerServer) volumeAttributes(ctx context.Context, volumeID string) (map[string]string, bool, error) {
	attrs, found, err := lookupPVAttributes(ctx, cs.Driver.K8sClient, volumeID)
	if err != nil {
		return nil, false, status.Errorf(codes.Unavailable, "failed to look up PV for volume %s: %v", volumeID, err)
	}
	return attrs, found, nil
}

// serviceForAttrs builds the proxy client for an existing volume: explicit
// attributes first, then its proxyProfile, then the legacy default.
func (cs *ControllerServer) serviceForAttrs(attrs map[string]string) (*service.FlexAService, error) {
	proxy, ok := proxyFromVolumeAttributes(attrs)
	if !ok {
		p, err := cs.Driver.SelectProxy(strings.TrimSpace(attrs[volAttrProxyProfile]))
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		proxy = p
	}
	reqService := service.NewFlexAService()
	reqService.SetFep(proxy)
	return reqService, nil
}

// backendKey returns the pool (zfs) or cluster (lustre) an existing volume lives in.
func backendKey(fs string, attrs map[string]string) (string, error) {
	if fs == "lustre" {
		if c := strings.TrimSpace(attrs["clusterName"]); c != "" {
			return c, nil
		}
		return "", status.Error(codes.InvalidArgument, "Missing clusterName in PV attributes")
	}
	if p := strings.TrimSpace(attrs["poolName"]); p != "" {
		return p, nil
	}
	return "", status.Error(codes.InvalidArgument, "Missing poolName in PV attributes")
}

func (cs *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	volName := req.GetName()
	if volName == "" {
		return nil, status.Errorf(codes.InvalidArgument, "No name is provided")
	}
	if err := cs.checkVolumeCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	// Snapshot restore and volume cloning are not supported. Without this guard the
	// requested content source is silently dropped and the caller gets an empty volume.
	if src := req.GetVolumeContentSource(); src != nil {
		return nil, status.Errorf(codes.InvalidArgument,
			"volume content source is not supported by this driver: %T", src.GetType())
	}

	sizeInByte, err := getSizeByCapacityRange(req.GetCapacityRange())
	if err != nil {
		return nil, err
	}

	params := req.GetParameters()
	fs, err := parseFs(params["fs"])
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	poolName := strings.TrimSpace(params["poolName"])
	clusterName := strings.TrimSpace(params["clusterName"])
	proxyProfile := strings.TrimSpace(params[volAttrProxyProfile])
	if fs == "zfs" && poolName == "" {
		return nil, status.Errorf(codes.InvalidArgument, "poolName is required for fs=zfs")
	}
	if fs == "lustre" && clusterName == "" {
		return nil, status.Errorf(codes.InvalidArgument, "clusterName is required for fs=lustre")
	}

	pvcName := params["csi.storage.k8s.io/pvc/name"]
	pvcNS := params["csi.storage.k8s.io/pvc/namespace"]

	log.Infof("PVC Name : %s PVC NameSpace : %s", pvcName, pvcNS)

	// Without --extra-create-metadata (or outside Kubernetes) there is no PVC to
	// read annotations from; StorageClass parameters and proxy defaults apply.
	var annotations map[string]string
	if pvcName != "" && pvcNS != "" {
		pvc, err := cs.Driver.K8sClient.CoreV1().PersistentVolumeClaims(pvcNS).Get(ctx, pvcName, metav1.GetOptions{})
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "Failed to get pvc %s/%s: %v", pvcNS, pvcName, err)
		}
		annotations = pvc.Annotations
	}

	// 볼륨 옵션: PVC 어노테이션이 있으면 그 값, 없으면 StorageClass 의 같은 이름 파라미터.
	opt := func(key string) string { return volumeOption(annotations, params, key) }

	optionSVS := opt("flexa.io/optionSVS")
	optionISS := opt("flexa.io/optionISS")
	optionComp := opt("flexa.io/optionComp")
	optionDedup := opt("flexa.io/optionDedup")

	log.Infof("Volume Options : optionSVS(%s), optionISS(%s), optionComp(%s), optionDedup(%s)", optionSVS, optionISS, optionComp, optionDedup)

	zoneOwner := pvcName
	if zoneOwner == "" {
		zoneOwner = volName
	}
	secureName := fmt.Sprintf("zone_%s", zoneOwner)
	secureAddr := opt("flexa.io/secureAddress")
	secureSub := opt("flexa.io/secureSubnet")

	log.Infof("Share Secure Zone : name(%s) Address(%s) Subnet(%s)", secureName, secureAddr, secureSub)

	nfsAccess := opt("flexa.io/nfsAccess")
	nfsNoRoot := opt("flexa.io/nfsNoRootSquashing")
	nfsInsecure := opt("flexa.io/nfsInsecure")

	log.Infof("NFS Secure : Access(%s) NoRootSquashing(%s) Insecure(%s)", nfsAccess, nfsNoRoot, nfsInsecure)

	pvcQos := opt(volAttrPvcQos)
	log.Infof("PVC QoS : %s", pvcQos)

	// CSI VolumeHandle and backend volume name: same as provisioner volume name (no k8s-csi- prefix).
	backingName := volName
	volumeID := volName

	poolOrCluster := poolName
	if fs == "lustre" {
		poolOrCluster = clusterName
	}

	spec := &models.CreateVolumeSpec{
		VolumeId:    volumeID,
		VolumeName:  backingName,
		PoolName:    poolOrCluster,
		Fs:          fs,
		Size:        sizeInByte,
		OptionISS:   optionISS,
		OptionSVS:   optionSVS,
		OptionComp:  optionComp,
		OptionDedup: optionDedup,
		SecureName:  secureName,
		SecureAddr:  secureAddr,
		SecureSub:   secureSub,
		NfsAccess:   nfsAccess,
		NfsNoRoot:   nfsNoRoot,
		NfsInsecure: nfsInsecure,
		PvcQos:      pvcQos,
	}

	proxy, err := cs.Driver.SelectProxy(proxyProfile)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	spec.SecureAddr, spec.SecureSub, err = defaultSecureZone(secureAddr, secureSub, proxy.MountIP)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	release, err := cs.locks.acquire(volumeID)
	if err != nil {
		return nil, err
	}
	defer release()

	reqService := service.NewFlexAService()
	reqService.SetFep(proxy)

	k8sVolume, err := reqService.GetVolume(fs, poolOrCluster, volName)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "Failed to look up volume %s: %v", volName, err)
	}
	if k8sVolume != nil {
		// Size 0 means the backend could not report a size (e.g. Lustre quota not
		// yet visible in Redis); do not treat that as a capacity conflict. Volumes
		// made before sizes were rounded up may be short by less than 1MiB.
		required := req.GetCapacityRange().GetRequiredBytes()
		if k8sVolume.Size > 0 && k8sVolume.Size < utils.BytesToMB(required)*utils.UNIT_MB {
			return nil, status.Errorf(codes.AlreadyExists,
				"Volume [%s] already exists but is smaller than requested capacity", volName)
		}
		if limit := req.GetCapacityRange().GetLimitBytes(); limit > 0 && k8sVolume.Size > limit {
			return nil, status.Errorf(codes.AlreadyExists,
				"Volume [%s] already exists but is larger than limitBytes", volName)
		}
	}
	switch {
	case k8sVolume == nil:
		if k8sVolume, err = reqService.CreateVolume(spec); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to create volume %s: %v", volName, err)
		}
	case fs == "zfs" && strings.TrimSpace(k8sVolume.BaseDir) == "":
		// The legacy proxy reports no share path, and an earlier attempt may have
		// stopped between dataset and share: make sure the share is there.
		log.Infof("Volume [%s] exists without a known share path; ensuring the share", volName)
		if k8sVolume, err = reqService.CreateZfsShare(spec); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to create share for volume %s: %v", volName, err)
		}
	default:
		log.Debugf("Volume [%s] already exists , backing name: [%s]", volName, k8sVolume.VolumeName)
	}

	capacity := k8sVolume.Size
	if capacity <= 0 {
		capacity = sizeInByte
	}

	volCtx := map[string]string{
		"vip":               k8sVolume.Vip,
		"poolName":          k8sVolume.PoolName,
		"baseDir":           k8sVolume.BaseDir,
		"shareName":         k8sVolume.ShareName,
		"fs":                fs,
		"clusterName":       clusterName,
		"protocol":          params["protocol"],
		"pvcName":           pvcName,
		"pvcNS":             pvcNS,
		volAttrProxyProfile: proxyProfile,
		volAttrProxyIP:      proxy.Host,
		volAttrProxyPort:    fmt.Sprintf("%d", proxy.Port),
		volAttrMountIP:      proxy.MountIP,
	}
	if pvcQos != "" {
		volCtx[volAttrPvcQos] = pvcQos
	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      k8sVolume.VolumeId,
			CapacityBytes: capacity,
			VolumeContext: volCtx,
		},
	}, nil
}

func (cs *ControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	volumeId := req.GetVolumeId()
	if volumeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "No volume id is provided")
	}

	attrs, found, err := cs.volumeAttributes(ctx, volumeId)
	if err != nil {
		return nil, err
	}
	if !found {
		// No PV carries this handle: nothing tells us where the volume lives.
		return &csi.DeleteVolumeResponse{}, nil
	}

	release, err := cs.locks.acquire(volumeId)
	if err != nil {
		return nil, err
	}
	defer release()

	fs := volumeFsFromAttrs(attrs)
	container, err := backendKey(fs, attrs)
	if err != nil {
		return nil, err
	}
	reqService, err := cs.serviceForAttrs(attrs)
	if err != nil {
		return nil, err
	}

	if fs == "lustre" {
		if err := reqService.DeleteVolume("lustre", container, volumeId, volumeId); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to DeleteVolume(%s), err: %v", volumeId, err)
		}
		return &csi.DeleteVolumeResponse{}, nil
	}

	shareName := webapi.LegacyZfsNfsShareName
	if s := strings.TrimSpace(attrs["shareName"]); s != "" {
		shareName = s
	}
	if err := reqService.DeleteVolume("zfs", container, shareName, volumeId); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to DeleteVolume(%s), err: %v", volumeId, err)
	}

	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *ControllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerPublishVolume is not supported; the driver runs with attachRequired=false")
}

func (cs *ControllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerUnpublishVolume is not supported; the driver runs with attachRequired=false")
}

func (cs *ControllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	volumeId, volCap := req.GetVolumeId(), req.GetVolumeCapabilities()
	if volumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if len(volCap) == 0 {
		return nil, status.Error(codes.InvalidArgument, "No volume capabilities are provided")
	}

	attrs, found, err := cs.volumeAttributes(ctx, volumeId)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "Volume[%s] does not exist", volumeId)
	}

	fs := volumeFsFromAttrs(attrs)
	container, err := backendKey(fs, attrs)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "Volume[%s] does not exist: %v", volumeId, err)
	}
	reqService, err := cs.serviceForAttrs(attrs)
	if err != nil {
		return nil, err
	}
	vol, err := reqService.GetVolume(fs, container, volumeId)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "Failed to look up volume %s: %v", volumeId, err)
	}
	if vol == nil {
		return nil, status.Errorf(codes.NotFound, "Volume[%s] does not exist", volumeId)
	}

	// An empty Confirmed means "not supported"; Message says why.
	if err := cs.checkVolumeCapabilities(volCap); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{Message: err.Error()}, nil
	}
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeContext:      req.GetVolumeContext(),
			VolumeCapabilities: volCap,
			Parameters:         req.GetParameters(),
			MutableParameters:  req.GetMutableParameters(),
		},
	}, nil
}

// capacityProfile is one client-info entry ListVolumes/GetCapacity iterate over.
// name is "" for the legacy default host/port entry (SelectProxy("") resolves it).
type capacityProfile struct {
	name string
	fs   string
}

// normalizeFs maps a client-info "fs" value to "zfs" or "lustre" ("" -> "zfs").
func normalizeFs(fs string) string {
	if strings.EqualFold(strings.TrimSpace(fs), "lustre") {
		return "lustre"
	}
	return "zfs"
}

// capacityProfiles returns the profiles in a stable order (sorted by name,
// legacy default last) so offset-based pagination is repeatable across calls.
func capacityProfiles(cfg *common.ClientInfoConfig) []capacityProfile {
	var out []capacityProfile
	if cfg == nil {
		return out
	}
	names := make([]string, 0, len(cfg.Profiles))
	for n := range cfg.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, capacityProfile{name: n, fs: normalizeFs(cfg.Profiles[n].Fs)})
	}
	if cfg.Default != nil {
		defFs := normalizeFs(cfg.Default.Fs)
		duplicate := false
		for _, n := range names {
			p := cfg.Profiles[n]
			if p.Host == cfg.Default.Host && p.Port == cfg.Default.Port && normalizeFs(p.Fs) == defFs {
				duplicate = true
				break
			}
		}
		// A legacy host/port entry that duplicates a named profile would list the
		// same volumes twice, so it is only added when it points somewhere new.
		if !duplicate {
			out = append(out, capacityProfile{name: "", fs: defFs})
		}
	}
	return out
}

func (cs *ControllerServer) capacityService(profile string) (*service.FlexAService, error) {
	proxy, err := cs.Driver.SelectProxy(profile)
	if err != nil {
		return nil, err
	}
	svc := service.NewFlexAService()
	svc.SetFep(proxy)
	return svc, nil
}

// listedVolume identifies one backend volume for ListVolumes. Sorting on
// (profile, container, volumeId) keeps the offset token stable between calls.
type listedVolume struct {
	profile   string
	fs        string
	container string // zfs pool or lustre cluster
	volumeId  string
}

// ListVolumes enumerates every volume visible through the client-info
// profiles. The starting token is a decimal offset into the sorted list; a
// token that cannot be parsed or lies past the end is rejected with ABORTED
// as the CSI spec requires. Capacity is looked up only for the returned page.
func (cs *ControllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	maxEntries := req.GetMaxEntries()
	if 0 > maxEntries {
		return nil, status.Error(codes.InvalidArgument, "Max entries can not be negative.")
	}

	startIdx := 0
	if tok := strings.TrimSpace(req.GetStartingToken()); tok != "" {
		n, err := strconv.Atoi(tok)
		if err != nil || n < 0 {
			return nil, status.Errorf(codes.Aborted, "invalid starting_token %q", tok)
		}
		startIdx = n
	}

	profiles := capacityProfiles(cs.Driver.GetClientInfoConfig())
	var all []listedVolume
	attempted, failed := 0, 0

	for _, prof := range profiles {
		attempted++
		svc, err := cs.capacityService(prof.name)
		if err != nil {
			failed++
			log.Errorf("ListVolumes: skip profile %q: %v", prof.name, err)
			continue
		}

		var containers []string
		var listVols func(string) ([]string, error)
		switch prof.fs {
		case "lustre":
			containers, err = svc.ListLustreClusters()
			listVols = svc.ListLustreVolumes
		default:
			containers, err = svc.ListPools()
			listVols = svc.ListVolumes
		}
		if err != nil {
			failed++
			log.Errorf("ListVolumes: profile %q list %s containers failed: %v", prof.name, prof.fs, err)
			continue
		}
		for _, c := range containers {
			vols, err := listVols(c)
			if err != nil {
				log.Errorf("ListVolumes: profile %q %s %s list failed: %v", prof.name, prof.fs, c, err)
				continue
			}
			for _, v := range vols {
				all = append(all, listedVolume{profile: prof.name, fs: prof.fs, container: c, volumeId: v})
			}
		}
	}

	if attempted > 0 && failed == attempted {
		return nil, status.Error(codes.Unavailable, "ListVolumes: every client-info profile failed")
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].profile != all[j].profile {
			return all[i].profile < all[j].profile
		}
		if all[i].container != all[j].container {
			return all[i].container < all[j].container
		}
		return all[i].volumeId < all[j].volumeId
	})

	if startIdx > len(all) {
		return nil, status.Errorf(codes.Aborted, "starting_token %d is past the end of the volume list (%d)", startIdx, len(all))
	}

	endIdx := len(all)
	if maxEntries > 0 && startIdx+int(maxEntries) < endIdx {
		endIdx = startIdx + int(maxEntries)
	}

	entries := make([]*csi.ListVolumesResponse_Entry, 0, endIdx-startIdx)
	for i := startIdx; i < endIdx; i++ {
		lv := all[i]
		// capacity_bytes 0 means "unknown" in the CSI spec; a failed info call
		// must not drop the entry or later offsets would shift.
		var capacityBytes int64
		if svc, err := cs.capacityService(lv.profile); err == nil {
			switch lv.fs {
			case "lustre":
				if info, err := svc.LustreVolumeInfo(lv.container, lv.volumeId); err == nil {
					capacityBytes = utils.MBToBytes(info.Quota.LimitMb)
				}
			default:
				// zfs volume_info reports total/used/free in MB (see service.GetVolume).
				if info, err := svc.VolumeInfo(lv.container, lv.volumeId); err == nil {
					capacityBytes = utils.MBToBytes(info.Total)
				}
			}
		}
		entries = append(entries, &csi.ListVolumesResponse_Entry{
			Volume: &csi.Volume{
				VolumeId:      lv.volumeId,
				CapacityBytes: capacityBytes,
			},
		})
	}

	nextToken := ""
	if endIdx < len(all) {
		nextToken = strconv.Itoa(endIdx)
	}

	return &csi.ListVolumesResponse{
		Entries:   entries,
		NextToken: nextToken,
	}, nil
}

// GetCapacity reports the free space a CreateVolume with the same parameters
// could use, so the scope mirrors CreateVolume's parameter resolution:
//   - proxyProfile selects that client-info profile; when absent CreateVolume
//     uses the legacy default entry, so GetCapacity does too (when configured);
//   - fs defaults to zfs, exactly as CreateVolume treats a missing fs;
//   - poolName / clusterName restrict the sum to that single pool or cluster.
//
// Only when neither proxyProfile nor a legacy default exists (CreateVolume
// would fail; csi-sanity calls GetCapacity with no parameters) is every
// profile of the resolved fs summed.
func (cs *ControllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	params := req.GetParameters()
	filterProfile := strings.TrimSpace(params["proxyProfile"])
	filterFs := normalizeFs(params["fs"])
	poolName := strings.TrimSpace(params["poolName"])
	clusterName := strings.TrimSpace(params["clusterName"])

	cfg := cs.Driver.GetClientInfoConfig()
	if filterProfile != "" {
		if _, ok := cfg.Profiles[filterProfile]; !ok {
			return nil, status.Errorf(codes.InvalidArgument, "proxyProfile %q not found in client-info profiles", filterProfile)
		}
	}
	targets := capacityProfiles(cfg)
	if filterProfile == "" && cfg.Default != nil {
		// CreateVolume without proxyProfile goes to the legacy default proxy and
		// provisions whatever fs the StorageClass names there, so the capacity
		// query follows the requested fs rather than the entry's declared one.
		targets = []capacityProfile{{name: "", fs: filterFs}}
	}

	var availableCapacity int64
	attempted, failed := 0, 0

	for _, prof := range targets {
		if filterProfile != "" && prof.name != filterProfile {
			continue
		}
		if filterFs != prof.fs {
			continue
		}
		attempted++
		svc, err := cs.capacityService(prof.name)
		if err != nil {
			failed++
			log.Errorf("GetCapacity: skip profile %q: %v", prof.name, err)
			continue
		}

		switch prof.fs {
		case "lustre":
			clusters := []string{clusterName}
			if clusterName == "" {
				if clusters, err = svc.ListLustreClusters(); err != nil {
					failed++
					log.Errorf("GetCapacity: profile %q ListLustreClusters failed: %v", prof.name, err)
					continue
				}
			}
			for _, c := range clusters {
				ci, err := svc.LustreClusterInfo(c)
				if err != nil {
					log.Errorf("GetCapacity: profile %q cluster %s info failed: %v", prof.name, c, err)
					continue
				}
				availableCapacity += utils.MBToBytes(ci.Free)
			}
		default:
			pools := []string{poolName}
			if poolName == "" {
				if pools, err = svc.ListPools(); err != nil {
					failed++
					log.Errorf("GetCapacity: profile %q ListPools failed: %v", prof.name, err)
					continue
				}
			}
			for _, p := range pools {
				pi, err := svc.PoolInfo(p)
				if err != nil {
					log.Errorf("GetCapacity: profile %q pool %s info failed: %v", prof.name, p, err)
					continue
				}
				// zfs pool_info reports total/used/free in MB.
				if zpi, ok := pi.(webapi.ZfsPoolInfo); ok {
					availableCapacity += utils.MBToBytes(int64(zpi.Free))
				}
			}
		}
	}

	if attempted > 0 && failed == attempted {
		return nil, status.Error(codes.Unavailable, "GetCapacity: every matching client-info profile failed")
	}

	return &csi.GetCapacityResponse{
		AvailableCapacity: availableCapacity,
	}, nil
}

func (cs *ControllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: cs.Driver.csCap,
	}, nil
}

func (cs *ControllerServer) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerGetVolume is not supported")
}

func (cs *ControllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	volumeId := strings.TrimSpace(req.GetVolumeId())
	if volumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if req.GetCapacityRange() == nil {
		return nil, status.Error(codes.InvalidArgument, "Capacity range missing in request")
	}

	// Target size
	newSizeBytes, err := getSizeByCapacityRange(req.GetCapacityRange())
	if err != nil {
		return nil, err
	}

	attrs, found, err := cs.volumeAttributes(ctx, volumeId)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "PV attributes not found for volumeHandle=%s", volumeId)
	}

	release, err := cs.locks.acquire(volumeId)
	if err != nil {
		return nil, err
	}
	defer release()

	fs := volumeFsFromAttrs(attrs)
	poolOrCluster, err := backendKey(fs, attrs)
	if err != nil {
		return nil, err
	}
	reqService, err := cs.serviceForAttrs(attrs)
	if err != nil {
		return nil, err
	}

	info, err := reqService.GetVolume(fs, poolOrCluster, volumeId)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "Failed to look up volume %s: %v", volumeId, err)
	}
	if info == nil {
		return nil, status.Errorf(codes.NotFound, "Volume[%s] does not exist", volumeId)
	}
	currentSizeBytes := info.Size

	// Idempotency: if already at/above requested size, do not call proxy expand again.
	if currentSizeBytes > 0 && newSizeBytes <= currentSizeBytes {
		return &csi.ControllerExpandVolumeResponse{
			CapacityBytes:         currentSizeBytes,
			NodeExpansionRequired: false,
		}, nil
	}

	if err := reqService.ExpandVolume(fs, poolOrCluster, volumeId, newSizeBytes); err != nil {
		return nil, status.Errorf(codes.Internal, "ExpandVolume failed: %v", err)
	}

	// Fetch updated size when possible, otherwise return requested size.
	capBytes := newSizeBytes
	if info, err := reqService.GetVolume(fs, poolOrCluster, volumeId); err == nil && info != nil && info.Size > 0 {
		capBytes = info.Size
	}
	if capBytes < newSizeBytes {
		capBytes = newSizeBytes
	}

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         capBytes,
		NodeExpansionRequired: false,
	}, nil
}

func (cs *ControllerServer) ControllerModifyVolume(ctx context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerModifyVolume is not supported")
}

func (cs *ControllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "CreateSnapshot is not supported")
}

func (cs *ControllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "DeleteSnapshot is not supported")
}

func (cs *ControllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListSnapshots is not supported")
}
