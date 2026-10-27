package webapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/gluesys/flexa-csi/pkg/utils"
	log "github.com/sirupsen/logrus"
)

type LustreVolumeCreateResponse struct {
	ClusterName string `json:"clusterName"`
	VolName     string `json:"volName"`
	VolumeId    string `json:"volumeId"`
	Path        string `json:"path"`
	QuotaMb     int64  `json:"quotaMb"`
}

type LustreVolumeInfoResponse struct {
	ClusterName string `json:"clusterName"`
	VolName     string `json:"volName"`
	VolumeId    string `json:"volumeId"`
	Path        string `json:"path"`
	Quota       struct {
		LimitMb int64 `json:"limitMb"`
		UsedMb  int64 `json:"usedMb"`
		FreeMb  int64 `json:"freeMb"`
	} `json:"quota"`
	Cluster struct {
		TotalMb    int64 `json:"totalMb"`
		QuotaSumMb int64 `json:"quotaSumMb"`
		FreeMb     int64 `json:"freeMb"`
	} `json:"cluster"`
	Status string `json:"status"`
}

func (fep *FEP) LustreCreateVolume(
	size int64,
	clusterName string,
	volName string,
	zoneName string,
	address string,
	subnet string,
	access string,
	optionNoRootSquashing string,
	optionInsecure string,
	pvcQos string,
	noExport bool,
	subDir string,
) (LustreVolumeCreateResponse, error) {
	if clusterName == "" || volName == "" {
		return LustreVolumeCreateResponse{}, fmt.Errorf("clusterName and volName are required")
	}
	if access == "" {
		access = "RW"
	}

	body := map[string]string{
		"sizeValue":             fmt.Sprintf("%d", utils.BytesToMBCeil(size)),
		"sizeUnit":              "MB",
		"zoneName":              zoneName,
		"address":               address,
		"subNetmask":            subnet,
		"access":                access,
		"optionNoRootSquashing": optionNoRootSquashing,
		"optionInsecure":        optionInsecure,
	}
	if s := strings.TrimSpace(pvcQos); s != "" {
		body["pvcQos"] = s
	}
	if noExport {
		// Native mounts do not use NFS. provisiond without this option ignores it and exports.
		body["export"] = "off"
	}
	if subDir != "" {
		// provisiond without this option ignores it and creates the volume at the
		// filesystem root; callers use the returned path, so both work.
		body["subDir"] = subDir
	}

	cgiPath := fmt.Sprintf("lustre/cluster/%s/volume/%s/create", clusterName, volName)
	return fep.lustrePost(cgiPath, body)
}

func (fep *FEP) LustreDeleteVolume(clusterName string, volName string) error {
	if clusterName == "" || volName == "" {
		return fmt.Errorf("clusterName and volName are required")
	}
	cgiPath := fmt.Sprintf("lustre/cluster/%s/volume/%s/delete", clusterName, volName)
	_, err := fep.lustrePost(cgiPath, map[string]string{})
	if IsMissing(err, "Share("+volName+")") {
		log.Infof("Gluesys FlexA Call(LustreDeleteVolume) : %s already gone: %v", volName, err)
		return nil
	}
	return err
}

func (fep *FEP) LustreInfoVolume(clusterName string, volName string) (LustreVolumeInfoResponse, error) {
	if clusterName == "" || volName == "" {
		return LustreVolumeInfoResponse{}, fmt.Errorf("clusterName and volName are required")
	}

	params := url.Values{}
	cgiPath := fmt.Sprintf("lustre/cluster/%s/volume/%s", clusterName, volName)

	var output LustreVolumeInfoResponse
	_, err := fep.sendRequest("", &output, params, cgiPath)
	if err != nil {
		return LustreVolumeInfoResponse{}, err
	}
	return output, nil
}

type LustreClusterListResponse struct {
	ClusterList []string `json:"clusterList"`
}

// LustreListCluster lists all Lustre cluster names known to the proxy.
func (fep *FEP) LustreListCluster() ([]string, error) {
	params := url.Values{}
	cgiPath := "lustre/cluster/list"

	var output LustreClusterListResponse
	_, err := fep.sendRequest("", &output, params, cgiPath)
	if err != nil {
		return nil, err
	}
	log.Infof("Gluesys FlexA Call(LustreListCluster) : clusters=%v", output.ClusterList)
	return output.ClusterList, nil
}

type LustreVolumeListResponse struct {
	ClusterName string   `json:"clusterName"`
	VolumeList  []string `json:"volumeList"`
}

