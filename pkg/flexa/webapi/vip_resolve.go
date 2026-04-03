package webapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type vipResolveData struct {
	Vip string `json:"vip"`
}

// VipResolveRefIP returns the reference for Proxy VIP resolve: mountIP when set (e.g. 192.168.0.0/18), else proxy IP only.
func (fep *FEP) VipResolveRefIP() string {
	if fep == nil {
		return ""
	}
	if c := strings.TrimSpace(fep.MountIP); c != "" {
		return c
	}
	return strings.TrimSpace(fep.Ip)
}

// ResolveZfsVip calls POST /zfs/pool/<poolName>/vip/resolve with body {"ip": refIP}.
func (fep *FEP) ResolveZfsVip(poolName, refIP string) (string, error) {
	if poolName == "" || refIP == "" {
		return "", fmt.Errorf("poolName and refIP are required for ZFS VIP resolve")
	}
	cgiPath := fmt.Sprintf("zfs/pool/%s/vip/resolve", poolName)
	return fep.postVipResolve(cgiPath, refIP)
}

// ResolveLustreVip calls POST /lustre/vip/resolve with body {"ip": refIP}.
func (fep *FEP) ResolveLustreVip(refIP string) (string, error) {
	if refIP == "" {
		return "", fmt.Errorf("refIP is required for Lustre VIP resolve")
	}
	return fep.postVipResolve("lustre/vip/resolve", refIP)
}

func (fep *FEP) postVipResolve(cgiPath, refIP string) (string, error) {
	cgiURL := fmt.Sprintf("http://%s:%d/%s", fep.Ip, fep.Port, cgiPath)
	body := map[string]string{"ip": refIP}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", cgiURL, bytes.NewBuffer(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 && resp.StatusCode != 302 {
		return "", fmt.Errorf("VIP resolve failed: status=%d body=%s", resp.StatusCode, string(raw))
	}

	type envelope struct {
		Data json.RawMessage `json:"data"`
		Msg  string          `json:"msg"`
	}
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return "", err
	}
	var data vipResolveData
	if e.Data != nil {
		if err := json.Unmarshal(e.Data, &data); err != nil {
			return "", err
		}
	}
	if data.Vip == "" {
		return "", fmt.Errorf("VIP resolve returned empty vip: %s", string(raw))
	}
	return data.Vip, nil
}
