/*
 * Copyright 2021 Synology Inc.
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


func TestCreateVolume(ctrl *driver.ControllerServer) error {
    req := &csi.CreateVolumeRequest{
        Name:           "test-vol",
        CapacityRange:  &csi.CapacityRange{
                RequiredBytes:      1024*1024*1024*2,
        },
        VolumeCapabilities:     []*csi.VolumeCapability{
                {
                    AccessMode: &csi.VolumeCapability_AccessMode{
                        Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
                    },
                    AccessType: &csi.VolumeCapability_Mount{
                        Mount: &csi.VolumeCapability_MountVolume{},
                    },
                },
        },
        Parameters:     map[string]string{
                "protocol":         "",
                "formatOptions":    "",
                "mountPermissions": "",
                "poolName":         "AAAA",
                "fs":               "zfs",
        },
    }

    resp, err := ctrl.CreateVolume(context.Background(), req)

    fmt.Println(resp)
    fmt.Println(err)

    return nil
}

func TestDeleteVolume(ctrl *driver.ControllerServer) error {
    req := &csi.DeleteVolumeRequest{
        VolumeId: "test-vol",
    }

    resp, err := ctrl.DeleteVolume(context.Background(), req)

    fmt.Println(resp)
    fmt.Println(err)

    return nil
}




