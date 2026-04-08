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

func parseNfsVesrion(ops []string) string {
    for _, op := range ops {
        if strings.HasPrefix(op, "nfsvers") {
            kvpair := strings.Split(op, "=")
            if len(kvpair) == 2 {
                return kvpair[1]
            }
        }
    }
    return ""
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
    sizeInByte, err := getSizeByCapacityRange(req.GetCapacityRange())
    volName, volCap := req.GetName(), req.GetVolumeCapabilities()

    if err != nil {
        return nil, err
    }

    pvcName := req.Parameters["csi.storage.k8s.io/pvc/name"]
    pvcNS := req.Parameters["csi.storage.k8s.io/pvc/namespace"]

    log.Infof("PVC Name : %s PVC NameSpace : %s",pvcName, pvcNS)

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

    secureName = fmt.Sprintf("zone_%s",pvcName)
    secureAddr = pvc.Annotations["flexa.io/secureAddress"]
    secureSub = pvc.Annotations["flexa.io/secureSubnet"]

    log.Infof("Share Secure Zone : name(%s) Address(%s) Subnet(%s)",secureName,secureAddr,secureSub)

    var nfsAccess, nfsNoRoot, nfsInsecure string

    nfsAccess = pvc.Annotations["flexa.io/nfsAccess"]
    nfsNoRoot = pvc.Annotations["flexa.io/nfsNoRootSquashing"]
    nfsInsecure = pvc.Annotations["flexa.io/nfsInsecure"]

    log.Infof("NFS Secure : Access(%s) NoRootSquashing(%s) Insecure(%s)",nfsAccess,nfsNoRoot,nfsInsecure)


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
        VolumeId:         volumeID,
        VolumeName:       backingName,
        PoolName:         poolOrCluster,
        Fs:               fs,
        Size:             sizeInByte,
        OptionISS:        optionISS,
        OptionSVS:        optionSVS,
        OptionComp:       optionComp,
        OptionDedup:      optionDedup,
        SecureName:       secureName,
        SecureAddr:       secureAddr,
        SecureSub:        secureSub,
        NfsAccess:        nfsAccess,
        NfsNoRoot:        nfsNoRoot,
        NfsInsecure:      nfsInsecure,
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
    reqService := service.NewFlexAService()
    reqService.SetFep(proxy)

    k8sVolume := reqService.GetVolume(fs, lookupPool, lookupKey)
    if k8sVolume == nil {
        k8sVolume, err = reqService.CreateVolume(spec)
        if err != nil {
            return nil, err
        }
    } else {
        // already existed
        log.Debugf("Volume [%s] already exists , backing name: [%s]", volName, k8sVolume.VolumeName)
    }


    return &csi.CreateVolumeResponse{
        Volume: &csi.Volume{
            VolumeId:      k8sVolume.VolumeId,
            CapacityBytes: k8sVolume.Size,
            VolumeContext: map[string]string{
                "vip":              k8sVolume.Vip,
                "poolName":         k8sVolume.PoolName,
                "baseDir":          k8sVolume.BaseDir,
                "fs":               fs,
                "clusterName":      clusterName,
                "protocol":         params["protocol"],
                "pvcName":          pvcName,
                "pvcNS":            pvcNS,
                volAttrProxyProfile: proxyProfile,
                volAttrProxyIP:      proxy.Host,
                volAttrProxyPort:    fmt.Sprintf("%d", proxy.Port),
                volAttrMountIP:      proxy.MountIP,
            },
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

    if err := reqService.DeleteVolume("zfs", poolName, webapi.ZfsNfsShareName, volumeId); err != nil {
        return nil, status.Errorf(codes.Internal,
            fmt.Sprintf("Failed to DeleteVolume(%s), err: %v", volumeId, err))
    }

    return &csi.DeleteVolumeResponse{}, nil
}



func (cs *ControllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
    return nil, status.Error(codes.Unimplemented, "")
}

func (cs *ControllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
    return nil, status.Error(codes.Unimplemented, "")
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

func (cs *ControllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
    maxEntries := req.GetMaxEntries()

    var entries []*csi.ListVolumesResponse_Entry
    var nextToken string = ""

    if 0 > maxEntries {
        return nil, status.Error(codes.InvalidArgument, "Max entries can not be negative.")
    }



    return &csi.ListVolumesResponse{
        Entries:   entries,
        NextToken: nextToken,
    }, nil
}

func (cs *ControllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {


    var availableCapacity int64 = 0

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
    return nil, status.Error(codes.Unimplemented, "")
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
    return nil, fmt.Errorf("Not Supported")
}

func (cs *ControllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
    return nil, fmt.Errorf("Not Supported")
}

func (cs *ControllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
    return nil, fmt.Errorf("Not Supported")
}

func (cs *ControllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
    return nil, fmt.Errorf("Not Supported")
}


