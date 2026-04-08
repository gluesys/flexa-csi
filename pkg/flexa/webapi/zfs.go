package webapi

import (
    "fmt"
    "net/url"

    "github.com/gluesys/flexa-csi/pkg/utils"
    log "github.com/sirupsen/logrus"
)

// ZfsNfsShareName is the fixed share name segment for NFS export paths (per CSI driver convention).
const ZfsNfsShareName = "share"

type ZfsPoolList struct {
    PoolList     []string   `json:"poolList"`
}

type ZfsVolList struct {
    PoolName    string      `json:"poolName"`
    VolList     []string    `json:"volumeList"`
}

type ZfsPoolInfo struct {
    PoolName    string      `json:"poolName"`
    Total       int64       `json:"total"`
    Used        int64       `json:"used"`
    Free        int64       `json:"free"`
    Status      string      `json:"status"`
}

type ZfsVolInfo struct {
    PoolName    string      `json:"poolName"`
    VolName     string      `json:"volName"`
    Total       int64       `json:"total"`
    Used        int64       `json:"used"`
    Free        int64       `json:"free"`
    Status      string      `json:"status"`
    Vip         string      `json:"vip"`
    BaseDir     string      `json:"sharePath"`
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

    for _, pool := range output.PoolList {
        poolList = append(poolList, pool)
    }

    log.Infof("Gluesys FlexA Call(LisZfsPool) Response : %v", resp)


    return poolList, nil
}

func (fep *FEP) ListZfsVolume(poolName string) ([]string, error) {
    params := url.Values{}

    cgiPath := fmt.Sprintf("zfs/pool/%s/volume/list",poolName)

    var output ZfsVolList

    var volList []string

    resp, err := fep.sendRequest("", &output, params, cgiPath)
    if err != nil {
        return volList, err
    }

    for _, vol := range output.VolList {
        volList = append(volList, vol)
    }

    log.Infof("Gluesys FlexA Call(ListZfsVolume) Response: %v", resp)

    return volList, nil
}

func (fep *FEP) InfoZfsPool(poolName string) (ZfsPoolInfo, error) {
    params := url.Values{}

    cgiPath := fmt.Sprintf("zfs/pool/%s",poolName)

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

    cgiPath := fmt.Sprintf("zfs/pool/%s/volume/%s",poolName, volName)

    var output ZfsVolInfo

    resp, err := fep.sendRequest("", &output, params, cgiPath)
    if err != nil {
        return ZfsVolInfo{}, err
    }

    log.Infof("Gluesys FlexA Call(InfoZfsVol) Response : %v", resp)

    return output, nil
}

// 볼륨 생성 + 공유 생성 
func (fep *FEP) ZfsCreateVolume(size int64, volId string, volName string, poolName string, optionSVS string, optionISS string, optionComp string, optionDedup string)  error {
    params := url.Values{}
    params.Set("sizeValue",fmt.Sprintf("%d",utils.BytesToMB(size)))
    params.Set("sizeUnit", "MB")
    params.Set("optionSVS", optionSVS)
    params.Set("optionISS", optionISS)
    params.Set("optionComp", optionComp)
    params.Set("optionDedup", optionDedup)

    // 볼륨 생성 API 호출
    cgiPath := fmt.Sprintf("zfs/pool/%s/volume/%s/create",poolName, volName)

    type respCreateVolumeSpec struct {
        VolName         string `json:"name"`
        PoolName        string `json:"poolName"`
        Size            string `json:"sizeValue"`
        SizeUnit        string `json:"sizeUnit"`
    }

    var respCreateVolume respCreateVolumeSpec

    resp, err := fep.sendRequest("post",&respCreateVolume, params, cgiPath)
    if err != nil {
        return err
    }

    log.Infof("Gluesys FlexA Call(zfsCreateVolume) Requests : %v", params)
    log.Infof("Gluesys FlexA Call(ZfsCreateVolume) Response : %v", resp)

    return nil
}

func (fep *FEP) ZfsCreateShareNfs(volId string, volName string, poolName string, zoneName string, address string, subnet string, access string, optionNoRootSquashing string, optionInsecure string) (string, error) {
    params := url.Values{}
    params.Set("zoneName",zoneName)
    params.Set("address",address)
    params.Set("subNetmask",subnet)
    params.Set("access",access)
    params.Set("optionNoRootSquashing",optionNoRootSquashing)
    params.Set("optionInsecure",optionInsecure)


    // 공유 생성 API 호출 — share name is fixed "share" (volume id = volName, no extra prefix).
    shareName := ZfsNfsShareName
    sharePath := fmt.Sprintf("/%s/%s/%s", poolName, volName, shareName)
    params.Set("path", sharePath)

    cgiPath := fmt.Sprintf("share/pool/%s/volume/%s/%s/create", poolName, volName, shareName)

    type respCreateShareSpec struct {
        ShareName       string `json:"shareName"`
        ZoneName        string `json:"zoneName"`
        Address         string `json:"address"`
        SubnetMask      string `json:"subNetMasK"`
        Access          string `json:"access"`
        OptionNoRoot    string `json:"optionNoRootSquashing"`
        optionInsecure  string `json:"optionInsecure"`
    }

    var respCreateShare respCreateShareSpec

    resp, err := fep.sendRequest("post",&respCreateShare, params, cgiPath)
    if err != nil {
        return "", err
    }

    log.Infof("Gluesys FlexA Call(zfsCreateShareNfs) Requests : %v", params)
    log.Infof("Gluesys FlexA Call(ZfsCreateShareNfs) Response : %v", resp)

    return sharePath, nil
}

func (fep *FEP) ZfsDeleteVolume(volName string, shareName string, poolName string) error {
    // 공유 삭제
    params := url.Values{}

    cgiPath := fmt.Sprintf("share/pool/%s/volume/%s/%s/delete", poolName, volName, shareName)

    type respDeleteSpec struct {
        name        string `json:"name"`
     }

    var respDelete respDeleteSpec

    resp, err := fep.sendRequest("post",&respDelete, params, cgiPath)
    if err != nil {
        return err
    }

    // 볼륨 삭제
    cgiPath = fmt.Sprintf("zfs/pool/%s/volume/%s/delete",poolName, volName)

    resp, err = fep.sendRequest("post", &respDelete, params, cgiPath)
    if err != nil {
        return err
    }

    log.Infof("Gluesys FlexA Call(ZfsDeleteVolume) Response : %v", resp)

    return nil
}

func (fep *FEP) ZfsExpandVolume(poolName string, volName string, newSizeBytes int64) error {
    if poolName == "" || volName == "" {
        return fmt.Errorf("poolName and volName are required")
    }
    if newSizeBytes <= 0 {
        return fmt.Errorf("newSizeBytes must be positive")
    }

    params := url.Values{}
    params.Set("sizeValue", fmt.Sprintf("%d", utils.BytesToMB(newSizeBytes)))
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


