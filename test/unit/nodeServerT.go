/*
 * Copyright 2025 Gluesys FlexA Inc.
 */

package unit

import (
    //"os"
    //"os/signal"
    //"syscall"
    "context"
    "fmt"

    //"github.com/spf13/cobra"
    //log "github.com/sirupsen/logrus"
    csi "github.com/container-storage-interface/spec/lib/go/csi"

    "github.com/gluesys/flexa-csi/pkg/driver"
    //"csi/pkg/flexa/common"
    //"csi/pkg/flexa/service"
    //"csi/pkg/logger"
)



func TestPublishVolume(ns *driver.NodeServer) error {
    req := &csi.NodePublishVolumeRequest{
        VolumeId:           "test-vol",
        StagingTargetPath:  "/mnt/csi/test",
        TargetPath:         "/mnt/csi/test",
        VolumeCapability:   &csi.VolumeCapability{
            AccessMode: &csi.VolumeCapability_AccessMode{
                Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
            },
            AccessType: &csi.VolumeCapability_Mount{
                Mount: &csi.VolumeCapability_MountVolume{
                    FsType: "nfs",
                },
            },
        },
        VolumeContext:     map[string]string{
                "protocol":         "nfs",
                "vip":              "192.168.7.188",
                "baseDir":          "/k8s/csi/test-vol",
                "mountPermissions": "0755",
                "poolName":         "AAAA",
                "fs":               "zfs",
        },
    }

    resp, err := ns.NodePublishVolume(context.Background(), req)

    fmt.Println(resp)
    fmt.Println(err)

    return nil
}

func TestUnpublishVolume(ns *driver.NodeServer) error {
    req := &csi.NodeUnpublishVolumeRequest{
        VolumeId:       "test-vol",
        TargetPath:     "/mnt/csi/test",
    }

    resp, err := ns.NodeUnpublishVolume(context.Background(), req)

    fmt.Println(resp)
    fmt.Println(err)

    return nil
}




