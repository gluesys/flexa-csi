/*
 * Copyright 2025 Gluesys FlexA Inc.
 */

package service

import (
	//"errors"
	"fmt"
	"strings"

	//"github.com/cenkalti/backoff/v4"
	log "github.com/sirupsen/logrus"
	//"google.golang.org/grpc/codes"
	//"google.golang.org/grpc/status"
	//"strconv"
	//"time"
	//"strings"

	"github.com/gluesys/flexa-csi/pkg/flexa/common"
	"github.com/gluesys/flexa-csi/pkg/flexa/webapi"
	"github.com/gluesys/flexa-csi/pkg/models"
	"github.com/gluesys/flexa-csi/pkg/utils"
)

type FlexAService struct {
	fep *webapi.FEP
}

func NewFlexAService() *FlexAService {
	return &FlexAService{
		fep: &webapi.FEP{
			Ip:   "",
			Port: 9001,
		},
	}
}

func (service *FlexAService) SetFep(proxy *common.ProxyInfo) {

	fep := &webapi.FEP{
		Ip:      proxy.Host,
		Port:    proxy.Port,
		MountIP: proxy.MountIP,
		Token:   proxy.Token,
	}

	service.fep = fep
	log.Infof("Gluesys FlexA Call(SetFep) : %s (mountIP=%s).", fep.Ip, fep.MountIP)
}

func (service *FlexAService) CreateVolume(spec *models.CreateVolumeSpec) (*models.K8sVolumeRespSpec, error) {

	volId := spec.VolumeId
	volName := spec.VolumeName
	poolName := spec.PoolName
	fs := spec.Fs

	// Volume Options
	optionSVS := spec.OptionSVS
	optionISS := spec.OptionISS
	optionComp := spec.OptionComp
	optionDedup := spec.OptionDedup
	// Share Secure info
	secureName := spec.SecureName
	secureAddr := spec.SecureAddr
	secureSub := spec.SecureSub
	// nfs secure option
	nfsAccess := spec.NfsAccess
	nfsNoRoot := spec.NfsNoRoot
	nfsInsecure := spec.NfsInsecure

	if volName == "test-vol" {
		volId = "test-vol"
	}

	if fs == "" {
		fs = "zfs"
	}

	refForVip := service.fep.VipResolveRefIP()
	if refForVip == "" {
		return nil, fmt.Errorf("VIP resolve reference is empty; check client-info host")
	}

	var baseDir string
	if fs == "lustre" {
		clusterName := poolName // controllerserver에서 PoolName에 clusterName을 넣어줌
		resp, err := service.fep.LustreCreateVolume(
			spec.Size,
			clusterName,
			volName,
			secureName,
			secureAddr,
			secureSub,
			nfsAccess,
			nfsNoRoot,
			nfsInsecure,
			spec.PvcQos,
		)
		if err != nil {
			log.Errorf("Gluesys FlexA Call(CreateVolume) : Fail Create Lustre Volume(%s)", volName)
			return nil, err
		}
		baseDir = resp.Path
	} else {
		// Create ZFS Volume
		err := service.fep.ZfsCreateVolume(spec.Size, volId, volName, poolName, optionSVS, optionISS, optionComp, optionDedup)
		if err != nil {
			log.Errorf("Gluesys FlexA Call(CreateVolume) : Fail Create Volume(%s)", volName)
			return nil, err
		}

		baseDir, err = service.createZfsShare(spec)
		if err != nil {
			// The share was not created, so only the dataset is rolled back. Going
			// through ZfsDeleteVolume would stop at the missing share.
			if rollbackErr := service.fep.ZfsDestroyVolume(volName, poolName); rollbackErr != nil {
				log.Errorf("Gluesys FlexA Call(CreateVolume) : Fail Rollback Volume(%s): %v",
					volName, rollbackErr)
			}
			return nil, err
		}
	}

	return service.volumeResponse(spec, baseDir, refForVip)
}

