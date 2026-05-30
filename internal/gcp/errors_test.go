package gcp

import (
	"errors"
	"testing"
)

// TestIsQuotaExceeded_RealMessages 锁死配额识别：GCP compute/apiv1 在静态 IP 配额
// 耗尽时返回的真实 message 形如 "Quota 'STATIC_ADDRESSES' exceeded. ..."，旧实现的
// "quota_exceeded"/"quota exceeded" 字符串匹配识别不到，导致配额软冷却逻辑失效。
func TestIsQuotaExceeded_RealMessages(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"GCP 静态 IP 配额", errors.New("Quota 'STATIC_ADDRESSES' exceeded. Limit: 8.0 in region asia-northeast1."), true},
		{"GCP 在用 IP 配额", errors.New("rpc error: code = ResourceExhausted desc = Quota 'IN_USE_ADDRESSES' exceeded"), true},
		{"下划线形态", errors.New("QUOTA_EXCEEDED: something"), true},
		{"普通网络错误", errors.New("connection refused"), false},
		{"限流非配额", errors.New("rate limit exceeded"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := IsQuotaExceeded(c.err); got != c.want {
			t.Errorf("%s: IsQuotaExceeded(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}

// TestIsNotFound_Messages 校验 404 字符串兜底覆盖带空格的 "not found"。
func TestIsNotFound_Messages(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("The resource 'projects/x/addresses/y' was not found"), true},
		{errors.New("resource not found"), true},
		{errors.New("NotFound: gone"), true},
		{errors.New("permission denied"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := IsNotFound(c.err); got != c.want {
			t.Errorf("IsNotFound(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
