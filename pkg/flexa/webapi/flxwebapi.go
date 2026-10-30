/*
 * Copyright 2025 Gluesys FlexA Inc.
 */

package webapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	//"csi/pkg/logger"
	log "github.com/sirupsen/logrus"
)

type FEP struct {
	Ip   string
	Port int
	Sid  string
	// MountIP is the VIP resolve reference (e.g. 192.168.0.0/18) when set; else VipResolveRefIP uses Ip only.
	MountIP string
	// Token is the provisiond client token; every request carries it as a Bearer token.
	Token string
}

// apiPrefix is the provisiond namespace for the CSI driver contract.
// provisiond also serves OpenStack drivers, so each caller has its own prefix.
const apiPrefix = "csi"

// apiURL builds the provisiond URL for a contract path such as "zfs/pool/list".
func (fep *FEP) apiURL(cgiPath string) string {
	return fmt.Sprintf("http://%s:%d/%s/%s", fep.Ip, fep.Port, apiPrefix, cgiPath)
}

// newRequest builds a provisiond request with the JSON content type and the client token.
func (fep *FEP) newRequest(method, target string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if fep.Token != "" {
		req.Header.Set("Authorization", "Bearer "+fep.Token)
	}
	return req, nil
}

type Response struct {
	StatusCode int
	ErrorCode  int
	Success    bool
	Data       interface{}
}

func (fep *FEP) sendRequest(data string, apiTemplate interface{}, params url.Values, cgiPath string) (Response, error) {
	resp, err := fep.sendRequestWithoutConnectionCheck(data, apiTemplate, params, cgiPath)

	return resp, err
}

func (fep *FEP) sendRequestWithoutConnectionCheck(data string, apiTemplate interface{}, params url.Values, cgiPath string) (Response, error) {
	var req *http.Request

	// Ex: http://10.12.12.14:9001/csi/zfs/pool/list
	cgiUrl := fep.apiURL(cgiPath)
	log.Infof("Gluesys FlexA Webapi Call Path : %s", cgiUrl)
	baseUrl, err := url.Parse(cgiUrl)
	if err != nil {
		return Response{}, err
	}

	if data != "" {
		jsonMap := make(map[string]string)
		for key, values := range params {
			jsonMap[key] = values[0]
		}

		param, err := json.Marshal(jsonMap)
		if err != nil {
			return Response{}, err
		}

		req, err = fep.newRequest("POST", baseUrl.String(), bytes.NewBuffer(param))
		if err != nil {
			return Response{}, err
		}
	} else {
		baseUrl.RawQuery = params.Encode()
		req, err = fep.newRequest("GET", baseUrl.String(), nil)
		if err != nil {
			return Response{}, err
		}
	}

	return fep.do(req, apiTemplate, cgiPath)
}

// postJSON sends body as JSON. sendRequest only carries flat string fields; this
// one is for bodies with nested objects or booleans (snapshot restore source,
// missingOk).
func (fep *FEP) postJSON(body interface{}, apiTemplate interface{}, cgiPath string) (Response, error) {
	cgiUrl := fep.apiURL(cgiPath)
	log.Infof("Gluesys FlexA Webapi Call Path : %s", cgiUrl)
	param, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	req, err := fep.newRequest("POST", cgiUrl, bytes.NewBuffer(param))
	if err != nil {
		return Response{}, err
	}
	return fep.do(req, apiTemplate, cgiPath)
}

// do sends req and decodes the {data,msg} envelope into apiTemplate.
func (fep *FEP) do(req *http.Request, apiTemplate interface{}, cgiPath string) (Response, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	// For debug print text body
	var bodyText []byte
	bodyText, err = io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}
	s := string(bodyText)
	log.Debugln(s)

	if resp.StatusCode != 200 && resp.StatusCode != 302 {
		return Response{}, newAPIError(resp.StatusCode, bodyText, cgiPath)
	}

	// Strip data json data from response
	type envelop struct {
		Data json.RawMessage `json:"data"`
		Msg  string          `json:"msg"`
	}

	e := envelop{}
	var outResp Response

	if err := json.Unmarshal(bodyText, &e); err != nil {
		return Response{}, err
	}

	if e.Data != nil {
		if err := json.Unmarshal(e.Data, apiTemplate); err != nil {
			return Response{}, err
		}
	}
	outResp.Data = apiTemplate
	outResp.StatusCode = resp.StatusCode
	outResp.Success = true

	return outResp, nil
}
