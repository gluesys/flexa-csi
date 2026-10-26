package webapi

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/gluesys/flexa-csi/pkg/utils"
	log "github.com/sirupsen/logrus"
)

// LegacyZfsNfsShareName is used by PVs created before per-volume share names.
const LegacyZfsNfsShareName = "share"

// ZfsNfsShareName returns a globally unique share name for a CSI volume.
func ZfsNfsShareName(volumeID string) string {
	if strings.TrimSpace(volumeID) == "" {
		return LegacyZfsNfsShareName
	}
	return volumeID
}

type ZfsPoolList struct {
	PoolList []string `json:"poolList"`
}

type ZfsVolList struct {
	PoolName string   `json:"poolName"`
	VolList  []string `json:"volumeList"`
}

// MbInt is an int64 that also accepts a JSON string holding a number. The
// proxy's zfs pool_info builds "total" with str() and passes "used"/"free"
// through from Redis as-is, so the same field may arrive quoted or unquoted.
type MbInt int64

func (m *MbInt) UnmarshalJSON(b []byte) error {
	var raw json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	text := strings.TrimSpace(string(raw))
	if text == "null" {
		*m = 0
		return nil
	}
	if unquoted, err := strconv.Unquote(text); err == nil {
		text = strings.TrimSpace(unquoted)
	}
	if text == "" {
		*m = 0
		return nil
	}
	v, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(text, 64)
		if ferr != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("MbInt: cannot parse %q as a number: %w", text, err)
		}
		v = int64(f)
	}
	*m = MbInt(v)
	return nil
}

// ZfsPoolInfo mirrors zfs pool_info: total/used/free are in MB.
type ZfsPoolInfo struct {
	PoolName string `json:"poolName"`
	Total    MbInt  `json:"total"`
	Used     MbInt  `json:"used"`
	Free     MbInt  `json:"free"`
	Status   string `json:"status"`
}

type ZfsVolInfo struct {
	PoolName  string `json:"poolName"`
	VolName   string `json:"volName"`
	Total     int64  `json:"total"`
	Used      int64  `json:"used"`
	Free      int64  `json:"free"`
	Status    string `json:"status"`
	Vip       string `json:"vip"`
	BaseDir   string `json:"sharePath"`
	ShareName string `json:"shareName"`
}

func (fep *FEP) ListZfsPool() ([]string, error) {
	params := url.Values{}

	cgiPath := "zfs/pool/list"

	var output ZfsPoolList

	var poolList []string

	resp, err := fep.sendRequest("", &output, params, cgiPath)
	if err != nil {
		return poolList, err
	}

	poolList = append(poolList, output.PoolList...)

	log.Infof("Gluesys FlexA Call(LisZfsPool) Response : %v", resp)

	return poolList, nil
}

func (fep *FEP) ListZfsVolume(poolName string) ([]string, error) {
	params := url.Values{}

	cgiPath := fmt.Sprintf("zfs/pool/%s/volume/list", poolName)

	var output ZfsVolList

	var volList []string

	resp, err := fep.sendRequest("", &output, params, cgiPath)
	if err != nil {
		return volList, err
	}

	volList = append(volList, output.VolList...)

	log.Infof("Gluesys FlexA Call(ListZfsVolume) Response: %v", resp)

	return volList, nil
}

func (fep *FEP) InfoZfsPool(poolName string) (ZfsPoolInfo, error) {
	params := url.Values{}

	cgiPath := fmt.Sprintf("zfs/pool/%s", poolName)

	var output ZfsPoolInfo

	resp, err := fep.sendRequest("", &output, params, cgiPath)
	if err != nil {
		return ZfsPoolInfo{}, err
	}

	log.Infof("Gluesys FlexA Call(InfoZfsPool) Response : %v", resp)

	return output, nil
}

func (fep *FEP) InfoZfsVol(poolName string, volName string) (ZfsVolInfo, error) {
	params := url.Values{}

	cgiPath := fmt.Sprintf("zfs/pool/%s/volume/%s", poolName, volName)

	var output ZfsVolInfo

	resp, err := fep.sendRequest("", &output, params, cgiPath)
	if err != nil {
		return ZfsVolInfo{}, err
	}

	log.Infof("Gluesys FlexA Call(InfoZfsVol) Response : %v", resp)

	return output, nil
}

// 볼륨 생성 + 공유 생성
func (fep *FEP) ZfsCreateVolume(size int64, volId string, volName string, poolName string, optionSVS string, optionISS string, optionComp string, optionDedup string) error {
	params := url.Values{}
	params.Set("sizeValue", fmt.Sprintf("%d", utils.BytesToMBCeil(size)))
	params.Set("sizeUnit", "MB")
	params.Set("optionSVS", optionSVS)
	params.Set("optionISS", optionISS)
	params.Set("optionComp", optionComp)
	params.Set("optionDedup", optionDedup)

	// 볼륨 생성 API 호출
	cgiPath := fmt.Sprintf("zfs/pool/%s/volume/%s/create", poolName, volName)

	type respCreateVolumeSpec struct {
		VolName  string `json:"name"`
		PoolName string `json:"poolName"`
		Size     string `json:"sizeValue"`
		SizeUnit string `json:"sizeUnit"`
	}

	var respCreateVolume respCreateVolumeSpec

	resp, err := fep.sendRequest("post", &respCreateVolume, params, cgiPath)
	if err != nil {
		return err
	}

	log.Infof("Gluesys FlexA Call(zfsCreateVolume) Requests : %v", params)
	log.Infof("Gluesys FlexA Call(ZfsCreateVolume) Response : %v", resp)

	return nil
}

