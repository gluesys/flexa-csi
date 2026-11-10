package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/gluesys/flexa-csi/pkg/flexa/common"
	"github.com/gluesys/flexa-csi/pkg/flexa/service"
	"github.com/gluesys/flexa-csi/pkg/utils"
)

// fakeProxy answers like the legacy REST proxy: a missing pool, volume or
// share is 409 with "does not exist" in msg, volume info carries no share path
// and a second share create is 409 "already exists". provisiond=true switches to the
// provisiond shape (share path in volume info, idempotent share create).
type fakeProxy struct {
	mu    sync.Mutex
	pools map[string]map[string]*fakeVol
	// failShare: 0 creates the share; an HTTP status answers it without
	// creating; shareLost creates it and then drops the connection.
	failShare  int
	provisiond bool
	// poolGone makes every pool answer "does not exist", as the legacy proxy
	// does while its monitor snapshot misses the pool.
	poolGone bool
	calls    []string
	// lustre answers like provisiond for cluster "fs1".
	lustre map[string]*fakeVol
	// failCreate answers a ZFS dataset or Lustre volume create with this HTTP
	// status without creating it (shareLost: creates it, then drops the
	// connection). failVip answers the server address lookup with it.
	failCreate int
	failVip    int
	// failDelete answers a ZFS dataset delete with this HTTP status.
	failDelete int
}

type fakeVol struct {
	sizeMb int64
	usedMb int64
	snaps  []string
	// source: provisiond's recorded content source; nil = not reported.
	source *string
	share  string
	// creating: provisiond has records but the create has not finished.
	creating bool
}

func newFakeProxy(t *testing.T, pools ...string) (*fakeProxy, *common.ProxyInfo) {
	p := &fakeProxy{pools: map[string]map[string]*fakeVol{}, lustre: map[string]*fakeVol{}}
	for _, name := range pools {
		p.pools[name] = map[string]*fakeVol{}
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	n, _ := strconv.Atoi(port)
	return p, &common.ProxyInfo{Host: host, Port: n}
}

// shareLost makes the fake create the share and then drop the connection, as
// when the answer is lost after the backend acted.
const shareLost = -1

func dropConnection(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func reply(w http.ResponseWriter, code int, data interface{}, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data, "msg": msg})
}

