package unit

import (
    //csi "github.com/container-storage-interface/spec/lib/go/csi"
    log "github.com/sirupsen/logrus"

    "github.com/gluesys/flexa-csi/pkg/driver"
    "github.com/gluesys/flexa-csi/pkg/flexa/common"
    "github.com/gluesys/flexa-csi/pkg/flexa/service"
)

func GetTestDriver() *driver.Driver {
    csiNode := "CSINode"
    csiEndpoint := "/tmp/csi_test.sock"

    flxService := service.NewFlexAService()

    proxy := common.ProxyInfo{
        Host:       "192.168.7.188",
        Port:       9001,
    }

    flxService.SetFep(&proxy)

    drv, err := driver.NewControllerAndNodeDriver(csiNode, csiEndpoint, flxService)
    drv.PoolName = "AAAA"
    if err != nil {
        log.Errorf("Failed to create driver %v", err)
        return nil
    }

    return drv
}
