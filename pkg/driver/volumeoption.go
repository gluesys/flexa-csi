/*
Copyright 2026 Gluesys FlexA Inc.
*/

package driver

import "strings"

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