func (p *fakeProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	path, ok := strings.CutPrefix(r.URL.Path, "/csi/")
	if !ok {
		reply(w, 404, nil, "not under /csi")
		return
	}
	p.calls = append(p.calls, r.Method+" /"+path)
	seg := strings.Split(strings.Trim(path, "/"), "/")
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)

	switch {
	case seg[0] == "lustre" && len(seg) == 3 && seg[1] == "vip":
		if p.failVip != 0 {
			reply(w, p.failVip, nil, "vip lookup failed")
			return
		}
		reply(w, 200, map[string]string{"vip": "10.0.0.8"}, "")
	case seg[0] == "lustre" && len(seg) >= 5 && seg[1] == "cluster" && seg[3] == "volume":
		name := seg[4]
		v, exists := p.lustre[name]
		data := func(v *fakeVol) map[string]interface{} {
			st := "normal"
			if v.creating {
				st = "creating"
			}
			return map[string]interface{}{"clusterName": seg[2], "volName": name, "volumeId": name,
				"path": "/lustre/agent/" + seg[2] + "/" + name, "quotaMb": v.sizeMb,
				"quota": map[string]int64{"limitMb": v.sizeMb, "usedMb": v.usedMb, "freeMb": v.sizeMb - v.usedMb}, "status": st}
		}
		switch {
		case len(seg) == 5 && !exists:
			reply(w, 404, nil, fmt.Sprintf("Share(%s) does not exist", name))
		case len(seg) == 5:
			reply(w, 200, data(v), "")
		case seg[5] == "create":
			if p.failCreate > 0 {
				reply(w, p.failCreate, nil, "volume create failed")
				return
			}
			// provisiond: a create on a volume whose create is unfinished joins or
			// settles it and answers when the volume is complete.
			if !exists {
				mb, _ := strconv.ParseInt(body["sizeValue"], 10, 64)
				v = &fakeVol{sizeMb: mb}
				p.lustre[name] = v
			}
			v.creating = false
			if p.failCreate == shareLost {
				dropConnection(w)
				return
			}
			reply(w, 200, data(v), "")
		default:
			reply(w, 404, nil, "no route")
		}
	case len(seg) == 5 && seg[0] == "zfs" && seg[3] == "vip":
		if p.failVip != 0 {
			reply(w, p.failVip, nil, "vip lookup failed")
			return
		}
		reply(w, 200, map[string]string{"vip": "10.0.0.9"}, "")
	case len(seg) == 8 && seg[0] == "snapshot" && seg[7] == "create":
		// provisiond: a CSI snapshot name is one snapshot; the same name on
		// another volume is refused.
		for vn, v := range p.pools[seg[3]] {
			if vn != seg[5] && slices.Contains(v.snaps, body["name"]) {
				reply(w, 409, nil, fmt.Sprintf("Snapshot(%s) already exists on Volume(%s)", body["name"], vn))
				return
			}
		}
		v := p.pools[seg[3]][seg[5]]
		if !slices.Contains(v.snaps, body["name"]) {
			v.snaps = append(v.snaps, body["name"])
		}
		reply(w, 200, map[string]string{"volName": seg[5], "snapName": body["name"], "sizeMb": strconv.FormatInt(v.sizeMb, 10)}, "")
	case len(seg) == 3 && seg[0] == "zfs" && seg[1] == "pool" && seg[2] == "list":
		if p.poolGone {
			reply(w, 500, nil, "pool list failed")
			return
		}
		names := []string{}
		for n := range p.pools {
			names = append(names, n)
		}
		sort.Strings(names)
		reply(w, 200, map[string]interface{}{"poolList": names}, "")
	case seg[0] == "zfs" && len(seg) >= 4:
		pool, ok := p.pools[seg[2]]
		if !ok || p.poolGone {
			reply(w, 409, nil, fmt.Sprintf("Pool(%s) does not exist", seg[2]))
			return
		}
		if seg[3] == "volume" && len(seg) == 5 && seg[4] == "list" {
			names := []string{}
			for n := range pool {
				names = append(names, n)
			}
			reply(w, 200, map[string]interface{}{"poolName": seg[2], "volumeList": names}, "")
			return
		}
		name := seg[4]
		v, exists := pool[name]
		action := ""
		if len(seg) == 6 {
			action = seg[5]
		}
		switch action {
		case "":
			if !exists {
				reply(w, 409, nil, fmt.Sprintf("Volume(%s) does not exist", name))
				return
			}
			data := map[string]interface{}{"poolName": seg[2], "volName": name, "total": v.sizeMb}
			if v.creating {
				data["status"] = "creating"
			}
			if v.source != nil {
				data["source"] = *v.source
			}
			if p.provisiond && v.share != "" {
				data["sharePath"] = fmt.Sprintf("/%s/%s/%s", seg[2], name, v.share)
				data["shareName"] = v.share
			}
			reply(w, 200, data, "")
		case "create":
			mb, _ := strconv.ParseInt(body["sizeValue"], 10, 64)
			if p.failCreate > 0 {
				reply(w, p.failCreate, nil, "volume create failed")
				return
			}
			if p.failCreate == shareLost {
				pool[name] = &fakeVol{sizeMb: mb}
				dropConnection(w)
				return
			}
			if exists && p.provisiond && v.sizeMb >= mb {
				// provisiond: the create is idempotent and finishes (joins or
				// settles) an unfinished one on the same volume.
				v.creating = false
				reply(w, 200, map[string]string{"name": name}, "")
				return
			}
			if exists {
				reply(w, 409, nil, fmt.Sprintf("Volume(%s) already exists", name))
				return
			}
			pool[name] = &fakeVol{sizeMb: mb}
			reply(w, 200, map[string]string{"name": name}, "")
		case "delete":
			if p.failDelete != 0 {
				reply(w, p.failDelete, nil, "delete failed")
				return
			}
			if !exists {
				reply(w, 409, nil, fmt.Sprintf("Volume(%s) does not exist", name))
				return
			}
			delete(pool, name)
			reply(w, 200, map[string]string{}, "")
		}
	case seg[0] == "share" && (len(seg) == 6 || len(seg) == 7):
		if p.poolGone {
			reply(w, 409, nil, fmt.Sprintf("Pool(%s) does not exist", seg[2]))
			return
		}
		v, ok := p.pools[seg[2]][seg[4]]
		if !ok {
			reply(w, 409, nil, fmt.Sprintf("Volume(%s) does not exist", seg[4]))
			return
		}
		if len(seg) == 6 {
			if v.share != seg[5] {
				reply(w, 409, nil, fmt.Sprintf("%s does not exist", seg[5]))
				return
			}
			reply(w, 200, map[string]string{"shareName": seg[5], "path": fmt.Sprintf("/%s/%s/%s", seg[2], seg[4], seg[5])}, "")
			return
		}
		switch seg[6] {
		case "create":
			if p.failShare == shareLost {
				v.share = seg[5]
				dropConnection(w)
				return
			}
			if p.failShare != 0 {
				reply(w, p.failShare, nil, "share create failed")
				return
			}
			if v.share == seg[5] && !p.provisiond {
				reply(w, 409, nil, fmt.Sprintf("%s already exists", seg[5]))
				return
			}
			v.share = seg[5]
			reply(w, 200, map[string]string{"shareName": seg[5]}, "")
		case "delete":
			if v.share != seg[5] {
				reply(w, 409, nil, fmt.Sprintf("%s does not exist", seg[5]))
				return
			}
			v.share = ""
			reply(w, 200, map[string]string{}, "")
		}
	default:
		reply(w, 404, nil, "no route")
	}
}

func newTestController(t *testing.T, proxy *common.ProxyInfo, objs ...runtime.Object) (*ControllerServer, *fake.Clientset) {
	t.Helper()
	client := fake.NewSimpleClientset(objs...)
	d := &Driver{K8sClient: client}
	d.vCap = []*csi.VolumeCapability_AccessMode{
		{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
	}
	d.SetClientInfoConfig(&common.ClientInfoConfig{Profiles: map[string]common.ProxyInfo{"zfs": *proxy}})
	return &ControllerServer{Driver: d, locks: newVolumeLocks()}, client
}

func mountCaps() []*csi.VolumeCapability {
	return []*csi.VolumeCapability{{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
	}}
}

func zfsPV(name string, proxy *common.ProxyInfo) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{
				Driver:       DriverName,
				VolumeHandle: name,
				VolumeAttributes: map[string]string{
					"fs": "zfs", "poolName": "pool1", "shareName": name,
					volAttrProxyIP: proxy.Host, volAttrProxyPort: strconv.Itoa(proxy.Port),
				},
			},
		}},
	}
}

func createReq(name string, required int64) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:               name,
		CapacityRange:      &csi.CapacityRange{RequiredBytes: required},
		VolumeCapabilities: mountCaps(),
		Parameters: map[string]string{
			"fs": "zfs", "poolName": "pool1", "proxyProfile": "zfs", "protocol": "nfs",
			"flexa.io/secureAddress": "10.0.0.0", "flexa.io/secureSubnet": "255.0.0.0",
		},
	}
}

