// Copyright 2025 FlexA Inc.

package driver

import (
	"fmt"
	"net"
	"strings"

	"github.com/gluesys/flexa-csi/pkg/flexa/common"
)

// StorageClass parameter that picks the network nodes mount this class over,
// e.g. "198.51.100.0/24" to put NFS on an IB data network while the proxy
// profile's mountIP stays on the management network. It replaces the profile
// mountIP for volumes of this class: the backend resolves the server address
// (vip) in this network and the default NFS secure zone is this network.
const paramMountNetwork = "mountNetwork"

// proxyForClass returns the proxy to use for a StorageClass. With mountNetwork
// it is a copy of proxy with MountIP replaced; proxy itself is shared and must
// not change.
func proxyForClass(proxy *common.ProxyInfo, params map[string]string) (*common.ProxyInfo, error) {
	n := strings.TrimSpace(params[paramMountNetwork])
	if n == "" {
		return proxy, nil
	}
	if _, err := mountNetworkCIDR(n); err != nil {
		return nil, err
	}
	out := *proxy
	out.MountIP = n
	return &out, nil
}

// mountNetworkCIDR parses mountNetwork. A single IP means its /24, as the
// backend and the default secure zone read it.
func mountNetworkCIDR(n string) (*net.IPNet, error) {
	if !strings.Contains(n, "/") {
		n += "/24"
	}
	ip, ipNet, err := net.ParseCIDR(n)
	if err != nil || ip.To4() == nil {
		return nil, fmt.Errorf("invalid StorageClass %s %q: want an IPv4 address or CIDR", paramMountNetwork, n)
	}
	return ipNet, nil
}

// checkMountNetwork fails when the backend has no server address in the
// mountNetwork of the class. The backend falls back to a management IP in that
// case; mounting over it would put NFS back on the management network, and the
// secure zone (mountNetwork) would refuse the nodes anyway.
func checkMountNetwork(params map[string]string, vip string) error {
	n := strings.TrimSpace(params[paramMountNetwork])
	if n == "" {
		return nil
	}
	ipNet, err := mountNetworkCIDR(n)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(strings.TrimSpace(vip)); ip == nil || !ipNet.Contains(ip) {
		return fmt.Errorf("storage has no address in StorageClass %s %s (got %q); "+
			"add a VIP or interface address in that network on the storage", paramMountNetwork, n, vip)
	}
	return nil
}
