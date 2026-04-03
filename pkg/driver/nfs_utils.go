package driver

import (
    "os"
    log "github.com/sirupsen/logrus"
)

func chmodIfPermissionMismatch(targetPath string, mode os.FileMode) error {
    info, err := os.Lstat(targetPath)
    if err != nil {
        return err
    }

    perm := info.Mode() & os.ModePerm
    if perm != mode {
        log.Infof("chmod targetPath(%s, mode:0%o) with permissions(0%o)", targetPath, info.Mode(), mode)
        if err := os.Chmod(targetPath, mode); err != nil {
            return err
        }
    } else {
        log.Infof("skip chmod on targetPath(%s) since mode is already 0%o)", targetPath, info.Mode())
    }

    return nil
}
