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
	client := &http.Client{}
	var req *http.Request

	// Ex: http://10.12.12.14:9001/
	cgiUrl := fmt.Sprintf("http://%s:%d/%s", fep.Ip, fep.Port, cgiPath)
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

		req, err = http.NewRequest("POST", baseUrl.String(), bytes.NewBuffer(param))
		if err != nil {
			return Response{}, err
		}
	} else {
		baseUrl.RawQuery = params.Encode()
		req, err = http.NewRequest("GET", baseUrl.String(), nil)
		if err != nil {
			return Response{}, err
		}
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	// For debug print text body
	var bodyText []byte
	bodyText, err = io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}
	s := string(bodyText)
	log.Debugln(s)

	if resp.StatusCode != 200 && resp.StatusCode != 302 {
		return Response{}, fmt.Errorf("Bad response status code: %d", resp.StatusCode)
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
	fmt.Println(string(e.Data))
	outResp.Data = apiTemplate
	outResp.StatusCode = resp.StatusCode
	outResp.Success = true

	return outResp, nil
}