// CreateZfsShare finishes a ZFS volume whose dataset exists but whose NFS share
// does not (an earlier create stopped between the two calls).
func (service *FlexAService) CreateZfsShare(spec *models.CreateVolumeSpec) (*models.K8sVolumeRespSpec, error) {
	refForVip := service.fep.VipResolveRefIP()
	if refForVip == "" {
		return nil, fmt.Errorf("VIP resolve reference is empty; check client-info host")
	}
	baseDir, err := service.createZfsShare(spec)
	if webapi.IsAlreadyExists(err) {
		// The legacy proxy reports no share path in volume info, so an earlier
		// attempt may have finished the share already; take its path.
		baseDir, err = service.fep.ZfsSharePath(spec.PoolName, spec.VolumeName, webapi.ZfsNfsShareName(spec.VolumeId))
	}
	if err != nil {
		return nil, err
	}
	return service.volumeResponse(spec, baseDir, refForVip)
}

func (service *FlexAService) createZfsShare(spec *models.CreateVolumeSpec) (string, error) {
	baseDir, err := service.fep.ZfsCreateShareNfs(spec.VolumeId, spec.VolumeName, spec.PoolName,
		spec.SecureName, spec.SecureAddr, spec.SecureSub, spec.NfsAccess, spec.NfsNoRoot, spec.NfsInsecure)
	if err != nil {
		log.Errorf("Gluesys FlexA Call(CreateShare) : Fail Create Share in Volume(%s)", spec.VolumeName)
		return "", err
	}
	return baseDir, nil
}

func (service *FlexAService) volumeResponse(spec *models.CreateVolumeSpec, baseDir string, refForVip string) (*models.K8sVolumeRespSpec, error) {
	volId, volName, poolName := spec.VolumeId, spec.VolumeName, spec.PoolName
	if volName == "test-vol" {
		volId = "test-vol"
	}
	fs := spec.Fs
	if fs == "" {
		fs = "zfs"
	}

	var vip string
	var err error
	if fs == "lustre" {
		vip, err = service.fep.ResolveLustreVip(refForVip)
	} else {
		vip, err = service.fep.ResolveZfsVip(poolName, refForVip)
	}
	if err != nil {
		log.Errorf("Gluesys FlexA Call(CreateVolume) : VIP resolve failed: %v", err)
		return nil, err
	}

	outPool := poolName
	if fs == "lustre" {
		outPool = ""
	}

	ret := &models.K8sVolumeRespSpec{
		Vip:        vip,
		VolumeId:   volId,
		VolumeName: volName,
		PoolName:   outPool,
		Size:       spec.Size,
		Fs:         fs,
		BaseDir:    baseDir,
	}
	if fs == "zfs" {
		ret.ShareName = webapi.ZfsNfsShareName(volId)
	}

	log.Infof("Gluesys FlexA Call(CreateVolume) : Success Create Volume(%s)", volName)

	return ret, nil
}

func (service *FlexAService) DeleteVolume(fs string, poolName string, shareName string, volName string) error {
	fs = strings.TrimSpace(fs)
	if fs == "" {
		fs = "zfs"
	}
	if fs == "lustre" {
		err := service.fep.LustreDeleteVolume(poolName, volName)
		if err != nil {
			log.Errorf("Gluesys FlexA Call(DeleteVolume) : Fail Delete Lustre Volume(%s)", volName)
			return err
		}
		log.Infof("Gluesys FlexA Call(DeleteVolume) : Success Delete Lustre Volume(%s)", volName)
		return nil
	}

	err := service.fep.ZfsDeleteVolume(volName, shareName, poolName)

	if err != nil {
		log.Errorf("Gluesys FlexA Call(DeleteVolume) : Fail Delete Volume(%s)", volName)
		return err
	}

	log.Infof("Gluesys FlexA Call(DeleteVolume) : Success Delete Volume(%s)", volName)

	return nil
}

func (service *FlexAService) ListPools() ([]string, error) {
	poolList, err := service.fep.ListZfsPool()
	if err != nil {
		var out []string
		log.Error("Gluesys FlexA Call(ListPools) : Fail")
		return out, err
	}

	log.Info("Gluesys FlexA Call(ListPools) : Success")

	return poolList, nil
}