func TestGetSizeByCapacityRange(t *testing.T) {
	mib, gib := int64(utils.UNIT_MB), int64(utils.UNIT_GB)
	cases := []struct {
		name            string
		required, limit int64
		want            int64
		code            codes.Code
	}{
		{"unspecified uses default", 0, 0, gib, codes.OK},
		{"1G decimal is raised to 1GiB", 1_000_000_000, 0, gib, codes.OK},
		{"500Mi is raised to 1GiB", 500 * mib, 0, gib, codes.OK},
		{"1500M rounds up to whole MiB", 1_500_000_000, 0, 1431 * mib, codes.OK},
		{"exact GiB stays", 2 * gib, 0, 2 * gib, codes.OK},
		{"limit below floor keeps the request", 500 * mib, 600 * mib, 600 * mib, codes.OK},
		{"limit below required", 2 * gib, gib, 0, codes.InvalidArgument},
		{"no whole MiB fits", mib + 1, mib + 2, 0, codes.OutOfRange},
	}
	for _, c := range cases {
		got, err := getSizeByCapacityRange(&csi.CapacityRange{RequiredBytes: c.required, LimitBytes: c.limit})
		if status.Code(err) != c.code || got != c.want {
			t.Errorf("%s: got %d, %v; want %d, %v", c.name, got, err, c.want, c.code)
		}
		if err == nil && (got < c.required || (c.limit > 0 && got > c.limit)) {
			t.Errorf("%s: %d is outside [%d, %d]", c.name, got, c.required, c.limit)
		}
	}
}

func TestCreateVolumeIsIdempotentAndNeverSmaller(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	cs, _ := newTestController(t, info)
	ctx := context.Background()

	resp, err := cs.CreateVolume(ctx, createReq("pvc-1", 1_500_000_000))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := resp.GetVolume().GetCapacityBytes(); got < 1_500_000_000 {
		t.Fatalf("capacity %d is below the request", got)
	}
	if proxy.pools["pool1"]["pvc-1"].sizeMb != 1431 {
		t.Fatalf("backend size = %d MiB, want 1431", proxy.pools["pool1"]["pvc-1"].sizeMb)
	}
	if _, err := cs.CreateVolume(ctx, createReq("pvc-1", 1_500_000_000)); err != nil {
		t.Fatalf("repeat create: %v", err)
	}
	if _, err := cs.CreateVolume(ctx, createReq("pvc-1", 3*utils.UNIT_GB)); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("larger repeat = %v, want AlreadyExists", err)
	}

	// One byte more than an existing 1GiB volume is still too small.
	for _, provisiond := range []bool{false, true} {
		proxy.provisiond = provisiond
		proxy.pools["pool1"]["pvc-g"] = &fakeVol{sizeMb: 1024, share: "pvc-g"}
		if _, err := cs.CreateVolume(ctx, createReq("pvc-g", utils.UNIT_GB+1)); status.Code(err) != codes.AlreadyExists {
			t.Fatalf("provisiond=%t: 1GiB+1 over a 1GiB volume = %v, want AlreadyExists", provisiond, err)
		}
	}
}

func TestShareOnlyRetryReportsBackendSize(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	cs, _ := newTestController(t, info)
	// Legacy proxy: the dataset exists at 2GiB, the retry asks for 1GiB.
	proxy.pools["pool1"]["pvc-1"] = &fakeVol{sizeMb: 2048}
	resp, err := cs.CreateVolume(context.Background(), createReq("pvc-1", utils.UNIT_GB))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := resp.GetVolume().GetCapacityBytes(); got != 2*utils.UNIT_GB {
		t.Fatalf("capacity = %d, want the dataset's 2GiB", got)
	}
}

func TestCreateVolumeRetryAfterSlowCreate(t *testing.T) {
	for _, provisiond := range []bool{false, true} {
		proxy, info := newFakeProxy(t, "pool1")
		proxy.provisiond = provisiond
		cs, _ := newTestController(t, info)
		// The first attempt finished on the backend after the sidecar gave up:
		// dataset and share both exist.
		proxy.pools["pool1"]["pvc-1"] = &fakeVol{sizeMb: 1024, share: "pvc-1"}
		resp, err := cs.CreateVolume(context.Background(), createReq("pvc-1", utils.UNIT_GB))
		if err != nil {
			t.Fatalf("provisiond=%t: retry after a finished create: %v", provisiond, err)
		}
		if got := resp.GetVolume().GetVolumeContext()["baseDir"]; got != "/pool1/pvc-1/pvc-1" {
			t.Fatalf("provisiond=%t: baseDir = %q", provisiond, got)
		}
	}
}

func TestCreateVolumeRollbackAndShareOnlyRetry(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	cs, _ := newTestController(t, info)
	ctx := context.Background()

	// A share request the proxy refused (400) can never succeed: the dataset
	// goes away.
	proxy.failShare = http.StatusBadRequest
	if _, err := cs.CreateVolume(ctx, createReq("pvc-1", utils.UNIT_GB)); status.Code(err) != codes.Internal {
		t.Fatalf("create with refused share = %v, want Internal", err)
	}
	if _, ok := proxy.pools["pool1"]["pvc-1"]; ok {
		t.Fatal("rollback left the dataset behind")
	}

	// A dataset left without its share (e.g. the driver died in between) gets
	// only the share on the next attempt.
	proxy.failShare = 0
	proxy.pools["pool1"]["pvc-2"] = &fakeVol{sizeMb: 1024}
	resp, err := cs.CreateVolume(ctx, createReq("pvc-2", utils.UNIT_GB))
	if err != nil {
		t.Fatalf("create over a share-less dataset: %v", err)
	}
	if resp.GetVolume().GetVolumeContext()["baseDir"] == "" || proxy.pools["pool1"]["pvc-2"].share != "pvc-2" {
		t.Fatal("share was not created")
	}
}

