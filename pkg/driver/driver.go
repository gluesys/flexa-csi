/*
Copyright 2025 Gluesys FlexA Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package driver

import (
    "fmt"
    "sync/atomic"

    "github.com/container-storage-interface/spec/lib/go/csi"
    log "github.com/sirupsen/logrus"

    "github.com/gluesys/flexa-csi/pkg/flexa/common"
    "github.com/gluesys/flexa-csi/pkg/interfaces"
    "github.com/gluesys/flexa-csi/pkg/utils"

    //metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
)

const (
    DriverName = "csi.flexa.com" // CSI dirver name
    DriverVersion = "1.0.0"
)

var (
    MultipathEnabled = true
    supportedProtocolList = []string{utils.ProtocolIscsi, utils.ProtocolSmb, utils.ProtocolNfs}
    allowedNfsVersionList = []string{"3", "4", "4.0", "4.1"}
)

type IDriver interface {
    Activate()
}

type Driver struct {
    // *csicommon.CSIDriver
    Name        string
    nodeID      string
    Version     string
    endpoint    string
    csCap       []*csi.ControllerServiceCapability
    vCap        []*csi.VolumeCapability_AccessMode
    nsCap       []*csi.NodeServiceCapability
    FlxService  interfaces.FlexAService
    K8sClient  *kubernetes.Clientset
    PoolName    string

    clientInfo atomic.Value // *common.ClientInfoConfig
}

func NewControllerAndNodeDriver(nodeID string, endpoint string, flxService interfaces.FlexAService) (*Driver, error) {
    log.Debugf("NewControllerAndNodeDriver: DriverName: %v, DriverVersion: %v", DriverName, DriverVersion)


    conf, err := rest.InClusterConfig()
    if err != nil {
        return nil, err
    }

    client, err := kubernetes.NewForConfig(conf)
    if err != nil {
        return nil, err
    }

    // TODO version format and validation
    d := &Driver{
        Name:       DriverName,
        Version:    DriverVersion,
        nodeID:     nodeID,
        endpoint:   endpoint,
        K8sClient: client,
        FlxService: flxService,
    }

    // Initialize with an empty config (caller should set it from client-info.yml / Secret watch).
    d.clientInfo.Store(&common.ClientInfoConfig{Default: nil, Profiles: map[string]common.ProxyInfo{}})

    d.addControllerServiceCapabilities([]csi.ControllerServiceCapability_RPC_Type{
        csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
        csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
        csi.ControllerServiceCapability_RPC_GET_CAPACITY,
        csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
    })

    d.addVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{
        csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
        csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
    })

    d.addNodeServiceCapabilities([]csi.NodeServiceCapability_RPC_Type{
        csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
        csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
        csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
    })

    log.Infof("New driver created: name=%s, nodeID=%s, version=%s, endpoint=%s", d.Name, d.nodeID, d.Version, d.endpoint)
    return d, nil
}

func (d *Driver) SetClientInfoConfig(cfg *common.ClientInfoConfig) {
    if d == nil || cfg == nil {
        return
    }
    if cfg.Profiles == nil {
        cfg.Profiles = map[string]common.ProxyInfo{}
    }
    d.clientInfo.Store(cfg)
}

func (d *Driver) GetClientInfoConfig() *common.ClientInfoConfig {
    if d == nil {
        return &common.ClientInfoConfig{Default: nil, Profiles: map[string]common.ProxyInfo{}}
    }
    v := d.clientInfo.Load()
    if v == nil {
        return &common.ClientInfoConfig{Default: nil, Profiles: map[string]common.ProxyInfo{}}
    }
    cfg, ok := v.(*common.ClientInfoConfig)
    if !ok || cfg == nil {
        return &common.ClientInfoConfig{Default: nil, Profiles: map[string]common.ProxyInfo{}}
    }
    return cfg
}

// SelectProxy returns the proxy for the given profile, or the legacy default when profile is empty.
func (d *Driver) SelectProxy(profile string) (*common.ProxyInfo, error) {
    cfg := d.GetClientInfoConfig()
    if profile != "" {
        if cfg != nil && cfg.Profiles != nil {
            if p, ok := cfg.Profiles[profile]; ok {
                out := p
                return &out, nil
            }
        }
        return nil, fmt.Errorf("proxyProfile %q not found in client-info profiles", profile)
    }
    if cfg != nil && cfg.Default != nil {
        return cfg.Default, nil
    }
    return nil, fmt.Errorf("no proxyProfile specified and legacy default host/port is not configured in client-info")
}

// TODO: func NewNodeDriver() {}
// TODO: func NewControllerDriver() {}


func (d *Driver) Activate() {
    go func() {
        d.StartClientInfoSecretWatch()
        RunControllerandNodePublishServer(d.endpoint, d, NewControllerServer(d), NewNodeServer(d))
    }()
}

func (d *Driver) addControllerServiceCapabilities(cl []csi.ControllerServiceCapability_RPC_Type) {
    var csc []*csi.ControllerServiceCapability

    for _, c := range cl {
        log.Debugf("Enabling controller service capability: %v", c.String())
        csc = append(csc, NewControllerServiceCapability(c))
    }

    d.csCap = csc
    return
}

func (d *Driver) addVolumeCapabilityAccessModes(vc []csi.VolumeCapability_AccessMode_Mode) {
    var vca []*csi.VolumeCapability_AccessMode

    for _, c := range vc {
        log.Debugf("Enabling volume access mode: %v", c.String())
        vca = append(vca, NewVolumeCapabilityAccessMode(c))
    }

    d.vCap = vca
    return
}

func (d *Driver) addNodeServiceCapabilities(nsc []csi.NodeServiceCapability_RPC_Type) {
    var nca []*csi.NodeServiceCapability

    for _, c := range nsc {
        log.Debugf("Enabling node service capability: %v", c.String())
        nca = append(nca, NewNodeServiceCapability(c))
    }

    d.nsCap = nca
    return
}

func (d *Driver) getVolumeCapabilityAccessModes() []*csi.VolumeCapability_AccessMode { // for debugging
    return d.vCap
}

func isProtocolSupport(protocol string) bool {
    return utils.SliceContains(supportedProtocolList, protocol)
}

func isNfsVersionAllowed(ver string) bool {
    return utils.SliceContains(allowedNfsVersionList, ver)
}

