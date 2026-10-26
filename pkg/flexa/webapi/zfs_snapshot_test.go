package webapi

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type call struct {
	Path string
	Body map[string]interface{}
}

// fakeProvisiond records calls and answers with replies[path] (default 200 {}).
func fakeProvisiond(t *testing.T, replies map[string]string) (*FEP, func() []call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		json.Unmarshal(raw, &body)
		mu.Lock()
		calls = append(calls, call{r.URL.Path, body})
		mu.Unlock()
		reply, ok := replies[r.URL.Path]
		if !ok {
			reply = `200 {"data":{},"msg":"ok"}`
		}
		code, text, _ := strings.Cut(reply, " ")
		status, _ := strconv.Atoi(code)
		w.WriteHeader(status)
		w.Write([]byte(text))
	}))
	t.Cleanup(srv.Close)
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return &FEP{Ip: host, Port: port}, func() []call {
		mu.Lock()
		defer mu.Unlock()
		return append([]call(nil), calls...)
	}
}

const snapBase = "/csi/snapshot/zfs/pool/p1/volume/pvc-a/snapshot"

func TestZfsSnapshotCalls(t *testing.T) {
	fep, calls := fakeProvisiond(t, map[string]string{
		snapBase + "/create": `200 {"data":{"volName":"pvc-a","snapName":"snapshot-1","createTime":"1700000000","sizeMb":"1024"}}`,
	})
	got, err := fep.ZfsCreateSnapshot("p1", "pvc-a", "snapshot-1")
	if err != nil || got.SnapName != "snapshot-1" || got.CreateTime != "1700000000" || got.SizeMb != "1024" {
		t.Fatalf("create = %+v, %v", got, err)
	}
	if err := fep.ZfsDeleteSnapshot("p1", "pvc-a", "snapshot-1"); err != nil {
		t.Fatal(err)
	}
	err = fep.ZfsCreateVolumeFromSnapshot(2<<30, "pvc-b", "p1", "off", "on", "off", "off", "pvc-a", "snapshot-1")
	if err != nil {
		t.Fatal(err)
	}
	c := calls()
	if c[0].Body["name"] != "snapshot-1" {
		t.Errorf("create body = %v", c[0].Body)
	}
	if c[1].Path != snapBase+"/snapshot-1/delete" || c[1].Body["missingOk"] != true {
		t.Errorf("delete = %+v", c[1])
	}
	src, _ := c[2].Body["source"].(map[string]interface{})
	if c[2].Path != "/csi/zfs/pool/p1/volume/pvc-b/create" || c[2].Body["sizeValue"] != "2048" ||
		src["volume"] != "pvc-a" || src["snap"] != "snapshot-1" {
		t.Errorf("restore = %+v", c[2])
	}
}

func TestDeleteVolumeKeepsShareWhileCsiSnapshotsRemain(t *testing.T) {
	fep, calls := fakeProvisiond(t, map[string]string{
		snapBase + "/list": `200 {"data":{"snapList":["2026-01-01.000000","snapshot-1"],"snapshots":[` +
			`{"snapId":"2026-01-01.000000","snapCreater":"admin"},{"snapId":"snapshot-1","snapCreater":"csi"}]}}`,
	})
	err := fep.ZfsDeleteVolume("pvc-a", "pvc-a", "p1")
	if !IsHasSnapshots(err) || !strings.Contains(err.Error(), "snapshot-1") {
		t.Fatalf("err = %v", err)
	}
	if c := calls(); len(c) != 1 {
		t.Errorf("share or volume was touched: %+v", c)
	}
}

func TestDeleteVolumeGoesOnWithoutCsiSnapshots(t *testing.T) {
	fep, calls := fakeProvisiond(t, map[string]string{
		snapBase + "/list": `409 {"data":null,"msg":"Volume(pvc-a) does not exist"}`,
	})
	if err := fep.ZfsDeleteVolume("pvc-a", "pvc-a", "p1"); err != nil {
		t.Fatal(err)
	}
	if c := calls(); len(c) != 3 || !strings.HasSuffix(c[2].Path, "/volume/pvc-a/delete") {
		t.Errorf("calls = %+v", c)
	}
}

func TestProvisiondHasSnapshotsAnswer(t *testing.T) {
	fep, _ := fakeProvisiond(t, map[string]string{
		"/csi/zfs/pool/p1/volume/pvc-a/delete": `409 {"msg":"Volume(pvc-a) has CSI snapshots: snapshot-1","error":{"code":"HAS_SNAPSHOTS"}}`,
	})
	if err := fep.ZfsDestroyVolume("pvc-a", "p1"); !IsHasSnapshots(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestTooSmallAnswer(t *testing.T) {
	fep, _ := fakeProvisiond(t, map[string]string{
		"/csi/zfs/pool/p1/volume/pvc-b/create": `400 {"msg":"Size 512MB is smaller than snapshot size 1024MB","error":{"code":"TOO_SMALL"}}`,
	})
	err := fep.ZfsCreateVolumeFromSnapshot(512<<20, "pvc-b", "p1", "off", "on", "off", "off", "pvc-a", "s1")
	if !IsTooSmall(err) || IsHasSnapshots(err) {
		t.Fatalf("err = %v", err)
	}
}
