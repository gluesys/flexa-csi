package driver

import "strings"

// volumeFsFromAttrs returns "lustre" or "zfs" from PV volumeAttributes.
// If fs is missing, defaults to "zfs".
func volumeFsFromAttrs(attrs map[string]string) string {
	if attrs != nil {
		if fs := strings.ToLower(strings.TrimSpace(attrs["fs"])); fs != "" {
			if fs == "lustre" {
				return "lustre"
			}
			return "zfs"
		}
	}
	return "zfs"
}
