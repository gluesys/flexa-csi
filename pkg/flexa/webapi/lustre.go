package webapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	log "github.com/sirupsen/logrus"
	"github.com/gluesys/flexa-csi/pkg/utils"
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
	Status      string `json:"status"`
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
) (LustreVolumeCreateResponse, error) {
	if clusterName == "" || volName == "" {
		return LustreVolumeCreateResponse{}, fmt.Errorf("clusterName and volName are required")
	}
	if access == "" {
		access = "RW"
	}

	body := map[string]string{
		"sizeValue":             fmt.Sprintf("%d", utils.BytesToMB(size)),
		"sizeUnit":              "MB",
		"zoneName":              zoneName,
		"address":               address,
		"subNetmask":            subnet,
		"access":                access,
		"optionNoRootSquashing": optionNoRootSquashing,
		"optionInsecure":        optionInsecure,
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

func (fep *FEP) lustrePost(cgiPath string, body map[string]string) (LustreVolumeCreateResponse, error) {
	cgiUrl := fmt.Sprintf("http://%s:%d/%s", fep.Ip, fep.Port, cgiPath)
	log.Infof("FlexA Webapi Call Path : %s", cgiUrl)

	payload, err := json.Marshal(body)
	if err != nil {
		return LustreVolumeCreateResponse{}, err
	}

	req, err := http.NewRequest("POST", cgiUrl, bytes.NewBuffer(payload))
	if err != nil {
		return LustreVolumeCreateResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
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
		return LustreVolumeCreateResponse{}, fmt.Errorf("Bad response status code: %d", resp.StatusCode)
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

