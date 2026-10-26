/*
 * Copyright 2025 Gluesys FlexA Inc.
 */

package webapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultRequestTimeout bounds one proxy call. The legacy REST proxy runs Ansible
// and can take minutes under load, so the default is generous.
const DefaultRequestTimeout = 5 * time.Minute

// The request context is deliberately not tied to the gRPC deadline: cancelling
// the HTTP call does not stop the backend, and a retry would then race the
// still-running operation. The controller holds the per-volume lock until the
// proxy answers or this timeout fires instead.
var httpClient = &http.Client{Timeout: DefaultRequestTimeout}

// SetRequestTimeout changes the per-call proxy timeout (0 keeps the default).
func SetRequestTimeout(d time.Duration) {
	if d > 0 {
		httpClient.Timeout = d
	}
}

// APIError is a non-2xx proxy response.
type APIError struct {
	Status int
	Msg    string
	Path   string
}

func (e *APIError) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("proxy %s: status %d", e.Path, e.Status)
	}
	return fmt.Sprintf("proxy %s: status %d: %s", e.Path, e.Status, e.Msg)
}

// newAPIError keeps the proxy's "msg" so the caller sees why the call failed.
func newAPIError(status int, body []byte, path string) *APIError {
	var e struct {
		Msg string `json:"msg"`
	}
	msg := ""
	if json.Unmarshal(body, &e) == nil {
		msg = e.Msg
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 512 {
			msg = msg[:512]
		}
	}
	return &APIError{Status: status, Msg: msg, Path: path}
}

// IsMissing reports a proxy answer saying that the named object does not
// exist. Both the legacy proxy and provisiond answer 409 (ZFS/share) or 404 (Lustre)
// with "<what> does not exist" in msg. The match is per object on purpose: the
// legacy proxy reads existence from a monitor snapshot, so a transient
// "Pool(p) does not exist" must not be taken as proof that a volume is gone.
func IsMissing(err error, what ...string) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	if e.Status != http.StatusNotFound && e.Status != http.StatusConflict {
		return false
	}
	for _, w := range what {
		if strings.Contains(e.Msg, w+" does not exist") {
			return true
		}
	}
	return false
}

// IsAlreadyExists reports a 409 "already exists" answer.
func IsAlreadyExists(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusConflict && strings.Contains(e.Msg, "already exists")
}
