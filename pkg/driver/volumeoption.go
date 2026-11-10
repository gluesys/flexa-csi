/*
Copyright 2026 Gluesys FlexA Inc.
*/

package driver

import (
	"fmt"
	"strings"
)

// volumeOptionDefaults are the proxy defaults (legacy proxy and provisiond agree).
// The driver sends them itself because the legacy proxy rejects an empty value
// ("” is not one of ['on', 'off']"): for ZFS volume options at its own input
// check, and for Lustre NFS options at the REST API behind it.
var volumeOptionDefaults = map[string]string{
	"flexa.io/optionISS":          "on",
	"flexa.io/optionSVS":          "off",
	"flexa.io/optionComp":         "off",
	"flexa.io/optionDedup":        "off",
	"flexa.io/nfsAccess":          "RW",
	"flexa.io/nfsNoRootSquashing": "off",
	"flexa.io/nfsInsecure":        "off",
}

// volumeOption returns the PVC annotation value for key. When the PVC does not
// set it, the StorageClass parameter with the same key is used as the default.
func volumeOption(annotations, scParams map[string]string, key string) string {
	if v := strings.TrimSpace(annotations[key]); v != "" {
		return v
	}
	if v := strings.TrimSpace(scParams[key]); v != "" {
		return v
	}
	return volumeOptionDefaults[key]
}

// paramNfsExportMode selects how provisiond exports a ZFS NFS volume:
// "root" puts it under one pool-root export (crossmnt) per secure zone and
// marks the pool CSI-only; "volume" exports each volume. Empty leaves the
// choice to provisiond (prov:config zfsRootExport).
const paramNfsExportMode = "flexa.io/nfsExportMode"

func nfsExportMode(scParams map[string]string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(scParams[paramNfsExportMode]))
	switch v {
	case "", "root", "volume":
		return v, nil
	}
	return "", fmt.Errorf("%s must be root or volume, got %q", paramNfsExportMode, scParams[paramNfsExportMode])
}