// When the share result is unknown the dataset may already be in use (another
// controller can have returned it), so it must never be destroyed.
func TestCreateVolumeKeepsDatasetWhenShareResultUnknown(t *testing.T) {
	for _, provisiond := range []bool{false, true} {
		for _, fail := range []int{shareLost, http.StatusInternalServerError, http.StatusConflict, http.StatusServiceUnavailable} {
			proxy, info := newFakeProxy(t, "pool1")
			proxy.provisiond = provisiond
			cs, _ := newTestController(t, info)
			ctx := context.Background()

			proxy.failShare = fail
			// Not final: the provisioner must keep the claim so a PVC deleted now
			// still gets the kept dataset deleted.
			if _, err := cs.CreateVolume(ctx, createReq("pvc-1", utils.UNIT_GB)); status.Code(err) != codes.Unavailable {
				t.Fatalf("provisiond=%t fail=%d: create = %v, want Unavailable", provisiond, fail, err)
			}
			if _, ok := proxy.pools["pool1"]["pvc-1"]; !ok {
				t.Fatalf("provisiond=%t fail=%d: dataset destroyed on an unknown share result", provisiond, fail)
			}
			for _, c := range proxy.calls {
				if strings.Contains(c, "/destroy") || strings.Contains(c, "/delete") {
					t.Fatalf("provisiond=%t fail=%d: destructive call %s", provisiond, fail, c)
				}
			}

			// The retry converges on the same volume.
			proxy.failShare = 0
			resp, err := cs.CreateVolume(ctx, createReq("pvc-1", utils.UNIT_GB))
			if err != nil {
				t.Fatalf("provisiond=%t fail=%d: retry: %v", provisiond, fail, err)
			}
			if resp.GetVolume().GetVolumeContext()["baseDir"] != "/pool1/pvc-1/pvc-1" || proxy.pools["pool1"]["pvc-1"].share != "pvc-1" {
				t.Fatalf("provisiond=%t fail=%d: retry did not finish the share: %v", provisiond, fail, resp.GetVolume().GetVolumeContext())
			}
		}
	}
}

func TestCreateVolumeValidatesBeforeTouchingAnything(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	cs, _ := newTestController(t, info)
	ctx := context.Background()

	block := createReq("pvc-1", utils.UNIT_GB)
	block.VolumeCapabilities[0].AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
	badFs := createReq("pvc-1", utils.UNIT_GB)
	badFs.Parameters["fs"] = "xfs"
	noName := createReq("", utils.UNIT_GB)
	noCaps := createReq("pvc-1", utils.UNIT_GB)
	noCaps.VolumeCapabilities = nil

	for name, r := range map[string]*csi.CreateVolumeRequest{"block": block, "fs": badFs, "name": noName, "caps": noCaps} {
		if _, err := cs.CreateVolume(ctx, r); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
	if len(proxy.calls) != 0 {
		t.Fatalf("invalid requests reached the proxy: %v", proxy.calls)
	}

	upper := createReq("pvc-1", utils.UNIT_GB)
	upper.Parameters["fs"] = "ZFS"
	if _, err := cs.CreateVolume(ctx, upper); err != nil {
		t.Fatalf("fs=ZFS: %v", err)
	}
}

func TestDeleteVolumeFinishesAfterPartialFailure(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	cs, _ := newTestController(t, info, zfsPV("pvc-1", info), zfsPV("pvc-2", info))
	ctx := context.Background()

	// pvc-1: the share is already gone, the dataset is still there.
	proxy.pools["pool1"]["pvc-1"] = &fakeVol{sizeMb: 1024}
	if _, err := cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: "pvc-1"}); err != nil {
		t.Fatalf("delete with missing share: %v", err)
	}
	if _, ok := proxy.pools["pool1"]["pvc-1"]; ok {
		t.Fatal("dataset left behind")
	}
	// pvc-2: nothing left on the backend at all.
	if _, err := cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: "pvc-2"}); err != nil {
		t.Fatalf("delete of a volume that is already gone: %v", err)
	}
}

func TestDeleteVolumeKeepsRetryingWhilePoolIsMissing(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	cs, _ := newTestController(t, info, zfsPV("pvc-1", info))
	proxy.pools["pool1"]["pvc-1"] = &fakeVol{sizeMb: 1024, share: "pvc-1"}
	proxy.poolGone = true

	if _, err := cs.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "pvc-1"}); err == nil {
		t.Fatal("delete reported success while the proxy only said the pool is missing")
	}
	proxy.poolGone = false
	if _, err := cs.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "pvc-1"}); err != nil {
		t.Fatalf("delete once the pool is back: %v", err)
	}
	if _, ok := proxy.pools["pool1"]["pvc-1"]; ok {
		t.Fatal("volume left behind")
	}
}

