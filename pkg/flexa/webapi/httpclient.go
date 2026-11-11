/*
 * Copyright 2025 Gluesys FlexA Inc.
 */

package webapi

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
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

var (
	clientsMu sync.Mutex
	// tlsClients holds one https client per CA (PEM; "" = system roots), so
	// connections are reused across calls.
	tlsClients     = map[string]*http.Client{}
	requestTimeout = DefaultRequestTimeout
)

// SetRequestTimeout changes the per-call proxy timeout (0 keeps the default).
func SetRequestTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	clientsMu.Lock()
	defer clientsMu.Unlock()
	requestTimeout = d
	httpClient.Timeout = d
	for _, c := range tlsClients {
		c.Timeout = d
	}
}

// client returns the HTTP client for this proxy: plain for http, and for
// https one that verifies the server certificate against fep.CA (or the
// system roots when no CA is set). Verification is never skipped.
func (fep *FEP) client() (*http.Client, error) {
	if fep.Scheme != "https" {
		return httpClient, nil
	}
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := tlsClients[fep.CA]; ok {
		return c, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if fep.CA != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(fep.CA)) {
			return nil, fmt.Errorf("proxy %s:%d: ca holds no PEM certificate (%w)", fep.Ip, fep.Port, ErrNotSent)
		}
		cfg.RootCAs = pool
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = cfg
	c := &http.Client{Timeout: requestTimeout, Transport: tr}
	tlsClients[fep.CA] = c
	return c, nil
}

// send sends req with this proxy's client.
func (fep *FEP) send(req *http.Request) (*http.Response, error) {
	c, err := fep.client()
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// ErrNotSent marks a failure before the request left the driver (bad local
// configuration or arguments): the proxy did nothing.
var ErrNotSent = errors.New("request not sent")

// APIError is a non-2xx proxy response.
type APIError struct {
	Status int
	Msg    string
	Path   string
	Code   string // provisiond error.code, when given
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
		Msg   string `json:"msg"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	msg, code := "", ""
	if json.Unmarshal(body, &e) == nil {
		msg, code = e.Msg, e.Error.Code
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 512 {
			msg = msg[:512]
		}
	}
	return &APIError{Status: status, Msg: msg, Path: path, Code: code}
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

// IsRejected reports that the proxy refused the request itself (400): it did not
// act on it, and the same request will be refused again. Timeouts, dropped
// connections, 409 and 5xx are not rejections: the call may have taken effect,
// or may succeed when sent again.
func IsRejected(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusBadRequest
}

// shareConflicts are the 409 answers to an NFS share create that conflict
// with settings already on the storage: provisiond checks them before it acts,
// so the share was not made, and the same request gets the same answer.
var shareConflicts = []string{
	"already exists with different settings",    // provisiond: share, other path or NFS options
	"already exists with a different network",   // provisiond: secure zone name, other network
	"already exists with different NFS options", // legacy proxy: share, other NFS options
	"uses different NFS options",                // provisiond: pool root export, other options
	"has NFS disabled or no zone",               // provisiond: pool root export without this zone
	"is not the CSI root export",                // provisiond: pool root share name taken
	"has OpenStack volumes",                     // provisiond: pool root export on a shared pool
}

// IsShareConflict reports a share create refused for a conflict with existing
// settings (see shareConflicts). Retrying cannot make that share.
func IsShareConflict(err error) bool {
	var e *APIError
	if !errors.As(err, &e) || e.Status != http.StatusConflict {
		return false
	}
	for _, m := range shareConflicts {
		if strings.Contains(e.Msg, m) {
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