// shareDir is the directory inside the volume that is exported; "" means the
// share name. A restored volume exports the directory copied from its source.
func (fep *FEP) ZfsCreateShareNfs(volId string, volName string, poolName string, shareDir string, zoneName string, address string, subnet string, access string, optionNoRootSquashing string, optionInsecure string) (string, error) {
	params := url.Values{}
	params.Set("zoneName", zoneName)
	params.Set("address", address)
	params.Set("subNetmask", subnet)
	params.Set("access", access)
	params.Set("optionNoRootSquashing", optionNoRootSquashing)
	params.Set("optionInsecure", optionInsecure)

	// Share names are global in FlexA, so each CSI volume needs its own name.
	shareName := ZfsNfsShareName(volId)
	if shareDir == "" {
		shareDir = shareName
	}
	sharePath := fmt.Sprintf("/%s/%s/%s", poolName, volName, shareDir)
	params.Set("path", sharePath)

	cgiPath := fmt.Sprintf("share/pool/%s/volume/%s/%s/create", poolName, volName, shareName)

	type respCreateShareSpec struct {
		ShareName    string `json:"shareName"`
		ZoneName     string `json:"zoneName"`
		Address      string `json:"address"`
		SubnetMask   string `json:"subNetMasK"`
		Access       string `json:"access"`
		OptionNoRoot string `json:"optionNoRootSquashing"`
	}

	var respCreateShare respCreateShareSpec

	resp, err := fep.sendRequest("post", &respCreateShare, params, cgiPath)
	if err != nil {
		return "", err
	}

	log.Infof("Gluesys FlexA Call(zfsCreateShareNfs) Requests : %v", params)
	log.Infof("Gluesys FlexA Call(ZfsCreateShareNfs) Response : %v", resp)

	return sharePath, nil
}

// ZfsDeleteVolume removes the NFS share and then the dataset. A part that is
// already gone counts as deleted, so a retry after a partial failure finishes
// the job instead of failing on the first step forever.
func (fep *FEP) ZfsDeleteVolume(volName string, shareName string, poolName string) error {
	// Destroying the dataset destroys its snapshots too, so a volume with
	// VolumeSnapshots stays (share included) until they are deleted.
	// provisiond refuses the dataset delete as well; checking first keeps the
	// share. A failed check is not fatal for that reason.
	snaps, err := fep.ZfsCsiSnapshots(poolName, volName)
	if err != nil {
		log.Warnf("Gluesys FlexA Call(ZfsDeleteVolume) : snapshot check of %s failed: %v", volName, err)
	}
	if len(snaps) > 0 {
		return &HasSnapshotsError{Volume: volName, Snapshots: snaps}
	}
	if err := fep.ZfsDeleteShare(volName, shareName, poolName); err != nil {
		return err
	}
	return fep.ZfsDestroyVolume(volName, poolName)
}

func (fep *FEP) ZfsDeleteShare(volName string, shareName string, poolName string) error {
	cgiPath := fmt.Sprintf("share/pool/%s/volume/%s/%s/delete", poolName, volName, shareName)

	var out map[string]interface{}
	_, err := fep.sendRequest("post", &out, url.Values{}, cgiPath)
	// Without the volume there is no share either; the volume step checks again.
	if IsMissing(err, shareName, "Share("+shareName+")", "Volume("+volName+")") {
		log.Infof("Gluesys FlexA Call(ZfsDeleteShare) : %s already gone: %v", shareName, err)
		return nil
	}
	return err
}

func (fep *FEP) ZfsDestroyVolume(volName string, poolName string) error {
	cgiPath := fmt.Sprintf("zfs/pool/%s/volume/%s/delete", poolName, volName)

	var out map[string]interface{}
	resp, err := fep.sendRequest("post", &out, url.Values{}, cgiPath)
	if IsMissing(err, "Volume("+volName+")") {
		log.Infof("Gluesys FlexA Call(ZfsDestroyVolume) : %s already gone: %v", volName, err)
		return nil
	}
	if err != nil {
		return err
	}

	log.Infof("Gluesys FlexA Call(ZfsDeleteVolume) Response : %v", resp)

	return nil
}

// ZfsSharePath returns the export path of an existing share. The legacy proxy's
// volume info carries no share path, so a retried create needs this lookup.
func (fep *FEP) ZfsSharePath(poolName string, volName string, shareName string) (string, error) {
	cgiPath := fmt.Sprintf("share/pool/%s/volume/%s/%s", poolName, volName, shareName)

	var out struct {
		Path string `json:"path"`
	}
	if _, err := fep.sendRequest("", &out, url.Values{}, cgiPath); err != nil {
		return "", err
	}
	if out.Path == "" {
		return "", fmt.Errorf("share %s has no path", shareName)
	}
	return out.Path, nil
}

func (fep *FEP) ZfsExpandVolume(poolName string, volName string, newSizeBytes int64) error {
	if poolName == "" || volName == "" {
		return fmt.Errorf("poolName and volName are required")
	}
	if newSizeBytes <= 0 {
		return fmt.Errorf("newSizeBytes must be positive")
	}

	params := url.Values{}
	params.Set("sizeValue", fmt.Sprintf("%d", utils.BytesToMBCeil(newSizeBytes)))
	params.Set("sizeUnit", "MB")

	cgiPath := fmt.Sprintf("zfs/pool/%s/volume/%s/expand", poolName, volName)

	// Proxy returns envelope {data,msg}; expand doesn't require a typed response payload here.
	var out map[string]interface{}
	_, err := fep.sendRequest("post", &out, params, cgiPath)
	if err != nil {
		return err
	}

	log.Infof("Gluesys FlexA Call(ZfsExpandVolume) Requests : %v", params)
	return nil
}