// The provisioner calls DeleteVolume to clean up a created volume whose PV it
// could not save: with no PV the volume is looked up on the backends.
func TestDeleteVolumeWithoutPVFindsTheBackendVolume(t *testing.T) {
	for _, provisiond := range []bool{false, true} {
		proxy, info := newFakeProxy(t, "pool1", "pool2")
		proxy.provisiond = provisiond
		cs, _ := newTestController(t, info)
		ctx := context.Background()

		proxy.pools["pool2"]["pvc-1"] = &fakeVol{sizeMb: 1024, share: "pvc-1"}
		if _, err := cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: "pvc-1"}); err != nil {
			t.Fatalf("provisiond=%t: delete without PV: %v", provisiond, err)
		}
		if _, ok := proxy.pools["pool2"]["pvc-1"]; ok {
			t.Fatalf("provisiond=%t: volume without PV left on the backend", provisiond)
		}
		// Gone everywhere: success.
		if _, err := cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: "pvc-1"}); err != nil {
			t.Fatalf("provisiond=%t: delete of a volume gone everywhere: %v", provisiond, err)
		}

		// A backend that cannot be listed may hold it: retry, never success.
		proxy.pools["pool1"]["pvc-2"] = &fakeVol{sizeMb: 1024, share: "pvc-2"}
		proxy.poolGone = true
		if _, err := cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: "pvc-2"}); status.Code(err) != codes.Unavailable {
			t.Fatalf("provisiond=%t: delete without PV while pools cannot be listed = %v, want Unavailable", provisiond, err)
		}
		proxy.poolGone = false

		// The same name in two pools: delete neither.
		proxy.pools["pool2"]["pvc-2"] = &fakeVol{sizeMb: 1024, share: "pvc-2"}
		if _, err := cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: "pvc-2"}); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("provisiond=%t: ambiguous delete = %v, want FailedPrecondition", provisiond, err)
		}
		if proxy.pools["pool1"]["pvc-2"] == nil || proxy.pools["pool2"]["pvc-2"] == nil {
			t.Fatalf("provisiond=%t: ambiguous delete removed a volume", provisiond)
		}
	}
}

func TestDeleteVolumeRetriesWhenPVLookupFails(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	cs, client := newTestController(t, info, zfsPV("pvc-1", info))
	proxy.pools["pool1"]["pvc-1"] = &fakeVol{sizeMb: 1024, share: "pvc-1"}
	client.PrependReactor("get", "persistentvolumes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})

	_, err := cs.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "pvc-1"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("delete during an API outage = %v, want Unavailable", err)
	}
	if _, ok := proxy.pools["pool1"]["pvc-1"]; !ok {
		t.Fatal("volume vanished although nothing was deleted")
	}
}

func TestValidateVolumeCapabilitiesConfirms(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	cs, _ := newTestController(t, info, zfsPV("pvc-1", info))
	proxy.pools["pool1"]["pvc-1"] = &fakeVol{sizeMb: 1024, share: "pvc-1"}
	ctx := context.Background()

	resp, err := cs.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: "pvc-1", VolumeCapabilities: mountCaps()})
	if err != nil || resp.GetConfirmed() == nil {
		t.Fatalf("supported caps: resp=%v err=%v, want Confirmed", resp, err)
	}

	block := mountCaps()
	block[0].AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
	resp, err = cs.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: "pvc-1", VolumeCapabilities: block})
	if err != nil || resp.GetConfirmed() != nil || resp.GetMessage() == "" {
		t.Fatalf("block caps: resp=%v err=%v, want unconfirmed with a message", resp, err)
	}

	_, err = cs.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: "pvc-x", VolumeCapabilities: mountCaps()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("unknown volume = %v, want NotFound", err)
	}
}

func TestExpandVolumeChecks(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	cs, _ := newTestController(t, info, zfsPV("pvc-1", info), zfsPV("pvc-gone", info))
	proxy.pools["pool1"]["pvc-1"] = &fakeVol{sizeMb: 2048, share: "pvc-1"}
	ctx := context.Background()

	if _, err := cs.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{VolumeId: "pvc-1"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil capacity range = %v, want InvalidArgument", err)
	}
	resp, err := cs.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{
		VolumeId: "pvc-1", CapacityRange: &csi.CapacityRange{RequiredBytes: utils.UNIT_GB}})
	if err != nil || resp.GetCapacityBytes() != 2*utils.UNIT_GB {
		t.Fatalf("expand below current size: resp=%v err=%v, want current 2GiB kept", resp, err)
	}
	_, err = cs.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{
		VolumeId: "pvc-gone", CapacityRange: &csi.CapacityRange{RequiredBytes: utils.UNIT_GB}})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expand of a missing volume = %v, want NotFound", err)
	}
}

func TestProxyForVolumeTakesTokenFromClientInfo(t *testing.T) {
	d := &Driver{}
	d.SetClientInfoConfig(&common.ClientInfoConfig{
		Default:  &common.ProxyInfo{Host: "10.0.0.1", Port: 9001, Token: "top"},
		Profiles: map[string]common.ProxyInfo{"b": {Host: "10.0.0.2", Port: 9001, Token: "b-tok"}},
	})
	cases := []struct {
		attrs     map[string]string
		host, tok string
	}{
		{map[string]string{volAttrProxyIP: "10.0.0.2", volAttrProxyPort: "9001", volAttrProxyProfile: "b"}, "10.0.0.2", "b-tok"},
		{map[string]string{volAttrProxyIP: "10.0.0.1", volAttrProxyPort: "9001"}, "10.0.0.1", "top"},
		{map[string]string{volAttrProxyProfile: "b"}, "10.0.0.2", "b-tok"},
		{map[string]string{}, "10.0.0.1", "top"},
		// An address that matches no client-info entry never gets a token.
		{map[string]string{volAttrProxyIP: "10.9.9.9", volAttrProxyPort: "9001", volAttrProxyProfile: "b"}, "10.9.9.9", ""},
	}
	for i, c := range cases {
		p, err := d.ProxyForVolume(c.attrs)
		if err != nil || p.Host != c.host || p.Token != c.tok {
			t.Errorf("case %d: proxy=%+v err=%v, want %s/%s", i, p, err, c.host, c.tok)
		}
	}
}