func (service *FlexAService) ListVolumes(poolName string) ([]string, error) {
	volList, err := service.fep.ListZfsVolume(poolName)

	if err != nil {
		var out []string
		log.Error("Gluesys FlexA Call(ListVolumes) : Fail")
		return out, err
	}

	log.Info("Gluesys FlexA Call(ListVolumes) : Success")

	return volList, nil
}

func (service *FlexAService) PoolInfo(poolName string) (interface{}, error) {
	poolInfo, err := service.fep.InfoZfsPool(poolName)

	if err != nil {
		var out interface{}
		log.Errorf("Gluesys FlexA Call(PoolInfo) : Fail Pool(%s) Info", poolName)
		return out, err
	}

	log.Infof("Gluesys FlexA Call(PoolInfo) : Success Pool(%s) Info", poolName)

	return poolInfo, nil
}

func (service *FlexAService) ListLustreClusters() ([]string, error) {
	clusterList, err := service.fep.LustreListCluster()
	if err != nil {
		var out []string
		log.Error("Gluesys FlexA Call(ListLustreClusters) : Fail")
		return out, err
	}

	log.Info("Gluesys FlexA Call(ListLustreClusters) : Success")

	return clusterList, nil
}

func (service *FlexAService) ListLustreVolumes(clusterName string) ([]string, error) {
	volList, err := service.fep.LustreListVolume(clusterName)

	if err != nil {
		var out []string
		log.Errorf("Gluesys FlexA Call(ListLustreVolumes) : Fail Cluster(%s)", clusterName)
		return out, err
	}

	log.Infof("Gluesys FlexA Call(ListLustreVolumes) : Success Cluster(%s)", clusterName)

	return volList, nil
}

func (service *FlexAService) LustreClusterInfo(clusterName string) (webapi.LustreClusterInfoResponse, error) {
	clusterInfo, err := service.fep.LustreClusterInfo(clusterName)

	if err != nil {
		log.Errorf("Gluesys FlexA Call(LustreClusterInfo) : Fail Cluster(%s) Info", clusterName)
		return webapi.LustreClusterInfoResponse{}, err
	}

	log.Infof("Gluesys FlexA Call(LustreClusterInfo) : Success Cluster(%s) Info", clusterName)

	return clusterInfo, nil
}

// LustreVolumeInfo is the public wrapper around fep.LustreInfoVolume for list/stats paths.
func (service *FlexAService) LustreVolumeInfo(clusterName string, volName string) (webapi.LustreVolumeInfoResponse, error) {
	volInfo, err := service.fep.LustreInfoVolume(clusterName, volName)
	if err != nil {
		return webapi.LustreVolumeInfoResponse{}, err
	}

	log.Infof("Gluesys FlexA Call(LustreVolumeInfo) : Success Volume(%s) Info", volName)

	return volInfo, nil
}

func (service *FlexAService) volumeInfo(poolName string, volName string) (webapi.ZfsVolInfo, error) {
	volInfo, err := service.fep.InfoZfsVol(poolName, volName)
	if err != nil {
		return webapi.ZfsVolInfo{}, err
	}

	log.Infof("Gluesys FlexA Call(volumeInfo) : Success Volume(%s) Info", volName)

	return volInfo, nil
}

// VolumeInfo is the public wrapper around volumeInfo for ZFS volumes (list/stats paths).
func (service *FlexAService) VolumeInfo(poolName string, volName string) (webapi.ZfsVolInfo, error) {
	return service.volumeInfo(poolName, volName)
}

