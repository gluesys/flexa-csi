// Copyright 2025 FlexA Inc.

package models

import (
    "fmt"
)

const (
    //Share definitions
    sharePrefix = "k8s-csi-share"
    sharePathPrefix = "/k8s/csi"

    volumePrefix = "k8s-csi"
)

func GenVolumeName(volumeId string) string{
    return fmt.Sprintf("%s-%s", volumePrefix, volumeId)
}

func GenShareName(volumeId string) string{
    return fmt.Sprintf("%s-%s", sharePrefix, volumeId)
}

func GenSharePath(volumeId string) string {
    return fmt.Sprintf("%s/%s", sharePathPrefix, volumeId)
}
