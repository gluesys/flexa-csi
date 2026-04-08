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
            Ip:     "",
            Port:   9001,
        },
    }
}

func (service *FlexAService) SetFep(proxy *common.ProxyInfo) {

    fep := &webapi.FEP{
        Ip:      proxy.Host,
        Port:    proxy.Port,
        MountIP: proxy.MountIP,
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

    if volName == "test-vol"{
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
            log.Errorf("Gluesys FlexA Call(CreateVolume) : Fail Create Volume(%s)",volName)
            return nil, err
        }

        //Create Share
        baseDir, err = service.fep.ZfsCreateShareNfs(volId, volName, poolName, secureName,secureAddr,secureSub,nfsAccess,nfsNoRoot,nfsInsecure)
        if err != nil {
            log.Errorf("Gluesys FlexA Call(CreateShare) : Fail Create Share in Volume(%s)",volName)
            return nil, err
        }
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
       Vip:         vip,
       VolumeId:    volId,
       VolumeName:  volName,
       PoolName:    outPool,
       Size:        spec.Size,
       Fs:          fs,
       BaseDir:     baseDir,
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
        log.Errorf("Gluesys FlexA Call(PoolInfo) : Fail Pool(%s) Info",poolName)
        return out, err
    }

    log.Infof("Gluesys FlexA Call(PoolInfo) : Success Pool(%s) Info",poolName)

    return poolInfo, nil
}

func (service *FlexAService) volumeInfo(poolName string, volName string) (webapi.ZfsVolInfo, error) {
    volInfo, err := service.fep.InfoZfsVol(poolName, volName)
    if err != nil {
        return webapi.ZfsVolInfo{}, err
    }

    log.Infof("Gluesys FlexA Call(volumeInfo) : Success Volume(%s) Info", volName)

    return volInfo, nil
}


func (service *FlexAService) GetVolume(fs string, poolOrCluster string, volName string) *models.K8sVolumeRespSpec {
    refForVip := service.fep.VipResolveRefIP()
    if refForVip == "" {
        return nil
    }

    fs = strings.TrimSpace(fs)
    if fs == "" {
        fs = "zfs"
    }

    if fs == "lustre" {
        clusterName := strings.TrimSpace(poolOrCluster)
        v := strings.TrimSpace(volName)
        if clusterName == "" || v == "" {
            log.Errorf("Gluesys FlexA Call(GetVolume): empty clusterName or volName for lustre")
            return nil
        }
        info, err := service.fep.LustreInfoVolume(clusterName, v)
        if err != nil {
            return nil
        }
        vip, err := service.fep.ResolveLustreVip(refForVip)
        if err != nil {
            log.Errorf("Gluesys FlexA Call(GetVolume) : Lustre VIP resolve failed: %v", err)
            return nil
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
        }
    }

    poolName := strings.TrimSpace(poolOrCluster)
    if poolName == "" {
        log.Errorf("Gluesys FlexA Call(GetVolume): empty poolName for ZFS volume %q", volName)
        return nil
    }

    vols, err := service.ListVolumes(poolName)

    if err != nil {
        log.Errorf("Gluesys FlexA Call(GetVolume) : Fail Volume(%s)", volName)
        return nil
    }

    for _, vol := range vols {
        if vol == volName {
            volInfo, err := service.volumeInfo(poolName, volName)
            if err != nil {
                return nil
            }

            vip, err := service.fep.ResolveZfsVip(poolName, refForVip)
            if err != nil {
                log.Errorf("Gluesys FlexA Call(GetVolume) : ZFS VIP resolve failed: %v", err)
                return nil
            }

            info := &models.K8sVolumeRespSpec {
                Vip:        vip,
                VolumeName: volInfo.VolName,
                PoolName:   volInfo.PoolName,
                // ZFS volume_info returns total/used/free in MB. Convert to bytes for CSI.
                Size:       volInfo.Total * utils.UNIT_MB,
                Used:       volInfo.Used * utils.UNIT_MB,
                Free:       volInfo.Free * utils.UNIT_MB,
                Fs:         "zfs",
                BaseDir:    volInfo.BaseDir,
            }

            log.Infof("Gluesys FlexA Call(GetVolume) : Success Volume(%s)", volName)

            return info
        }
    }

    return nil
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
