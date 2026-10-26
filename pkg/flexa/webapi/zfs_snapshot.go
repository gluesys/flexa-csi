/*
 * Copyright 2026 Gluesys FlexA Inc.
 */

package webapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gluesys/flexa-csi/pkg/utils"
	log "github.com/sirupsen/logrus"
)

// ZfsSnapshot is a snapshot of a ZFS volume on provisiond.
type ZfsSnapshot struct {
	VolName    string `json:"volName"`
	SnapName   string `json:"snapName"`
	CreateTime string `json:"createTime"` // Unix seconds as a string
	SizeMb     string `json:"sizeMb"`     // volume size when taken ("" if unknown)
}

func snapshotPath(poolName, volName string) string {
	return fmt.Sprintf("snapshot/zfs/pool/%s/volume/%s/snapshot", poolName, volName)
}

// ZfsCreateSnapshot creates pool/vol@snapName. provisiond answers a repeated
// request with the snapshot it already made, so a retry is safe.
func (fep *FEP) ZfsCreateSnapshot(poolName, volName, snapName string) (ZfsSnapshot, error) {
	var out ZfsSnapshot
	_, err := fep.postJSON(map[string]string{"name": snapName}, &out,
		snapshotPath(poolName, volName)+"/create")
	if err != nil {
		return ZfsSnapshot{}, err
	}
	log.Infof("Gluesys FlexA Call(ZfsCreateSnapshot) : %s/%s@%s", poolName, volName, out.SnapName)
	return out, nil
}

// ZfsDeleteSnapshot removes pool/vol@snapName. A snapshot or volume that is
// already gone counts as deleted.
func (fep *FEP) ZfsDeleteSnapshot(poolName, volName, snapName string) error {
	var out map[string]interface{}
	_, err := fep.postJSON(map[string]bool{"missingOk": true}, &out,
		fmt.Sprintf("%s/%s/delete", snapshotPath(poolName, volName), snapName))
	if err != nil {
		return err
	}
	log.Infof("Gluesys FlexA Call(ZfsDeleteSnapshot) : %s/%s@%s %v", poolName, volName, snapName, out)
	return nil
}

// ZfsCreateVolumeFromSnapshot creates volName as an independent copy of
// pool/srcVol@srcSnap, sized and optioned like ZfsCreateVolume.
func (fep *FEP) ZfsCreateVolumeFromSnapshot(size int64, volName, poolName, optionSVS, optionISS, optionComp, optionDedup, srcVol, srcSnap string) error {
	body := map[string]interface{}{
		"sizeValue":   fmt.Sprintf("%d", utils.BytesToMBCeil(size)),
		"sizeUnit":    "MB",
		"optionSVS":   optionSVS,
		"optionISS":   optionISS,
		"optionComp":  optionComp,
		"optionDedup": optionDedup,
		"source":      map[string]string{"volume": srcVol, "snap": srcSnap},
	}
	var out map[string]interface{}
	resp, err := fep.postJSON(body, &out, fmt.Sprintf("zfs/pool/%s/volume/%s/create", poolName, volName))
	if err != nil {
		return err
	}
	log.Infof("Gluesys FlexA Call(ZfsCreateVolumeFromSnapshot) : %s from %s@%s: %v", volName, srcVol, srcSnap, resp)
	return nil
}

// IsHasSnapshots reports provisiond refusing to delete a volume that still has
// CSI snapshots (deleting it would destroy them).
func IsHasSnapshots(err error) bool {
	var h *HasSnapshotsError
	if errors.As(err, &h) {
		return true
	}
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusConflict && e.Code == "HAS_SNAPSHOTS"
}

// IsTooSmall reports a restore asked smaller than the snapshot's volume.
func IsTooSmall(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusBadRequest && e.Code == "TOO_SMALL"
}

// ZfsCsiSnapshots lists the CSI snapshots of pool/vol. A missing volume has none.
func (fep *FEP) ZfsCsiSnapshots(poolName, volName string) ([]string, error) {
	var out struct {
		Snapshots []struct {
			SnapID      string `json:"snapId"`
			SnapCreater string `json:"snapCreater"`
		} `json:"snapshots"`
	}
	_, err := fep.sendRequest("", &out, nil, snapshotPath(poolName, volName)+"/list")
	if IsMissing(err, "Volume("+volName+")") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, s := range out.Snapshots {
		if s.SnapCreater == "csi" {
			names = append(names, s.SnapID)
		}
	}
	return names, nil
}

// HasSnapshotsError is returned when a volume cannot be deleted because CSI
// snapshots of it remain.
type HasSnapshotsError struct {
	Volume    string
	Snapshots []string
}

func (e *HasSnapshotsError) Error() string {
	return fmt.Sprintf("volume %s has CSI snapshots: %s", e.Volume, strings.Join(e.Snapshots, ", "))
}
