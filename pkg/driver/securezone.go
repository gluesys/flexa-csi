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

// secureZoneName 은 보안존 이름을 네트워크로 정한다. 같은 네트워크의 볼륨은 보안존 하나를 같이 쓴다.
// 예: ("172.16.0.0", "255.255.192.0") → "zone_csi_172-16-0-0_18".
// 기존에는 PVC 마다 zone_<PVC> 를 만들어 같은 대역의 보안존이 볼륨 수만큼 늘었다.
// provisiond 는 같은 이름·같은 네트워크면 재사용하고, 마지막 공유가 지워질 때 보안존을 지운다.
func secureZoneName(addr, sub string) string {
	mask := sanitizeZonePart(sub)
	if ip := net.ParseIP(strings.TrimSpace(sub)).To4(); ip != nil {
		if ones, bits := net.IPMask(ip).Size(); bits != 0 {
			mask = fmt.Sprint(ones)
		}
	}
	return fmt.Sprintf("zone_csi_%s_%s", sanitizeZonePart(addr), mask)
}

// sanitizeZonePart 는 보안존 이름에 쓸 수 없는 글자('.', ':' 등)를 '-' 로 바꾼다.
func sanitizeZonePart(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, strings.TrimSpace(s))
}