func lustreCreateReq(name string, required int64) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name:               name,
		CapacityRange:      &csi.CapacityRange{RequiredBytes: required},
		VolumeCapabilities: mountCaps(),
		Parameters: map[string]string{
			"fs": "lustre", "clusterName": "fs1", "proxyProfile": "zfs", "protocol": "nfs",
			"flexa.io/secureAddress": "10.0.0.0", "flexa.io/secureSubnet": "255.0.0.0",
		},
	}
}

func countCalls(p *fakeProxy, call string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if c == call {
			n++
		}
	}
	return n
}

// A volume whose backend create has not finished (provisiond status
// "creating": records exist before the directory and quota) is never reported
// as created from the lookup alone.
func TestCreateVolumeDoesNotTrustAnUnfinishedCreate(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	cs, _ := newTestController(t, info)
	ctx := context.Background()

	// Lustre: the create is sent again, so provisiond joins or settles it.
	proxy.lustre["pvc-l"] = &fakeVol{sizeMb: 2048, creating: true}
	resp, err := cs.CreateVolume(ctx, lustreCreateReq("pvc-l", 2*utils.UNIT_GB))
	if err != nil {
		t.Fatalf("lustre create over an unfinished one: %v", err)
	}
	if got := countCalls(proxy, "POST /lustre/cluster/fs1/volume/pvc-l/create"); got != 1 {
		t.Fatalf("lustre create POSTs = %d, want 1", got)
	}
	if resp.GetVolume().GetCapacityBytes() != 2*utils.UNIT_GB || proxy.lustre["pvc-l"].creating {
		t.Fatalf("lustre volume not completed: %+v", resp.GetVolume())
	}
	// A finished volume is answered from the lookup without another create.
	if _, err := cs.CreateVolume(ctx, lustreCreateReq("pvc-l", 2*utils.UNIT_GB)); err != nil {
		t.Fatalf("repeat lustre create: %v", err)
	}
	if got := countCalls(proxy, "POST /lustre/cluster/fs1/volume/pvc-l/create"); got != 1 {
		t.Fatalf("lustre create POSTs after a finished create = %d, want 1", got)
	}

	// ZFS: the same — dataset create and share create are sent again, and
	// provisiond finishes the unfinished create before answering.
	proxy.provisiond = true
	proxy.pools["pool1"]["pvc-z"] = &fakeVol{sizeMb: 1024, share: "pvc-z", creating: true}
	resp, err = cs.CreateVolume(ctx, createReq("pvc-z", utils.UNIT_GB))
	if err != nil {
		t.Fatalf("zfs create over an unfinished one: %v", err)
	}
	if got := countCalls(proxy, "POST /zfs/pool/pool1/volume/pvc-z/create"); got != 1 {
		t.Fatalf("zfs create POSTs = %d, want 1", got)
	}
	if got := countCalls(proxy, "POST /share/pool/pool1/volume/pvc-z/pvc-z/create"); got != 1 {
		t.Fatalf("zfs share create POSTs = %d, want 1", got)
	}
	if proxy.pools["pool1"]["pvc-z"].creating || resp.GetVolume().GetVolumeContext()["baseDir"] != "/pool1/pvc-z/pvc-z" {
		t.Fatalf("zfs volume not completed: %+v", resp.GetVolume())
	}
	if _, err := cs.CreateVolume(ctx, createReq("pvc-z", utils.UNIT_GB)); err != nil {
		t.Fatalf("repeat zfs create: %v", err)
	}
	if got := countCalls(proxy, "POST /zfs/pool/pool1/volume/pvc-z/create"); got != 1 {
		t.Fatalf("zfs create POSTs after a finished create = %d, want 1", got)
	}
}

// mountNetwork: the server address must be in that network, checked before the
// backend creates anything; the volume context carries the network as mountIP.
func TestCreateVolumeMountNetwork(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1") // the fake resolves Lustre VIPs to 10.0.0.8
	cs, _ := newTestController(t, info)
	ctx := context.Background()

	req := lustreCreateReq("pvc-in", 2*utils.UNIT_GB)
	req.Parameters[paramMountNetwork] = "10.0.0.0/24"
	resp, err := cs.CreateVolume(ctx, req)
	if err != nil {
		t.Fatalf("create in mountNetwork: %v", err)
	}
	if got := resp.GetVolume().GetVolumeContext(); got["vip"] != "10.0.0.8" || got[volAttrMountIP] != "10.0.0.0/24" {
		t.Fatalf("volume context = %v", got)
	}

	req = lustreCreateReq("pvc-out", 2*utils.UNIT_GB)
	req.Parameters[paramMountNetwork] = "198.51.100.0/24"
	if _, err = cs.CreateVolume(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("create outside mountNetwork: err = %v, want FailedPrecondition", err)
	}
	if got := countCalls(proxy, "POST /lustre/cluster/fs1/volume/pvc-out/create"); got != 0 {
		t.Fatalf("create POSTs outside mountNetwork = %d, want 0", got)
	}
}

// Lustre volume stats carry the project quota usage, not zero.
func TestLustreGetVolumeReportsUsage(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	proxy.lustre["pvc-l"] = &fakeVol{sizeMb: 2048, usedMb: 512}
	svc := service.NewFlexAService()
	svc.SetFep(info)
	v, err := svc.GetVolume("lustre", "fs1", "pvc-l")
	if err != nil || v == nil {
		t.Fatalf("GetVolume = %v, %v", v, err)
	}
	if v.Size != 2048*utils.UNIT_MB || v.Used != 512*utils.UNIT_MB || v.Free != 1536*utils.UNIT_MB {
		t.Fatalf("size/used/free = %d/%d/%d", v.Size, v.Used, v.Free)
	}
}

