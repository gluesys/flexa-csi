package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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
	"github.com/gluesys/flexa-csi/pkg/utils"
)

// fakeProxy answers like the legacy REST proxy: a missing pool, volume or
// share is 409 with "does not exist" in msg, volume info carries no share path
// and a second share create is 409 "already exists". provisiond=true switches to the
// provisiond shape (share path in volume info, idempotent share create).
type fakeProxy struct {
	mu         sync.Mutex
	pools      map[string]map[string]*fakeVol
	failShare  bool
	provisiond bool
	// poolGone makes every pool answer "does not exist", as the legacy proxy
	// does while its monitor snapshot misses the pool.
	poolGone bool
	calls    []string
	// lustre answers like provisiond for cluster "fs1".
	lustre map[string]*fakeVol
}

type fakeVol struct {
	sizeMb int64
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
				"quota": map[string]int64{"limitMb": v.sizeMb}, "status": st}
		}
		switch {
		case len(seg) == 5 && !exists:
			reply(w, 404, nil, fmt.Sprintf("Share(%s) does not exist", name))
		case len(seg) == 5:
			reply(w, 200, data(v), "")
		case seg[5] == "create":
			// provisiond: a create on a volume whose create is unfinished joins or
			// settles it and answers when the volume is complete.
			if !exists {
				mb, _ := strconv.ParseInt(body["sizeValue"], 10, 64)
				v = &fakeVol{sizeMb: mb}
				p.lustre[name] = v
			}
			v.creating = false
			reply(w, 200, data(v), "")
		default:
			reply(w, 404, nil, "no route")
		}
	case len(seg) == 5 && seg[0] == "zfs" && seg[3] == "vip":
		reply(w, 200, map[string]string{"vip": "10.0.0.9"}, "")
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
			if p.provisiond && v.share != "" {
				data["sharePath"] = fmt.Sprintf("/%s/%s/%s", seg[2], name, v.share)
				data["shareName"] = v.share
			}
			reply(w, 200, data, "")
		case "create":
			if exists {
				reply(w, 409, nil, fmt.Sprintf("Volume(%s) already exists", name))
				return
			}
			mb, _ := strconv.ParseInt(body["sizeValue"], 10, 64)
			pool[name] = &fakeVol{sizeMb: mb}
			reply(w, 200, map[string]string{"name": name}, "")
		case "delete":
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
			if p.failShare {
				reply(w, 500, nil, "share create failed")
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

	proxy.failShare = true
	if _, err := cs.CreateVolume(ctx, createReq("pvc-1", utils.UNIT_GB)); status.Code(err) != codes.Internal {
		t.Fatalf("create with failing share = %v, want Internal", err)
	}
	if _, ok := proxy.pools["pool1"]["pvc-1"]; ok {
		t.Fatal("rollback left the dataset behind")
	}

	// A dataset left without its share (e.g. the driver died in between) gets
	// only the share on the next attempt.
	proxy.failShare = false
	proxy.pools["pool1"]["pvc-2"] = &fakeVol{sizeMb: 1024}
	resp, err := cs.CreateVolume(ctx, createReq("pvc-2", utils.UNIT_GB))
	if err != nil {
		t.Fatalf("create over a share-less dataset: %v", err)
	}
	if resp.GetVolume().GetVolumeContext()["baseDir"] == "" || proxy.pools["pool1"]["pvc-2"].share != "pvc-2" {
		t.Fatal("share was not created")
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

	// ZFS: wait (Unavailable) and touch nothing; the external-provisioner retries.
	proxy.provisiond = true
	proxy.pools["pool1"]["pvc-z"] = &fakeVol{sizeMb: 1024, share: "pvc-z", creating: true}
	if _, err := cs.CreateVolume(ctx, createReq("pvc-z", utils.UNIT_GB)); status.Code(err) != codes.Unavailable {
		t.Fatalf("zfs create over an unfinished one = %v, want Unavailable", err)
	}
	for _, c := range proxy.calls {
		if strings.HasPrefix(c, "POST ") && strings.Contains(c, "pvc-z") {
			t.Fatalf("zfs unfinished create sent %q", c)
		}
	}
	proxy.pools["pool1"]["pvc-z"].creating = false
	if _, err := cs.CreateVolume(ctx, createReq("pvc-z", utils.UNIT_GB)); err != nil {
		t.Fatalf("zfs create after the backend finished: %v", err)
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
