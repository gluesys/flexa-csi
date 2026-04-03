package main

import (
    "os"
    "github.com/gluesys/flexa-csi/test/unit"
    //log "github.com/sirupsen/logrus"

    "github.com/gluesys/flexa-csi/pkg/driver"
)


func main() {
    drv := unit.GetTestDriver()

    //cs := driver.NewControllerServer(drv)

    ns := driver.NewNodeServer(drv)

    if err := unit.TestUnpublishVolume(ns); err != nil {
        os.Exit(1)
    }

    os.Exit(0)
}
