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
    "os"
    "os/exec"
    "bufio"
    "encoding/json"
    "encoding/base64"
    "crypto/sha256"
    "strings"
    "sort"
    "slices"

    "github.com/container-storage-interface/spec/lib/go/csi"
    log "github.com/sirupsen/logrus"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
    "k8s.io/mount-utils"
    clientset "k8s.io/client-go/kubernetes"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

    "github.com/gluesys/flexa-csi/pkg/flexa/service"
    "github.com/gluesys/flexa-csi/pkg/flexa/webapi"
    "github.com/gluesys/flexa-csi/pkg/interfaces"
    "github.com/gluesys/flexa-csi/pkg/models"
)

type NodeServer struct {
    csi.UnimplementedNodeServer
    Driver     *Driver
    Mounter    *mount.SafeFormatAndMount
    FlxService interfaces.FlexAService
    Client     clientset.Interface
}

func (ns *NodeServer) lookupPVAttributesByVolumeHandle(ctx context.Context, volumeID string) (map[string]string, bool) {
    pvs, err := ns.Driver.K8sClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
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

func createTargetMountPathNfs(mounter mount.Interface, mountPath string, mountPermissionsUint uint64) (bool, error) {
    notMount, err := mounter.IsLikelyNotMountPoint(mountPath)
    if err != nil {
        if os.IsNotExist(err) {
            log.Infof("NodeServer : Create Mount Path(%s)", mountPath)
            if err := os.MkdirAll(mountPath, os.FileMode(mountPermissionsUint)); err != nil{
                return notMount, err
            }
            notMount = true
        } else {
            return false, err
        }
    }

    return notMount, nil
}

func createTargetMountPath(mounter mount.Interface, mountPath string, isBlock bool) (bool, error) {
    notMount, err := mount.IsNotMountPoint(mounter, mountPath)
    if err != nil {
        if os.IsNotExist(err) {
            if isBlock{
                pathFile, err := os.OpenFile(mountPath, os.O_CREATE | os.O_RDWR, 0750)
                if err != nil {
                    log.Errorf("Failed to create mountPath: %s with error: %v", mountPath, err)
                    return notMount, err
                }
                if err = pathFile.Close(); err != nil {
                    log.Errorf("Failed to close mountPath: %s with error : %v", mountPath, err)
                    return notMount, err
                }
            } else {
                err = os.MkdirAll(mountPath, 0750)
                if err != nil {
                    return notMount, err
                }
            }
            notMount = true
        } else {
            return false, err
        }
    }

    return notMount, nil
}

func(ns *NodeServer) saveNfsVolMeta(volId string, targetPath string, stagingGroupPath string, source string, options []string) error{
    targetHash := sha256.Sum256([]byte(targetPath))
    encode := base64.RawURLEncoding.EncodeToString(targetHash[:])

    metaDict := fmt.Sprintf("/var/lib/flexa/meta/%s", volId)
    metaFile := fmt.Sprintf("%s/%s.json",metaDict,encode)

    metaData := &models.NfsVolMeta{
        VolumeId:           volId,
        Source:             source,
        TargetPath:         targetPath,
        StagingGroupPath:   stagingGroupPath,
        MountOptions:       options,
    }

    jsonData, err := json.MarshalIndent(metaData,"", "  ")

    log.Infof("Save Volume Meta Path : %s",metaFile)
    log.Infof("Save Volume Meta : %v",metaData)

    if err != nil {
        return err
    }

    if err := os.MkdirAll(metaDict,0755); err != nil {
        return err
    }

    if err := os.WriteFile(metaFile, jsonData, 0644); err != nil{
        return err
    }

    return nil
}

func(ns *NodeServer) loadNfsVolMeta(volId string, targetPath string) (*models.NfsVolMeta, error) {
    targetHash := sha256.Sum256([]byte(targetPath))
    encode := base64.RawURLEncoding.EncodeToString(targetHash[:])

    metaFile := fmt.Sprintf("/var/lib/flexa/meta/%s/%s.json",volId, encode)
    jsonData, err := os.ReadFile(metaFile)
    if err != nil {
        return nil, err
    }

    var data models.NfsVolMeta

    log.Infof("Load Volume Meta Path : %v",metaFile)


    if err := json.Unmarshal(jsonData, &data); err != nil {
        return nil, err
    }

    log.Infof("Load Volume Meta : %v",data)

    return &data, nil
}

func(ns *NodeServer) deleteNfsVolMeta(volId string, targetPath string) error {
    targetHash := sha256.Sum256([]byte(targetPath))
    encode := base64.RawURLEncoding.EncodeToString(targetHash[:])

    metaFile := fmt.Sprintf("/var/lib/flexa/meta/%s/%s.json", volId, encode)

    if err := os.Remove(metaFile); err != nil {
        return err
    }

    log.Infof("Delete Volume Meta Path : %s",metaFile)

    return nil
}

func(ns *NodeServer) getMountOptions(ctx context.Context, podName string, podNS string) ([]string, error) {
    pod, err := ns.Driver.K8sClient.CoreV1().Pods(podNS).Get(ctx, podName, metav1.GetOptions{})
    if err != nil {
        return nil, err
    }

    options := pod.Annotations["flexa.io/mountOptions"]

    listOptions := strings.Split(options,",")

    return listOptions, nil
}

func(ns *NodeServer)getServiceVIP(ctx context.Context, podName string, podNS string) (string, error){
    pod, err := ns.Driver.K8sClient.CoreV1().Pods(podNS).Get(ctx, podName, metav1.GetOptions{})
    if err != nil {
        return "", err
    }

    svip := pod.Annotations["flexa.io/serviceVIP"]

    return svip, nil
}

func(ns *NodeServer) getNfsGroupPath(ctx context.Context, req *csi.NodePublishVolumeRequest, pvcName string, pvcNS string, options []string) (string) {
    sort.Strings(options)
    strOpts := strings.Join(options,"_")
    hashOpts := sha256.Sum256([]byte(strOpts))
    encode := base64.RawURLEncoding.EncodeToString(hashOpts[:])

    path := fmt.Sprintf("/mnt/flexa/global/%s/%s/%s/mount",pvcNS,pvcName,encode)

    return path
}

func(ns *NodeServer) scanMountData(source string, stagingPath string) ([]*models.MountData, error){
    mountInfos, err := os.Open("/proc/mounts")
    if err != nil {
        return nil, err
    }

    defer mountInfos.Close()

    var mountDatas []*models.MountData
    scanner := bufio.NewScanner(mountInfos)

    for scanner.Scan() {
        line := scanner.Text()
        fields := strings.Fields(line)
        if source == fields[0] {
            mountDatas = append(mountDatas, &models.MountData{
                Source:     fields[0],
                Mountpoint:  fields[1],
                Opts:       fields[3],

            })
        }
    }

    return mountDatas, nil
}

func(ns *NodeServer) splitStagingOrTarget(mountDatas []*models.MountData, stagingPath string) (*models.MountData, []*models.MountData){
    var targets []*models.MountData
    var staging *models.MountData

    for _, mountData:= range mountDatas {
        if mountData.Mountpoint == stagingPath{
            staging = mountData
        } else {
            targets = append(targets, mountData)
        }
    }

    return staging, targets
}

func(ns *NodeServer) countBindMounts(stagingGroupPath string, source string, options []string) (int64, error) {
    mountInfos, err := ns.scanMountData(source, stagingGroupPath)
    if err != nil {
        return -1, err
    }

    stagingMountInfo, targetsMountInfo := ns.splitStagingOrTarget(mountInfos, stagingGroupPath)
    stagingOpts := stagingMountInfo.Opts

    var bindCount int

    bindCount = 0

    for _, targetMountInfo := range targetsMountInfo {
        log.Infof("Staging Group Path : %s, Staging Optional : %s",stagingGroupPath, stagingOpts)
        if stagingOpts == targetMountInfo.Opts {
            log.Infof("Bind Path : %s",targetMountInfo.Mountpoint)

            bindCount = bindCount + 1
        }
    }

    return int64(bindCount), nil
}


func(ns *NodeServer) mountNfsGroup(mount mount.Interface, source string, stagingGroupPath string, options []string) error {
    var mountPermissionUnit uint64 = 0750

    notMount, err := createTargetMountPathNfs(ns.Mounter.Interface, stagingGroupPath, mountPermissionUnit)
    if err != nil {
        return status.Error(codes.Internal, err.Error())
    }

    if !notMount{
        log.Infof("mountNfsGroup : %s is already mounted", stagingGroupPath)
        return nil
    }

    log.Infof("mountNFsGroup: mountGroupPath(%s) options(%v)", stagingGroupPath, options)
    err = ns.Mounter.Mount(source, stagingGroupPath, "nfs", options)
    if err != nil {
        if os.IsPermission(err){
            return status.Error(codes.PermissionDenied, err.Error())
        }
        if strings.Contains(err.Error(), "Invalid argument") {
            return status.Error(codes.InvalidArgument, err.Error())
        }
        return status.Error(codes.Internal, err.Error())
    }

    if mountPermissionUnit > 0 {
        if err := chmodIfPermissionMismatch(stagingGroupPath, os.FileMode(mountPermissionUnit)); err != nil {
            return status.Error(codes.Internal, err.Error())
        }
    }

    log.Infof("NFS Group Mount %s on %s succeeded", source, stagingGroupPath)


    return nil
}



func lustreMountViaHost(source, target string, options []string) error {
    args := []string{"--target", "1", "--mount", "--", "mount", "-t", "lustre"}
    for _, o := range options {
        args = append(args, "-o", o)
    }
    args = append(args, source, target)

    log.Infof("lustreMountViaHost: nsenter %v", args)
    cmd := exec.Command("nsenter", args...)
    output, err := cmd.CombinedOutput()
    if err != nil {
        return fmt.Errorf("nsenter lustre mount failed: %s: %w", strings.TrimSpace(string(output)), err)
    }
    return nil
}

func lustreUmountViaHost(target string) error {
    args := []string{"--target", "1", "--mount", "--", "umount", target}

    log.Infof("lustreUmountViaHost: nsenter %v", args)
    cmd := exec.Command("nsenter", args...)
    output, err := cmd.CombinedOutput()
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
    protocol := req.VolumeContext["protocol"]
    if protocol != "lustre" {
        return &csi.NodeStageVolumeResponse{}, nil
    }

    volumeId := req.GetVolumeId()
    stagingPath := req.GetStagingTargetPath()
    clusterName := req.VolumeContext["clusterName"]

    if volumeId == "" {
        return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
    }
    if stagingPath == "" {
        return nil, status.Error(codes.InvalidArgument, "Staging target path missing in request")
    }
    if clusterName == "" {
        return nil, status.Error(codes.InvalidArgument, "clusterName missing in volume context for Lustre mount")
    }

    notMount, err := ns.Mounter.Interface.IsLikelyNotMountPoint(stagingPath)
    if err != nil {
        if os.IsNotExist(err) {
            if mkErr := os.MkdirAll(stagingPath, 0750); mkErr != nil {
                return nil, status.Errorf(codes.Internal, "failed to create staging path %s: %v", stagingPath, mkErr)
            }
            notMount = true
        } else {
            return nil, status.Errorf(codes.Internal, "failed to check mount point %s: %v", stagingPath, err)
        }
    }
    if !notMount {
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
    if err := lustreMountViaHost(source, stagingPath, nil); err != nil {
        return nil, status.Errorf(codes.Internal, "Lustre mount failed: source=%s target=%s err=%v", source, stagingPath, err)
    }

    log.Infof("NodeStageVolume: Lustre mount succeeded source(%s) stagingPath(%s)", source, stagingPath)
    return &csi.NodeStageVolumeResponse{}, nil
}

func (ns *NodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
    stagingPath := req.GetStagingTargetPath()
    if stagingPath == "" {
        return nil, status.Error(codes.InvalidArgument, "Staging target path missing in request")
    }

    notMount, err := mount.IsNotMountPoint(ns.Mounter.Interface, stagingPath)
    if err != nil {
        if os.IsNotExist(err) {
            return &csi.NodeUnstageVolumeResponse{}, nil
        }
        return nil, status.Errorf(codes.Internal, "failed to check mount point %s: %v", stagingPath, err)
    }
    if notMount {
        return &csi.NodeUnstageVolumeResponse{}, nil
    }

    log.Infof("NodeUnstageVolume: unmounting stagingPath(%s)", stagingPath)
    if err := lustreUmountViaHost(stagingPath); err != nil {
        return nil, status.Errorf(codes.Internal, "failed to unmount staging path %s: %v", stagingPath, err)
    }

    if err := os.RemoveAll(stagingPath); err != nil {
        log.Debugf("NodeUnstageVolume: failed to remove staging path %s: %v", stagingPath, err)
    }

    log.Infof("NodeUnstageVolume: successfully unmounted stagingPath(%s)", stagingPath)
    return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *NodeServer) nodePublishLustre(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
    stagingPath := req.GetStagingTargetPath()
    targetPath := req.GetTargetPath()
    volumeId := req.GetVolumeId()

    if volumeId == "" || targetPath == "" || stagingPath == "" {
        return nil, status.Error(codes.InvalidArgument,
            "InvalidArgument: Please check volume ID, target path and staging path.")
    }

    log.Infof("NodePublishVolume[lustre]: volumeId(%s) stagingPath(%s) targetPath(%s)", volumeId, stagingPath, targetPath)

    notMount, err := createTargetMountPath(ns.Mounter.Interface, targetPath, false)
    if err != nil {
        return nil, status.Errorf(codes.Internal, "failed to create target path: %v", err)
    }
    if !notMount {
        return &csi.NodePublishVolumeResponse{}, nil
    }

    readonly := req.GetReadonly()

    bindOptions := []string{"bind", "rprivate"}
    if err = ns.Mounter.Interface.Mount(stagingPath, targetPath, "", bindOptions); err != nil {
        return nil, status.Errorf(codes.Internal, "Lustre bind mount failed: %v", err)
    }

    if readonly {
        if err := ns.Mounter.Interface.Mount(targetPath, targetPath, "", []string{"remount", "ro"}); err != nil {
            return nil, status.Errorf(codes.Internal, "Lustre readonly remount failed: %v", err)
        }
        log.Infof("NodePublishVolume[lustre]: readonly remount succeeded (%s)", targetPath)
    }

    log.Infof("NodePublishVolume[lustre]: bind mount succeeded (%s) -> (%s)", stagingPath, targetPath)
    return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *NodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
    protocol := req.VolumeContext["protocol"]
    if protocol == "lustre" {
        return ns.nodePublishLustre(ctx, req)
    }

    volumeId, targetPath := req.GetVolumeId(), req.GetTargetPath()

    if pool, ok := req.VolumeContext["poolName"]; ok && pool != "" {
        ns.Driver.PoolName = pool
    }

    pvcName := req.VolumeContext["pvcName"]
    pvcNS := req.VolumeContext["pvcNS"]
    podName := req.VolumeContext["csi.storage.k8s.io/pod.name"]
    podNS := req.VolumeContext["csi.storage.k8s.io/pod.namespace"]
    stagingOptions, err := ns.getMountOptions(ctx, podName, podNS)
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

    source := fmt.Sprintf("%s:%s",svip,req.VolumeContext["baseDir"])

    stagingGroupPath := ns.getNfsGroupPath(ctx,req,pvcName,pvcNS,stagingOptions)

    if volumeId == "" || targetPath == "" || stagingGroupPath == "" {
        return nil, status.Error(codes.InvalidArgument,
                "InvalidArgument: Please check volume ID, target path and group path.")
    }

    log.Infof("volumeId(%s) targetPath(%s) stagingGrouptPath(%s)", volumeId, targetPath, stagingGroupPath)

    log.Infof("PVC(%s), Mount Options(%v)",pvcName, stagingOptions)

    err = ns.mountNfsGroup(ns.Mounter.Interface, source, stagingGroupPath, stagingOptions)
    if err != nil {
        return nil, status.Error(codes.Internal, err.Error())
    }

    err = ns.saveNfsVolMeta(volumeId,targetPath,stagingGroupPath,source,stagingOptions)
    if err != nil {
        return nil, status.Error(codes.Internal, err.Error())
    }

    if req.GetVolumeCapability() == nil {
        return nil, status.Error(codes.InvalidArgument, "Volume capability missing in request")
    }

    notMount, err := createTargetMountPath(ns.Mounter.Interface, targetPath, false)
    if err != nil {
        return nil, status.Error(codes.Internal, err.Error())
    }
    if !notMount {
        return &csi.NodePublishVolumeResponse{}, nil
    }

    var bindOptions []string

    readonly := slices.Contains(stagingOptions,"ro")

    bindOptions = []string{"bind", "rprivate"}

    notMount, err = ns.Mounter.Interface.IsLikelyNotMountPoint(targetPath)
    if err != nil {
        return nil, status.Error(codes.Internal, err.Error())
    }

    if notMount {
        if err = ns.Mounter.Interface.Mount(stagingGroupPath, targetPath, "", bindOptions); err != nil  {
            return nil, status.Error(codes.Internal, err.Error())
        }
        if readonly {
            remountOptions := []string{"remount", "ro"}

            if err := ns.Mounter.Interface.Mount(targetPath, targetPath, "", remountOptions); err != nil {
                return nil, status.Error(codes.Internal, err.Error())
            }

            log.Infof("NodePublishVolume: Success ReadOnly Remount (%s) <---> (%s)",targetPath, stagingGroupPath)
        }
    }

    if !notMount {
        if readonly {
            remountOptions := []string{"remount", "ro"}

            if err := ns.Mounter.Interface.Mount(targetPath, targetPath, "", remountOptions); err != nil {
                return nil, status.Error(codes.Internal, err.Error())
            }

            log.Infof("NodePublishVolume: Success ReadOnly Remount (%s) <---> (%s)",targetPath, stagingGroupPath)
        }
    }

    log.Infof("NodePublishVolume: Success Bind (%s) <---> (%s)", targetPath, stagingGroupPath)


    return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *NodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {

    volumeId, targetPath := req.GetVolumeId(), req.GetTargetPath()

    if volumeId == "" {
        return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
    }

    if targetPath == "" {
        return nil, status.Error(codes.InvalidArgument, "target path missing in request")
    }

    if _, err := os.Stat(targetPath); err != nil {
        if os.IsNotExist(err){
            return &csi.NodeUnpublishVolumeResponse{}, nil
        }
        return nil, status.Error(codes.Internal, err.Error())
    }

    notMount, err := mount.IsNotMountPoint(ns.Mounter.Interface, targetPath)
    if err != nil {
        return nil, status.Error(codes.Internal, err.Error())
    }
    if notMount {
        return &csi.NodeUnpublishVolumeResponse{}, nil
    }

    nfsVolMeta, nfsMetaErr := ns.loadNfsVolMeta(volumeId, targetPath)
    if nfsMetaErr != nil {
        // No NFS meta -> Lustre volume: bind unmount only (Stage handles the actual Lustre mount)
        log.Infof("NodeUnpublishVolume[lustre]: unmounting bind target(%s)", targetPath)

        if err := ns.Mounter.Interface.Unmount(targetPath); err != nil {
            return nil, status.Errorf(codes.Internal, "failed to unmount target path %s: %v", targetPath, err)
        }
        if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
            log.Debugf("NodeUnpublishVolume[lustre]: failed to remove target path %s: %v", targetPath, err)
        }

        log.Infof("NodeUnpublishVolume[lustre]: bind unmount succeeded (%s)", targetPath)
        return &csi.NodeUnpublishVolumeResponse{}, nil
    }

    // NFS path: existing logic
    stagingGroupPath := nfsVolMeta.StagingGroupPath
    log.Debugf("NodeServer : stagingGroupPath(%s)",stagingGroupPath)

    if err := ns.Mounter.Interface.Unmount(targetPath); err != nil {
        return nil, status.Error(codes.Internal, err.Error())
    }

    log.Infof("NodeServer : Umount Success TargetPath(%s)",targetPath)

    if err := os.Remove(targetPath); err != nil {
        return nil, status.Error(codes.Internal, "Failed to remove target path.")
    }

    log.Infof("NodeServer : Delete Success TargetPath(%s)",targetPath)

    if err := ns.deleteNfsVolMeta(volumeId, targetPath); err != nil {
        log.Debugf("NodeServer : Delete Fail %s meta file",volumeId)
    }

    bindCount, err := ns.countBindMounts(stagingGroupPath, nfsVolMeta.Source, nfsVolMeta.MountOptions)
    if err != nil {
        log.Debugf("NodeServer : %v",err)
        return &csi.NodeUnpublishVolumeResponse{}, nil
    }

    log.Infof("NodeServer : Bind Count -> %d",bindCount)

    if bindCount == 0 {
        log.Infof("NodeServer : Unmount stagingGroupPath(%s)",stagingGroupPath)
        if err := ns.Mounter.Interface.Unmount(stagingGroupPath); err != nil {
            log.Debugf("NodeServer : Unmount Fail stagingGroupPath(%s)",stagingGroupPath)
        }

        metaDir := fmt.Sprintf("/var/lib/flexa/meta/%s",volumeId)
        if err := os.Remove(metaDir); err != nil {
            log.Debugf("NodeServer : Delete Meta Path %s",metaDir)
        }

        log.Infof("NodeServer : Delete stagingGroupPath(%s)",stagingGroupPath)
        if err := os.RemoveAll(stagingGroupPath); err != nil {
            log.Debugf("NodeServer : Delete Fail %s group mount path", stagingGroupPath)
        }
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

    attrs, _ := ns.lookupPVAttributesByVolumeHandle(ctx, volumeId)
    poolName := ns.Driver.PoolName
    if attrs != nil {
        if p := strings.TrimSpace(attrs["poolName"]); p != "" {
            poolName = p
        }
    }

    fs := volumeFsFromAttrs(attrs)

    proxy, ok := proxyFromVolumeAttributes(attrs)
    if !ok {
        proxyProfile := ""
        if attrs != nil {
            proxyProfile = strings.TrimSpace(attrs[volAttrProxyProfile])
        }
        p, err := ns.Driver.SelectProxy(proxyProfile)
        if err != nil {
            return nil, status.Error(codes.InvalidArgument, err.Error())
        }
        proxy = p
    }
    reqService := service.NewFlexAService()
    reqService.SetFep(proxy)

    statLabel := volumeId
    var k8sVolume *models.K8sVolumeRespSpec
    if fs == "lustre" {
        clusterName := ""
        if attrs != nil {
            clusterName = strings.TrimSpace(attrs["clusterName"])
        }
        if clusterName == "" {
            return nil, status.Error(codes.InvalidArgument,
                "lustre volume requires clusterName in PV volumeAttributes")
        }
        k8sVolume = reqService.GetVolume("lustre", clusterName, volumeId)
    } else {
        if strings.TrimSpace(poolName) == "" {
            return nil, status.Error(codes.InvalidArgument,
                "ZFS volume requires poolName in PV volumeAttributes; node default pool is unset")
        }
        k8sVolume = reqService.GetVolume("zfs", poolName, volumeId)
    }

    if k8sVolume == nil {
        return nil, status.Error(codes.NotFound,
            fmt.Sprintf("Volume[%s] is not found", statLabel))
    }

    notMount, err := mount.IsNotMountPoint(ns.Mounter.Interface, volumePath)
    if err != nil || notMount {
        return nil, status.Error(codes.NotFound,
            fmt.Sprintf("Volume[%s] does not exist on the %s", statLabel, volumePath))
    }

    return &csi.NodeGetVolumeStatsResponse{
            Usage: []*csi.VolumeUsage{
                    &csi.VolumeUsage{
                        Total: k8sVolume.Size,
                        Available: k8sVolume.Free,
                        Used:   k8sVolume.Used,
                        Unit: csi.VolumeUsage_BYTES,
                    },
            },
    }, nil
}


func (ns *NodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
    if req.GetVolumeId() == "" {
        return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
    }
    if req.GetVolumePath() == "" {
        return nil, status.Error(codes.InvalidArgument, "Volume path missing in request")
    }

    // Current driver uses NFS mounts; there is no filesystem resize step here.
    // Return success so the external-resizer can complete the workflow.
    capBytes, err := getSizeByCapacityRange(req.GetCapacityRange())
    if err != nil {
        return nil, err
    }
    return &csi.NodeExpandVolumeResponse{
        CapacityBytes: capBytes,
    }, nil
}

