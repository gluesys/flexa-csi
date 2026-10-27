// Copyright 2025 FlexA Inc.

package driver

import (
	"fmt"
	"regexp"
	"strings"
)

// StorageClass parameter that sets the backend volume/share name.
// Placeholders: {pvname} (provisioner name, pvc-<uid>), {namespace}, {pvcname}.
// Default is "{pvname}".
const paramVolumeNameFormat = "volumeNameFormat"

const defaultVolumeNameFormat = "{pvname}"

// Same rules as provisiond routes (lustre.py VALID_NAME, zfs.py VOLUME_NAME).
var (
	lustreVolumeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	zfsVolumeName    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,127}$`)
)

// renderVolumeName expands format into the backend volume name. {pvname} is
// required so the name stays unique and stable across CreateVolume retries.
func renderVolumeName(format, fs, pvName, pvcNS, pvcName string) (string, error) {
	format = strings.TrimSpace(format)
	if format == "" {
		format = defaultVolumeNameFormat
	}
	if !strings.Contains(format, "{pvname}") {
		return "", fmt.Errorf("%s %q must contain {pvname}", paramVolumeNameFormat, format)
	}
	if (strings.Contains(format, "{namespace}") && pvcNS == "") ||
		(strings.Contains(format, "{pvcname}") && pvcName == "") {
		return "", fmt.Errorf("%s %q needs PVC metadata (csi-provisioner --extra-create-metadata)",
			paramVolumeNameFormat, format)
	}
	name := strings.NewReplacer(
		"{pvname}", pvName,
		"{namespace}", pvcNS,
		"{pvcname}", pvcName,
	).Replace(format)
	if strings.ContainsAny(name, "{}") {
		return "", fmt.Errorf("%s %q has an unknown placeholder", paramVolumeNameFormat, format)
	}

	re := zfsVolumeName
	if fs == "lustre" {
		re = lustreVolumeName
	}
	if !re.MatchString(name) {
		return "", fmt.Errorf("%s %q gives invalid %s volume name %q (allowed: %s)",
			paramVolumeNameFormat, format, fs, name, re.String())
	}
	return name, nil
}