// OpenStack volumes on the same pool (cdr- Cinder, mnl- Manila) are not CSI
// volumes: ListVolumes leaves them out and DeleteVolume never looks for them.
func TestListVolumesLeavesOutOpenStackVolumes(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	proxy.provisiond = true
	cs, _ := newTestController(t, info)
	for _, n := range []string{"pvc-1", "cdr-1", "mnl-1"} {
		proxy.pools["pool1"][n] = &fakeVol{sizeMb: 1024, share: n}
	}
	resp, err := cs.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	var got []string
	for _, e := range resp.GetEntries() {
		got = append(got, e.GetVolume().GetVolumeId())
	}
	if len(got) != 1 || got[0] != "pvc-1" {
		t.Fatalf("ListVolumes = %v, want [pvc-1]", got)
	}
	if _, err := cs.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "cdr-1"}); err != nil {
		t.Fatalf("DeleteVolume cdr-1: %v", err)
	}
	if proxy.pools["pool1"]["cdr-1"] == nil {
		t.Fatal("DeleteVolume without PV removed an OpenStack volume")
	}
}

// The same snapshot name on another source is a different snapshot:
// AlreadyExists, while a repeat on the same source returns the first one.
func TestCreateSnapshotNameTakenByAnotherSource(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	proxy.provisiond = true
	cs, _ := newTestController(t, info, zfsPV("pvc-1", info), zfsPV("pvc-2", info))
	ctx := context.Background()
	for _, n := range []string{"pvc-1", "pvc-2"} {
		proxy.pools["pool1"][n] = &fakeVol{sizeMb: 1024, share: n}
	}
	req := &csi.CreateSnapshotRequest{Name: "snapshot-a", SourceVolumeId: "pvc-1"}
	first, err := cs.CreateSnapshot(ctx, req)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	again, err := cs.CreateSnapshot(ctx, req)
	if err != nil || again.GetSnapshot().GetSnapshotId() != first.GetSnapshot().GetSnapshotId() {
		t.Fatalf("repeat CreateSnapshot = %v, %v", again, err)
	}
	_, err = cs.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "snapshot-a", SourceVolumeId: "pvc-2"})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("same name on another source = %v, want AlreadyExists", err)
	}
}

// An existing volume answers a create only for the same content source; a
// backend that does not report the source is not checked.
func TestCreateVolumeComparesContentSource(t *testing.T) {
	proxy, info := newFakeProxy(t, "pool1")
	proxy.provisiond = true
	cs, _ := newTestController(t, info, zfsPV("pvc-src", info))
	ctx := context.Background()
	proxy.pools["pool1"]["pvc-src"] = &fakeVol{sizeMb: 1024, share: "pvc-src"}
	empty, fromS1 := "", "pvc-src@s1"
	restore := func(name, snap string) *csi.CreateVolumeRequest {
		r := createReq(name, utils.UNIT_GB)
		r.VolumeContentSource = &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{
			Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: snapshotID("zfs", "pool1", "pvc-src", snap)}}}
		return r
	}

	proxy.pools["pool1"]["pvc-a"] = &fakeVol{sizeMb: 1024, share: "pvc-a", source: &fromS1}
	if _, err := cs.CreateVolume(ctx, restore("pvc-a", "s1")); err != nil {
		t.Fatalf("same source: %v", err)
	}
	if _, err := cs.CreateVolume(ctx, restore("pvc-a", "s2")); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("other snapshot = %v, want AlreadyExists", err)
	}
	if _, err := cs.CreateVolume(ctx, createReq("pvc-a", utils.UNIT_GB)); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("empty over restored = %v, want AlreadyExists", err)
	}
	proxy.pools["pool1"]["pvc-b"] = &fakeVol{sizeMb: 1024, share: "pvc-b", source: &empty}
	if _, err := cs.CreateVolume(ctx, restore("pvc-b", "s1")); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("restore over empty = %v, want AlreadyExists", err)
	}
	// Not reported: not checked.
	proxy.pools["pool1"]["pvc-c"] = &fakeVol{sizeMb: 1024, share: "pvc-c"}
	if _, err := cs.CreateVolume(ctx, restore("pvc-c", "s2")); err != nil {
		t.Fatalf("source not reported: %v", err)
	}
}

// A create that may have left the volume on the backend is Unavailable (the
// provisioner keeps retrying, also for a PVC deleted meanwhile, and deletes
// what it finally gets). One that created nothing is final.
func TestCreateVolumeErrorCodeFollowsWhatIsLeft(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name                string
		lustre              bool
		failCreate, failVip int
		want                codes.Code
		left                bool
	}{
		{"zfs dataset create answer lost", false, shareLost, 0, codes.Unavailable, true},
		{"zfs dataset create 500", false, http.StatusInternalServerError, 0, codes.Unavailable, false},
		{"zfs dataset create refused 409", false, http.StatusConflict, 0, codes.Internal, false},
		{"zfs dataset create refused 400", false, http.StatusBadRequest, 0, codes.Internal, false},
		{"zfs server address after create", false, 0, http.StatusNotFound, codes.Unavailable, true},
		{"lustre create answer lost", true, shareLost, 0, codes.Unavailable, true},
		{"lustre create 503", true, http.StatusServiceUnavailable, 0, codes.Unavailable, false},
		{"lustre create refused 409", true, http.StatusConflict, 0, codes.Internal, false},
		{"lustre server address after create", true, 0, http.StatusNotFound, codes.Unavailable, true},
	}
	for _, tc := range cases {
		proxy, info := newFakeProxy(t, "pool1")
		proxy.provisiond = true
		cs, _ := newTestController(t, info)
		proxy.failCreate, proxy.failVip = tc.failCreate, tc.failVip
		req := createReq("pvc-1", utils.UNIT_GB)
		if tc.lustre {
			req = lustreCreateReq("pvc-1", utils.UNIT_GB)
		}
		_, err := cs.CreateVolume(ctx, req)
		if status.Code(err) != tc.want {
			t.Fatalf("%s: create = %v, want %s", tc.name, err, tc.want)
		}
		proxy.mu.Lock()
		_, zfsLeft := proxy.pools["pool1"]["pvc-1"]
		_, lustreLeft := proxy.lustre["pvc-1"]
		proxy.mu.Unlock()
		if left := zfsLeft || lustreLeft; left != tc.left {
			t.Fatalf("%s: volume left on the backend = %t, want %t", tc.name, left, tc.left)
		}
	}
}

