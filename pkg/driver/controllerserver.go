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
}

func getSizeByCapacityRange(capRange *csi.CapacityRange) (int64, error) {
	if capRange == nil {
		return 1 * utils.UNIT_GB, nil
	}

	minSize := capRange.GetRequiredBytes()
	maxSize := capRange.GetLimitBytes()
	if 0 < maxSize && maxSize < minSize {
		return 0, status.Error(codes.InvalidArgument, "Invalid input: limitBytes is smaller than requiredBytes")
	}
	if minSize < utils.UNIT_GB {
		return 0, status.Error(codes.InvalidArgument, "Invalid input: required bytes is smaller than 1G")
	}

	return int64(minSize), nil
}

func (cs *ControllerServer) isVolumeAccessModeSupport(mode csi.VolumeCapability_AccessMode_Mode) bool {
	for _, accessMode := range cs.Driver.getVolumeCapabilityAccessModes() {
		if mode == accessMode.Mode {
			return true
		}
	}

	return false
}

func (cs *ControllerServer) lookupPVAttributesByVolumeHandle(ctx context.Context, volumeID string) (map[string]string, bool) {
	pvs, err := cs.Driver.K8sClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, false
	}
	for _, pv := range pvs.Items {
		if pv.Spec.CSI == nil {
			continue
		}
		if pv.Spec.CSI.VolumeHandle != volumeID {
			continue
		}
		return pv.Spec.CSI.VolumeAttributes, true
	}
	return nil, false
}

func (cs *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	// Snapshot restore and volume cloning are not supported. Without this guard the
	// requested content source is silently dropped and the caller gets an empty volume.
	if src := req.GetVolumeContentSource(); src != nil {
		return nil, status.Errorf(codes.InvalidArgument,
			"volume content source is not supported by this driver: %T", src.GetType())
	}

	sizeInByte, err := getSizeByCapacityRange(req.GetCapacityRange())
	volName, volCap := req.GetName(), req.GetVolumeCapabilities()

	if err != nil {
		return nil, err
	}

	pvcName := req.Parameters["csi.storage.k8s.io/pvc/name"]
	pvcNS := req.Parameters["csi.storage.k8s.io/pvc/namespace"]

	log.Infof("PVC Name : %s PVC NameSpace : %s", pvcName, pvcNS)

	pvc, err := cs.Driver.K8sClient.CoreV1().PersistentVolumeClaims(pvcNS).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("Failed to get pvc %s/%s: %v", pvcNS, pvcName, err)
	}

	var optionSVS, optionISS, optionComp, optionDedup string

	optionSVS = pvc.Annotations["flexa.io/optionSVS"]
	optionISS = pvc.Annotations["flexa.io/optionISS"]
	optionComp = pvc.Annotations["flexa.io/optionComp"]
	optionDedup = pvc.Annotations["flexa.io/optionDedup"]

	log.Infof("Volume Options : optionSVS(%s), optionISS(%s), optionComp(%s), optionDedup(%s)", optionSVS, optionISS, optionComp, optionDedup)

	var secureName, secureAddr, secureSub string

	secureName = fmt.Sprintf("zone_%s", pvcName)
	secureAddr = pvc.Annotations["flexa.io/secureAddress"]
	secureSub = pvc.Annotations["flexa.io/secureSubnet"]

	log.Infof("Share Secure Zone : name(%s) Address(%s) Subnet(%s)", secureName, secureAddr, secureSub)

	var nfsAccess, nfsNoRoot, nfsInsecure string

	nfsAccess = pvc.Annotations["flexa.io/nfsAccess"]
	nfsNoRoot = pvc.Annotations["flexa.io/nfsNoRootSquashing"]
	nfsInsecure = pvc.Annotations["flexa.io/nfsInsecure"]

	log.Infof("NFS Secure : Access(%s) NoRootSquashing(%s) Insecure(%s)", nfsAccess, nfsNoRoot, nfsInsecure)

	pvcQos := strings.TrimSpace(pvc.Annotations[volAttrPvcQos])
	log.Infof("PVC QoS : %s", pvcQos)

	if volName == "" {
		return nil, status.Errorf(codes.InvalidArgument, "No name is provided")
	}

	if volCap == nil {
		return nil, status.Errorf(codes.InvalidArgument, "No volume capabilities are provided")
	}

	for _, cap := range volCap {
		accessMode := cap.GetAccessMode().GetMode()

		if !cs.isVolumeAccessModeSupport(accessMode) {
			return nil, status.Errorf(codes.InvalidArgument, "Invalid volume capability access mode")
		}
	}

	params := req.GetParameters()

	poolName := params["poolName"]
	fs := params["fs"]
	proxyProfile := strings.TrimSpace(params[volAttrProxyProfile])

	if fs == "" {
		fs = "zfs"
	}

	if fs == "zfs" {
		if poolName == "" {
			return nil, status.Errorf(codes.InvalidArgument, "poolName is required for fs=zfs")
		}
		cs.Driver.PoolName = poolName
	}

	clusterName := params["clusterName"]
	if fs == "lustre" {
		if clusterName == "" {
			return nil, status.Errorf(codes.InvalidArgument, "clusterName is required for fs=lustre")
		}
	}

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

	lookupPool := poolName
	lookupKey := volName
	if fs == "lustre" {
		lookupPool = clusterName
	}

	proxy, err := cs.Driver.SelectProxy(proxyProfile)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	spec.SecureAddr, spec.SecureSub, err = defaultSecureZone(secureAddr, secureSub, proxy.MountIP)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	reqService := service.NewFlexAService()
	reqService.SetFep(proxy)

	k8sVolume := reqService.GetVolume(fs, lookupPool, lookupKey)
	if k8sVolume == nil || (fs == "zfs" && strings.TrimSpace(k8sVolume.BaseDir) == "") {
		k8sVolume, err = reqService.CreateVolume(spec)
		if err != nil {
			return nil, err
		}
	} else {
		// Size 0 means the backend could not report a size (e.g. Lustre quota not
		// yet visible in Redis); do not treat that as a capacity conflict.
		if k8sVolume.Size > 0 && k8sVolume.Size < sizeInByte {
			return nil, status.Errorf(codes.AlreadyExists,
				"Volume [%s] already exists but is smaller than requested capacity", volName)
		}
		// already existed
		log.Debugf("Volume [%s] already exists , backing name: [%s]", volName, k8sVolume.VolumeName)
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
			CapacityBytes: k8sVolume.Size,
			VolumeContext: volCtx,
		},
	}, nil
}

