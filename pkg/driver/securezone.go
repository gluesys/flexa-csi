/*
Copyright 2026 Gluesys FlexA Inc.
*/

package driver

import (
	"fmt"
	"net"
	"strings"
)

// defaultSecureZone 는 NFS export 를 허용할 보안존 네트워크를 정한다.
// PVC 어노테이션(flexa.io/secureAddress, flexa.io/secureSubnet)이 있으면 그 값을 쓰고,
// 둘 다 없으면 프록시 profile 의 mountIP 대역(노드가 스토리지에 붙는 망)을 쓴다.
// 예: mountIP "172.16.0.0/18" → ("172.16.0.0", "255.255.192.0").
// 한쪽만 있으면 오류다. 기존에는 빈 주소로 보안존을 만들어 export 가 깨졌다.
func defaultSecureZone(addr, sub, mountIP string) (string, string, error) {
	addr, sub, mountIP = strings.TrimSpace(addr), strings.TrimSpace(sub), strings.TrimSpace(mountIP)
	if addr != "" && sub != "" {
		return addr, sub, nil
	}
	if addr != "" || sub != "" {
		return "", "", fmt.Errorf("flexa.io/secureAddress and flexa.io/secureSubnet must be set together")
	}
	if mountIP == "" {
		return "", "", fmt.Errorf("flexa.io/secureAddress/secureSubnet are not set and the proxy profile has no mountIP")
	}
	cidr := mountIP
	if !strings.Contains(cidr, "/") {
		cidr += "/24"
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", fmt.Errorf("invalid mountIP %q: %v", mountIP, err)
	}
	if ipnet.IP.To4() == nil {
		return "", "", fmt.Errorf("mountIP %q is not IPv4; set flexa.io/secureAddress/secureSubnet", mountIP)
	}
	return ipnet.IP.String(), net.IP(ipnet.Mask).String(), nil
}