// GetVolume returns (nil, nil) when the volume does not exist and an error when
// the proxy could not answer, so callers never mistake an outage for "gone".
func (service *FlexAService) GetVolume(fs string, poolOrCluster string, volName string) (*models.K8sVolumeRespSpec, error) {
	refForVip := service.fep.VipResolveRefIP()
	if refForVip == "" {
		return nil, fmt.Errorf("VIP resolve reference is empty; check client-info host")
	}

	fs = strings.TrimSpace(fs)
	if fs == "" {
		fs = "zfs"
	}

	if fs == "lustre" {
		clusterName := strings.TrimSpace(poolOrCluster)
		v := strings.TrimSpace(volName)
		if clusterName == "" || v == "" {
			return nil, fmt.Errorf("empty clusterName or volName for lustre volume %q", volName)
		}
		info, err := service.fep.LustreInfoVolume(clusterName, v)
		if webapi.IsMissing(err, "Share("+v+")") {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		vip, err := service.fep.ResolveLustreVip(refForVip)
		if err != nil {
			log.Errorf("Gluesys FlexA Call(GetVolume) : Lustre VIP resolve failed: %v", err)
			return nil, err
		}
		sizeBytes := int64(0)
		if info.Quota.LimitMb > 0 {
			sizeBytes = info.Quota.LimitMb * utils.UNIT_MB
		}
		return &models.K8sVolumeRespSpec{
			Vip:        vip,
			VolumeId:   v,
			PoolName:   "",
			VolumeName: info.VolName,
			Size:       sizeBytes,
			Fs:         "lustre",
			BaseDir:    info.Path,
		}, nil
	}

	poolName := strings.TrimSpace(poolOrCluster)
	if poolName == "" {
		return nil, fmt.Errorf("empty poolName for ZFS volume %q", volName)
	}

	vols, err := service.ListVolumes(poolName)
	if webapi.IsMissing(err, "Pool("+poolName+")") {
		// The pool itself is gone, so the volume is too.
		return nil, nil
	}
	if err != nil {
		log.Errorf("Gluesys FlexA Call(GetVolume) : Fail Volume(%s)", volName)
		return nil, err
	}

	for _, vol := range vols {
		if vol != volName {
			continue
		}
		volInfo, err := service.volumeInfo(poolName, volName)
		if webapi.IsMissing(err, "Volume("+volName+")") {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}

		vip, err := service.fep.ResolveZfsVip(poolName, refForVip)
		if err != nil {
			log.Errorf("Gluesys FlexA Call(GetVolume) : ZFS VIP resolve failed: %v", err)
			return nil, err
		}

		info := &models.K8sVolumeRespSpec{
			Vip:        vip,
			VolumeId:   volName,
			VolumeName: volInfo.VolName,
			PoolName:   volInfo.PoolName,
			// ZFS volume_info returns total/used/free in MB. Convert to bytes for CSI.
			Size:      volInfo.Total * utils.UNIT_MB,
			Used:      volInfo.Used * utils.UNIT_MB,
			Free:      volInfo.Free * utils.UNIT_MB,
			Fs:        "zfs",
			BaseDir:   volInfo.BaseDir,
			ShareName: volInfo.ShareName,
		}

		log.Infof("Gluesys FlexA Call(GetVolume) : Success Volume(%s)", volName)

		return info, nil
	}

	return nil, nil
}

func (service *FlexAService) ExpandVolume(fs string, poolOrCluster string, volName string, newSizeBytes int64) error {
	if service == nil || service.fep == nil {
		return fmt.Errorf("service is not initialized")
	}
	if strings.TrimSpace(volName) == "" {
		return fmt.Errorf("volName is required")
	}
	if strings.TrimSpace(poolOrCluster) == "" {
		return fmt.Errorf("poolOrCluster is required")
	}
	if newSizeBytes <= 0 {
		return fmt.Errorf("newSizeBytes must be positive")
	}

	if fs == "" {
		fs = "zfs"
	}
	if fs == "lustre" {
		return service.fep.LustreExpandVolume(poolOrCluster, volName, newSizeBytes)
	}
	return service.fep.ZfsExpandVolume(poolOrCluster, volName, newSizeBytes)
}

//TODO func (service *FlexAService) GetSnapshotByName(snapshotName string) *models.K8sSnapshotRespSpec {}

//TODO func (service *FlexAService) CreateSnapshot(spec *models.CreateK8sVolumeSnapshotSpec) (*models.K8sSnapshotRespSpec, error) {}

//TODO func (service *FlexAService) DeleteSnapshot(snapshotUuid string) error {}

//TODO func (service *FlexAService) ListAllSnapshots() []*models.K8sSnapshotRespSpec {}

//TODO func (service *FlexAService) ListSnapshots(volId string) []*models.K8sSnapshotRespSpec {}
