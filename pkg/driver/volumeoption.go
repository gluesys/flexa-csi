/*
Copyright 2026 Gluesys FlexA Inc.
*/

package driver

import "strings"

// volumeOption returns the PVC annotation value for key. When the PVC does not
// set it, the StorageClass parameter with the same key is used as the default.
func volumeOption(annotations, scParams map[string]string, key string) string {
	if v := strings.TrimSpace(annotations[key]); v != "" {
		return v
	}
	return strings.TrimSpace(scParams[key])
}
