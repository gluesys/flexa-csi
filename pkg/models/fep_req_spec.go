package models

import (
    //"fmt"
    "github.com/container-storage-interface/spec/lib/go/csi"

    //"csi/pkg/flexa/webapi"
)

type CreateVolumeSpec struct {
    VolumeName          string // Volume Name
    VolumeId            string // Volume UUID
    PoolName            string // Pool Name
    Size                int64 // Volume Size
    SourceSnapshotId    string // Volume Snapshot ID
    SourceVolumeId      string // Source Volume ID
    Fs                  string // Default : zfs , TODO : lustre
    OptionSVS           string // zfs volume options
    OptionISS           string // zfs volume options
    OptionComp          string // zfs volume options
    OptionDedup         string // zfs volume options
    SecureName          string // share secure zone name
    SecureAddr          string // share secure address
    SecureSub           string // share secure subnetmask
    NfsAccess           string // nfs secure : Access Mode ( RW, RO )
    NfsNoRoot           string // nfs NoRootSquashing Option
    NfsInsecure         string // nfs Insecure Option
}

type CreateShareSpec struct {
    ShareName           string // Share Name
    PoolName            string // Pool Name
    VolName             string // Volume Name
    Path                string // Share Path
    Size                int64 // Share Size-bytes
}

//TODO type CreateSnapshotSpec struct {}

//TODO type CreateSnapshotResSpec struct {}

type K8sVolumeRespSpec struct {
    Vip                 string // FlexA NFS Mount Vip
    VolumeId            string // Volume UUID
    PoolName            string // Pool Name
    VolumeName          string // Volume Name
    Size                int64  // Volume Size
    Free                int64
    Used                int64
    Fs                  string // Default : zfs , TODO : lustre
    BaseDir             string // Share Base Directory Path
}

//TODO type K8sSnapshotRespSpec struct {}

type NodeStageVolumeSpec struct {
    VolumeId            string // Volume ID
    StagingTargetPath   string // Node Staging Path(target)
    VolumeCapability    *csi.VolumeCapability
    Source              string // NFS mount source path( Vip@BaseDir )
    FormatOptions       string
}

type NfsVolMeta struct {
    VolumeId            string
    Source              string
    TargetPath          string
    StagingGroupPath    string
    MountOptions        []string
}

type MountData struct {
    Source      string
    Mountpoint  string
    Opts        string
}