// LustreListVolume lists CSI volume (share) names under a Lustre cluster.
func (fep *FEP) LustreListVolume(clusterName string) ([]string, error) {
	if clusterName == "" {
		return nil, fmt.Errorf("clusterName is required")
	}
	params := url.Values{}
	cgiPath := fmt.Sprintf("lustre/cluster/%s/volume/list", clusterName)

	var output LustreVolumeListResponse
	_, err := fep.sendRequest("", &output, params, cgiPath)
	if err != nil {
		return nil, err
	}
	log.Infof("Gluesys FlexA Call(LustreListVolume) : cluster=%s volumes=%v", clusterName, output.VolumeList)
	return output.VolumeList, nil
}

type LustreClusterInfoResponse struct {
	ClusterName string `json:"clusterName"`
	Total       int64  `json:"total"`
	QuotaSum    int64  `json:"quotaSum"`
	Free        int64  `json:"free"`
}

// LustreClusterInfo returns cluster-level capacity (Mb) for GetCapacity.
func (fep *FEP) LustreClusterInfo(clusterName string) (LustreClusterInfoResponse, error) {
	if clusterName == "" {
		return LustreClusterInfoResponse{}, fmt.Errorf("clusterName is required")
	}
	params := url.Values{}
	cgiPath := fmt.Sprintf("lustre/cluster/%s", clusterName)

	var output LustreClusterInfoResponse
	_, err := fep.sendRequest("", &output, params, cgiPath)
	if err != nil {
		return LustreClusterInfoResponse{}, err
	}
	log.Infof("Gluesys FlexA Call(LustreClusterInfo) : cluster=%s total=%d free=%d", clusterName, output.Total, output.Free)
	return output, nil
}

type LustreMgsNidResponse struct {
	NidList []string `json:"nidList"`
}

func (fep *FEP) LustreMgsNid() ([]string, error) {
	cgiPath := "lustre/cluster/mgs/nid"
	var output LustreMgsNidResponse
	_, err := fep.sendRequest("", &output, url.Values{}, cgiPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get MGS NID list: %w", err)
	}
	if len(output.NidList) == 0 {
		return nil, fmt.Errorf("no MGS NID available from proxy")
	}
	log.Infof("Gluesys FlexA Call(LustreMgsNid) : nidList=%v", output.NidList)
	return output.NidList, nil
}

func (fep *FEP) LustreExpandVolume(clusterName string, volName string, newSizeBytes int64) error {
	if clusterName == "" || volName == "" {
		return fmt.Errorf("clusterName and volName are required")
	}
	if newSizeBytes <= 0 {
		return fmt.Errorf("newSizeBytes must be positive")
	}
	body := map[string]string{
		"sizeValue": fmt.Sprintf("%d", utils.BytesToMBCeil(newSizeBytes)),
		"sizeUnit":  "MB",
	}
	cgiPath := fmt.Sprintf("lustre/cluster/%s/volume/%s/expand", clusterName, volName)
	_, err := fep.lustrePost(cgiPath, body)
	return err
}

func (fep *FEP) lustrePost(cgiPath string, body map[string]string) (LustreVolumeCreateResponse, error) {
	cgiUrl := fep.apiURL(cgiPath)
	log.Infof("Gluesys FlexA Webapi Call Path : %s", cgiUrl)

	payload, err := json.Marshal(body)
	if err != nil {
		return LustreVolumeCreateResponse{}, err
	}

	req, err := fep.newRequest("POST", cgiUrl, bytes.NewBuffer(payload))
	if err != nil {
		return LustreVolumeCreateResponse{}, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return LustreVolumeCreateResponse{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return LustreVolumeCreateResponse{}, err
	}
	log.Debugln(string(raw))

	if resp.StatusCode != 200 && resp.StatusCode != 302 {
		return LustreVolumeCreateResponse{}, newAPIError(resp.StatusCode, raw, cgiPath)
	}

	type envelope struct {
		Data json.RawMessage `json:"data"`
		Msg  string          `json:"msg"`
	}
	e := envelope{}
	if err := json.Unmarshal(raw, &e); err != nil {
		return LustreVolumeCreateResponse{}, err
	}

	var out LustreVolumeCreateResponse
	if e.Data != nil {
		if err := json.Unmarshal(e.Data, &out); err != nil {
			return LustreVolumeCreateResponse{}, err
		}
	}
	return out, nil
}
