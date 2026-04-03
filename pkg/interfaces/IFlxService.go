// Copyright 2025 FlexA Inc.

package interfaces

import (
    "github.com/gluesys/flexa-csi/pkg/flexa/common"
    //"csi/pkg/flexa/webapi"
    "github.com/gluesys/flexa-csi/pkg/models"
)

// An interface for FlexA service

type FlexAService interface {
    SetFep(proxy *common.ProxyInfo)
    CreateVolume(spec *models.CreateVolumeSpec) (*models.K8sVolumeRespSpec, error)
    DeleteVolume(poolName string, shareName string, volumeName string) error
    ListPools() ([]string, error)
    ListVolumes(poolName string) ([]string, error)
    PoolInfo(poolName string) (interface{}, error)
    GetVolume(poolName string, volName string) *models.K8sVolumeRespSpec
    //ExpandVolume(volId string, newSize int64) (*models.VolumeRespSpec, error)
    //TODO
    //CreateSnapshot(spec *models.CreatSnapshotSpec) (*models.CreateSnapshotResSpec, error)
    //DeleteSnapshot(snapshotUuid string) error
    //GetSnapshotByName(snapshotName string) (*models.K8sSnapshotRespSpec, error)
    //ListAllSnapshots() []*models.K8sSnapshotRespSpec
    //ListSnapshots(volId string) []*models.K8sSnapshotRespSpec
}