func (cs *ControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	volumeId := req.GetVolumeId()
	if volumeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "No volume id is provided")
	}

	attrs, ok := cs.lookupPVAttributesByVolumeHandle(ctx, volumeId)
	if !ok {
		return &csi.DeleteVolumeResponse{}, nil
	}

	fs := volumeFsFromAttrs(attrs)
	poolName := cs.Driver.PoolName
	if attrs != nil {
		if p := strings.TrimSpace(attrs["poolName"]); p != "" {
			poolName = p
		}
	}

	proxy, ok := proxyFromVolumeAttributes(attrs)
	if !ok {
		proxyProfile := ""
		if attrs != nil {
			proxyProfile = strings.TrimSpace(attrs[volAttrProxyProfile])
		}
		p, err := cs.Driver.SelectProxy(proxyProfile)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		proxy = p
	}
	reqService := service.NewFlexAService()
	reqService.SetFep(proxy)

	if fs == "lustre" {
		clusterName := strings.TrimSpace(attrs["clusterName"])
		if clusterName == "" {
			return nil, status.Errorf(codes.InvalidArgument, "Missing clusterName in PV attributes for lustre delete")
		}
		if err := reqService.DeleteVolume("lustre", clusterName, volumeId, volumeId); err != nil {
			return nil, status.Errorf(codes.Internal,
				fmt.Sprintf("Failed to DeleteVolume(%s), err: %v", volumeId, err))
		}
		return &csi.DeleteVolumeResponse{}, nil
	}

	shareName := webapi.LegacyZfsNfsShareName
	if attrs != nil {
		if s := strings.TrimSpace(attrs["shareName"]); s != "" {
			shareName = s
		}
	}
	if err := reqService.DeleteVolume("zfs", poolName, shareName, volumeId); err != nil {
		return nil, status.Errorf(codes.Internal,
			fmt.Sprintf("Failed to DeleteVolume(%s), err: %v", volumeId, err))
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

	if volCap == nil {
		return nil, status.Error(codes.InvalidArgument, "No volume capabilities are provided")
	}

	attrs, ok := cs.lookupPVAttributesByVolumeHandle(ctx, volumeId)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Volume[%s] does not exist", volumeId)
	}

	fs := volumeFsFromAttrs(attrs)
	poolName := cs.Driver.PoolName
	if attrs != nil {
		if p := strings.TrimSpace(attrs["poolName"]); p != "" {
			poolName = p
		}
	}

	proxy, ok := proxyFromVolumeAttributes(attrs)
	if !ok {
		proxyProfile := ""
		if attrs != nil {
			proxyProfile = strings.TrimSpace(attrs[volAttrProxyProfile])
		}
		p, err := cs.Driver.SelectProxy(proxyProfile)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		proxy = p
	}
	reqService := service.NewFlexAService()
	reqService.SetFep(proxy)

	if fs == "lustre" {
		clusterName := strings.TrimSpace(attrs["clusterName"])
		if clusterName == "" {
			return nil, status.Errorf(codes.NotFound, "Volume[%s] does not exist", volumeId)
		}
		if reqService.GetVolume("lustre", clusterName, volumeId) == nil {
			return nil, status.Errorf(codes.NotFound, "Volume[%s] does not exist", volumeId)
		}
		return &csi.ValidateVolumeCapabilitiesResponse{}, nil
	}

	if reqService.GetVolume("zfs", poolName, volumeId) == nil {
		return nil, status.Errorf(codes.NotFound, "Volume[%s] does not exist", volumeId)
	}

	return &csi.ValidateVolumeCapabilitiesResponse{}, nil
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

	// Target size
	newSizeBytes, err := getSizeByCapacityRange(req.GetCapacityRange())
	if err != nil {
		return nil, err
	}

	// Lookup PV attributes to determine fs/pool/cluster and proxy hints.
	attrs, ok := cs.lookupPVAttributesByVolumeHandle(ctx, volumeId)
	if !ok || attrs == nil {
		return nil, status.Errorf(codes.NotFound, "PV attributes not found for volumeHandle=%s", volumeId)
	}

	fs := volumeFsFromAttrs(attrs)

	// Resolve proxy: explicit attrs -> proxyProfile -> legacy default.
	proxy, ok := proxyFromVolumeAttributes(attrs)
	if !ok {
		proxyProfile := strings.TrimSpace(attrs[volAttrProxyProfile])
		p, err := cs.Driver.SelectProxy(proxyProfile)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		proxy = p
	}

	reqService := service.NewFlexAService()
	reqService.SetFep(proxy)

	// Determine backend name and current size (for shrink guard).
	var poolOrCluster string
	var backendVolName string
	var currentSizeBytes int64

	if fs == "lustre" {
		poolOrCluster = strings.TrimSpace(attrs["clusterName"])
		if poolOrCluster == "" {
			return nil, status.Error(codes.InvalidArgument, "Missing clusterName in PV attributes")
		}
		backendVolName = volumeId

		if info := reqService.GetVolume(fs, poolOrCluster, backendVolName); info != nil {
			currentSizeBytes = info.Size
		}
	} else {
		poolOrCluster = strings.TrimSpace(attrs["poolName"])
		if poolOrCluster == "" {
			return nil, status.Error(codes.InvalidArgument, "Missing poolName in PV attributes")
		}
		backendVolName = volumeId
		if info := reqService.GetVolume(fs, poolOrCluster, backendVolName); info != nil {
			currentSizeBytes = info.Size
		}
	}

	// Idempotency: if already at/above requested size, do not call proxy expand again.
	if currentSizeBytes > 0 && newSizeBytes <= currentSizeBytes {
		return &csi.ControllerExpandVolumeResponse{
			CapacityBytes:         currentSizeBytes,
			NodeExpansionRequired: false,
		}, nil
	}

	if err := reqService.ExpandVolume(fs, poolOrCluster, backendVolName, newSizeBytes); err != nil {
		return nil, status.Errorf(codes.Internal, "ExpandVolume failed: %v", err)
	}

	// Fetch updated size when possible, otherwise return requested size.
	capBytes := newSizeBytes
	if info := reqService.GetVolume(fs, poolOrCluster, backendVolName); info != nil && info.Size > 0 {
		capBytes = info.Size
	}
	if currentSizeBytes > 0 && capBytes < currentSizeBytes {
		capBytes = currentSizeBytes
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
