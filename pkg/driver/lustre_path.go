// Copyright 2025 FlexA Inc.

package driver

import (
	"fmt"
	"strings"
)

// Lustre volume directories live under this root on the storage controllers
// (provisiond routes/lustre.py MOUNT_ROOT).
const lustreAgentRoot = "/lustre/agent/"

// lustreSubDir is the directory under the filesystem root that holds the
// volumes CSI creates, so they do not mix with other directories at the root.
func lustreSubDir(fs, pvcNS string) string {
	if fs != "lustre" {
		return ""
	}
	if pvcNS == "" {
		return "k8s"
	}
	return "k8s/" + pvcNS
}

// lustreMountSource is the subdirectory mount source of a volume. baseDir is
// the path provisiond returned at creation (with or without a subdirectory);
// volumes without a usable baseDir sit at the filesystem root.
func lustreMountSource(mgsSpec, clusterName, volumeId, baseDir string) string {
	rel := volumeId
	prefix := lustreAgentRoot + clusterName + "/"
	if r, ok := strings.CutPrefix(baseDir, prefix); ok && r != "" && !strings.HasSuffix(r, "/") &&
		!strings.Contains("/"+r+"/", "/../") && !strings.Contains("/"+r+"/", "/./") &&
		!strings.Contains(r, "//") {
		rel = r
	}
	return fmt.Sprintf("%s:/%s/%s", mgsSpec, clusterName, rel)
}