// A refused share (400) is final only when the dataset is really removed;
// a dataset that could not be removed keeps the create in progress.
func TestCreateVolumeRefusedShareIsFinalOnlyWhenDatasetRemoved(t *testing.T) {
	ctx := context.Background()
	for _, failDelete := range []int{0, http.StatusInternalServerError} {
		proxy, info := newFakeProxy(t, "pool1")
		proxy.provisiond = true
		cs, _ := newTestController(t, info)
		proxy.failShare, proxy.failDelete = http.StatusBadRequest, failDelete
		want, left := codes.Internal, false
		if failDelete != 0 {
			want, left = codes.Unavailable, true
		}
		if _, err := cs.CreateVolume(ctx, createReq("pvc-1", utils.UNIT_GB)); status.Code(err) != want {
			t.Fatalf("failDelete=%d: create = %v, want %s", failDelete, err, want)
		}
		if _, ok := proxy.pools["pool1"]["pvc-1"]; ok != left {
			t.Fatalf("failDelete=%d: dataset left = %t, want %t", failDelete, ok, left)
		}
	}

	// The share-only path (dataset left by an earlier attempt) does the same.
	proxy, info := newFakeProxy(t, "pool1")
	proxy.provisiond = true
	cs, _ := newTestController(t, info)
	proxy.pools["pool1"]["pvc-2"] = &fakeVol{sizeMb: 1024}
	proxy.failShare = http.StatusBadRequest
	if _, err := cs.CreateVolume(ctx, createReq("pvc-2", utils.UNIT_GB)); status.Code(err) != codes.Internal {
		t.Fatalf("share-only refused: create = %v, want Internal", err)
	}
	if _, ok := proxy.pools["pool1"]["pvc-2"]; ok {
		t.Fatal("share-only refused: dataset left behind")
	}
	proxy.failShare = http.StatusInternalServerError
	proxy.pools["pool1"]["pvc-3"] = &fakeVol{sizeMb: 1024}
	if _, err := cs.CreateVolume(ctx, createReq("pvc-3", utils.UNIT_GB)); status.Code(err) != codes.Unavailable {
		t.Fatalf("share-only unknown: create = %v, want Unavailable", err)
	}
}

// A claim deleted while CreateVolume was still unfinished keeps being retried by
// external-provisioner. CreateVolume must then finish (with StorageClass options)
// so the PV is saved and deleted along with the backend volume; any other
// failure to read the claim stays retryable.
func TestCreateVolumeFinishesWhenClaimIsGone(t *testing.T) {
	ctx := context.Background()
	withClaim := func(req *csi.CreateVolumeRequest) *csi.CreateVolumeRequest {
		req.Parameters["csi.storage.k8s.io/pvc/name"] = "gone"
		req.Parameters["csi.storage.k8s.io/pvc/namespace"] = "ns1"
		return req
	}
	for _, lustre := range []bool{false, true} {
		proxy, info := newFakeProxy(t, "pool1")
		proxy.provisiond = true
		cs, _ := newTestController(t, info)
		req := withClaim(createReq("pvc-1", utils.UNIT_GB))
		if lustre {
			req = withClaim(lustreCreateReq("pvc-1", utils.UNIT_GB))
		}
		resp, err := cs.CreateVolume(ctx, req)
		if err != nil || resp.GetVolume().GetVolumeId() == "" {
			t.Fatalf("lustre=%t: create for a deleted claim = %v, %v; want the volume so it can be deleted", lustre, resp, err)
		}
	}

	// The case seen on a device: the first attempt created the volume and failed
	// after it (Unavailable), then the claim was deleted. The retry must finish.
	for _, lustre := range []bool{false, true} {
		proxy, info := newFakeProxy(t, "pool1")
		proxy.provisiond = true
		cs, _ := newTestController(t, info)
		req := withClaim(createReq("pvc-1", utils.UNIT_GB))
		if lustre {
			req = withClaim(lustreCreateReq("pvc-1", utils.UNIT_GB))
		}
		proxy.failVip = http.StatusNotFound
		if _, err := cs.CreateVolume(ctx, req); status.Code(err) != codes.Unavailable {
			t.Fatalf("lustre=%t: first attempt = %v, want Unavailable", lustre, err)
		}
		proxy.mu.Lock()
		proxy.failVip = 0
		proxy.mu.Unlock()
		if resp, err := cs.CreateVolume(ctx, req); err != nil || resp.GetVolume().GetVolumeId() == "" {
			t.Fatalf("lustre=%t: retry after the claim is gone = %v, %v; want the existing volume", lustre, resp, err)
		}
	}

	proxy, info := newFakeProxy(t, "pool1")
	proxy.provisiond = true
	cs, client := newTestController(t, info)
	client.PrependReactor("get", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unreachable")
	})
	_, err := cs.CreateVolume(ctx, withClaim(createReq("pvc-1", utils.UNIT_GB)))
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("claim read failure = %v, want Unavailable", err)
	}
	proxy.mu.Lock()
	_, left := proxy.pools["pool1"]["pvc-1"]
	proxy.mu.Unlock()
	if left {
		t.Fatal("volume created although the claim could not be read")
	}
}
