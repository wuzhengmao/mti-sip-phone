package sip

import (
	"net"
	"sort"
	"strings"
)

// LocalIPv4s 本机可用的 IPv4 候选地址（"声明 IP"下拉用，REQ-ACC-023）：
//
//   - **过滤**：回环、没启用的网卡、**虚拟网卡**（Hyper-V/WSL/VMware/VirtualBox/
//     Docker/VPN/TAP 这类，按 MAC 厂商前缀 + 网卡名双重判定）、链路本地
//     169.254/16（DHCP 失败时的自动地址，SIP 用不上）；
//   - **排序**：**公网地址排前面**，其次是运营商级 NAT（100.64/10），
//     RFC1918 内网地址（10/8、172.16/12、192.168/16）排在最后——用户要求
//     "RFC 标准里约定的内网地址排在后面"。
//
// 列表只作**下拉候选**：手填任意地址仍然允许（界面上是可输入的下拉）。
func LocalIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	type cand struct {
		ip   string
		rank int
	}
	var cands []cand
	seen := map[string]bool{}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtualInterface(ifi.Name, ifi.HardwareAddr) {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			v4 := ipNet.IP.To4()
			if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
				continue
			}
			s := v4.String()
			if seen[s] {
				continue
			}
			seen[s] = true
			cands = append(cands, cand{ip: s, rank: ipRank(v4)})
		}
	}
	// 稳定排序：同档内保持网卡枚举顺序
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].rank < cands[j].rank })
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.ip
	}
	return out
}

// ipRank 候选排序权重：公网 0 < 运营商级 NAT 1 < RFC1918 私网 2。
func ipRank(ip net.IP) int {
	switch {
	case isCGNAT(ip):
		return 1
	case ip.IsPrivate(): // RFC1918：10/8、172.16/12、192.168/16
		return 2
	default:
		return 0
	}
}

// cgnatNet 运营商级 NAT（RFC 6598）——不是 RFC1918，但也拿不到公网可达性，
// 排在公网之后、私网之前。
var cgnatNet = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isCGNAT(ip net.IP) bool { return cgnatNet.Contains(ip) }

// virtualNameTokens 网卡名里出现即判为虚拟网卡（小写子串匹配）。
// 只放**足够有辨识度**的词：宁可漏掉一个虚拟网卡（用户还能手填），
// 也不要把真实网卡过滤掉（那会让人以为"网卡列表是空的"）。
var virtualNameTokens = []string{
	"vmware", "virtualbox", "vbox", "hyper-v", "vethernet", "docker", "wsl",
	"veth", "virbr", "br-", "tap-windows", "tun", "utun", "tailscale", "zerotier",
	"hamachi", "radmin", "loopback", "pseudo", "teredo", "isatap", "bluetooth",
	"wi-fi direct", "wifi direct", "npcap", "virtual", "vpn", "wireguard",
}

// virtualMACPrefixes 常见虚拟化平台的 MAC 厂商前缀（网卡名常常认不出来，
// MAC 是更硬的证据）。全是 OUI/前缀，**不做前缀之外的猜测**。
var virtualMACPrefixes = []string{
	"00:15:5d", // Hyper-V / WSL
	"00:50:56", "00:0c:29", "00:05:69", "00:1c:14", // VMware
	"08:00:27", "0a:00:27", // VirtualBox（含 Host-Only）
	"02:42",    // Docker
	"00:16:3e", // Xen
	"52:54:00", // QEMU/KVM
	"00:ff",    // Windows 上的 TAP/OpenVPN 虚拟网卡
}

// isVirtualInterface 是否虚拟网卡（名字或 MAC 命中）。
func isVirtualInterface(name string, mac net.HardwareAddr) bool {
	lower := strings.ToLower(name)
	for _, tok := range virtualNameTokens {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	if len(mac) >= 3 {
		prefix := strings.ToLower(mac.String()[:8]) // "xx:yy:zz"
		for _, p := range virtualMACPrefixes {
			if strings.HasPrefix(prefix, p) {
				return true
			}
		}
	}
	return false
}
