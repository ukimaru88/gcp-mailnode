package dnsbl

import "testing"

// TestDnsblListedHit 锁死 v0.2.9 软拒绝修复：DNSBL 返回的 A 记录必须落在 127.0.0.0/8 且
// 排除 127.255.255.0/24（RBL 错误/超限/未授权码）才算真正命中，避免把过载应答误判为脏。
func TestDnsblListedHit(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
		want  bool
	}{
		{"标准列入码 127.0.0.2", []string{"127.0.0.2"}, true},
		{"其它列入码 127.0.0.10", []string{"127.0.0.10"}, true},
		{"错误/超限码 127.255.255.252", []string{"127.255.255.252"}, false},
		{"错误码 127.255.255.0", []string{"127.255.255.0"}, false},
		{"非 127/8（劫持/NXDOMAIN 重定向）", []string{"1.2.3.4"}, false},
		{"空返回", nil, false},
		{"混合：错误码 + 真列入码 → 命中", []string{"127.255.255.254", "127.0.0.2"}, true},
		{"非法地址串", []string{"not-an-ip"}, false},
	}
	for _, c := range cases {
		if got := dnsblListedHit(c.addrs); got != c.want {
			t.Errorf("%s: dnsblListedHit(%v) = %v, want %v", c.name, c.addrs, got, c.want)
		}
	}
}
