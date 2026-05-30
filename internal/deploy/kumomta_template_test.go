package deploy

import (
	"strings"
	"testing"
)

func TestRenderInitLua_HideClientIPUsesTraceHeaders(t *testing.T) {
	v := BuildDeployVars("example.com", "mail", "1.2.3.4")
	v.HideClientIP = true

	out, err := RenderInitLua(v)
	if err != nil {
		t.Fatalf("RenderInitLua error: %v", err)
	}

	for _, placeholder := range []string{"{FQDN}", "{DOMAIN}", "{SELECTOR}", "{BIND_IP}", "{TRACE_RECEIVED}", "{TRACE_SUPPLEMENTAL}"} {
		if strings.Contains(out, placeholder) {
			t.Fatalf("init.lua has unreplaced placeholder %s:\n%s", placeholder, out)
		}
	}
	if got := strings.Count(out, "trace_headers = {"); got != 2 {
		t.Fatalf("expected trace_headers on both 25 and 587 listeners, got %d", got)
	}
	if !strings.Contains(out, "received_header = false") || !strings.Contains(out, "supplemental_header = false") {
		t.Fatalf("hide-client-IP mode must disable KumoMTA trace headers:\n%s", out)
	}
	if containsActiveLuaCall(out, "msg:remove_all_named_headers") {
		t.Fatalf("init.lua must not remove all Received headers; that can delete brutal-mailer persona chains:\n%s", out)
	}
	// v0.1.85 起完全删除 egress path shaping：init.lua 不再注册 get_egress_path_config
	// （仅注释里提及）。用 containsActiveLuaCall 跳过注释行，防止旧的 Contains 断言被
	// 注释字符串"假命中"而失去把关，也防止有人误把该 hook 加回来。
	if containsActiveLuaCall(out, "get_egress_path_config") {
		t.Fatalf("v0.1.85 后 init.lua 不应再注册 get_egress_path_config（应走 KumoMTA 内部默认）:\n%s", out)
	}
}

func containsActiveLuaCall(src, call string) bool {
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(trimmed, call) {
			return true
		}
	}
	return false
}

func TestRenderInitLua_VisibleModeKeepsTraceHeaders(t *testing.T) {
	v := BuildDeployVars("example.com", "@", "1.2.3.4")
	v.HideClientIP = false

	out, err := RenderInitLua(v)
	if err != nil {
		t.Fatalf("RenderInitLua error: %v", err)
	}
	if !strings.Contains(out, "received_header = true") || !strings.Contains(out, "supplemental_header = true") {
		t.Fatalf("visible mode must keep KumoMTA trace headers:\n%s", out)
	}
}

func TestRenderInitLua_SourceAddressUsesProvidedInternalIP(t *testing.T) {
	v := BuildDeployVars("example.com", "mail", "10.146.0.12")

	out, err := RenderInitLua(v)
	if err != nil {
		t.Fatalf("RenderInitLua error: %v", err)
	}
	if !strings.Contains(out, "source_address = '10.146.0.12'") {
		t.Fatalf("KumoMTA source_address must use VM internal IP:\n%s", out)
	}
	if strings.Contains(out, "source_address = '35.") {
		t.Fatalf("KumoMTA source_address must not use external NAT IP:\n%s", out)
	}
}

func TestRenderInstallKumoMTAIncludesZstd(t *testing.T) {
	out, err := RenderInstallKumoMTA(BuildDeployVars("example.com", "mail", "1.2.3.4"))
	if err != nil {
		t.Fatalf("RenderInstallKumoMTA error: %v", err)
	}
	if !strings.Contains(out, " zstd") {
		t.Fatalf("install_kumomta.sh must install zstd for archived log extraction:\n%s", out)
	}
}

// TestGenerateMailPassword_NeverEmpty 锁死 P0 修复：DefaultMailPassword 为空时
// 必须回退随机强密码，绝不能再渲染出空密码（空密码 = 587 开放中继 + 空系统账号）。
func TestGenerateMailPassword_NeverEmpty(t *testing.T) {
	for _, domain := range []string{"example.com", "a.co", "sub.example.co.jp"} {
		pw := GenerateMailPassword(domain)
		if pw == "" {
			t.Fatalf("GenerateMailPassword(%q) 返回空密码 —— P0 开放中继漏洞回归", domain)
		}
		if len(pw) < 16 {
			t.Fatalf("GenerateMailPassword(%q) 太短(%d)，强度不足: %q", domain, len(pw), pw)
		}
		for _, c := range pw {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
				t.Fatalf("密码含非字母数字字符 %q，可能破坏 shell/Lua/CSV 转义: %q", c, pw)
			}
		}
	}
	// 两次调用应不同（随机源），避免被硬编码回退
	if GenerateMailPassword("example.com") == GenerateMailPassword("example.com") {
		t.Fatalf("GenerateMailPassword 两次返回相同值，疑似未走随机源")
	}
}

// TestRenderSmtpAuthLua_NoEmptyPassword 锁死 smtp_auth.lua 不会渲染出空密码（587 开放中继）。
func TestRenderSmtpAuthLua_NoEmptyPassword(t *testing.T) {
	v := BuildDeployVars("example.com", "mail", "1.2.3.4")
	out, err := RenderSmtpAuthLua(v)
	if err != nil {
		t.Fatalf("RenderSmtpAuthLua error: %v", err)
	}
	if strings.Contains(out, "local PASSWORD = ''") {
		t.Fatalf("smtp_auth.lua 渲染出空密码 —— 587 开放中继 P0 回归:\n%s", out)
	}
	for _, ph := range []string{"{PASSWORD}", "{USERNAME}"} {
		if strings.Contains(out, ph) {
			t.Fatalf("smtp_auth.lua 有未替换占位符 %s:\n%s", ph, out)
		}
	}
}

// TestRender_PreservesShellVarRefs 锁死 v0.2.13 修复：模板里的 ${FQDN} ${DOMAIN} ${USER%@*}
// 等 shell 变量引用必须原样保留，不能被 strings.ReplaceAll("{FQDN}", ...) 误伤。
// 之前的 bug：${FQDN} 中的 {FQDN} 子串被替换 → 渲染出 $madouchuanm.com → bash 把
// $madouchuanm 当未定义变量展开成空 → 实际值变成 ".com" → /etc/hostname=.com →
// myhostname=.com → postfix master fatal exit。
func TestRender_PreservesShellVarRefs(t *testing.T) {
	v := BuildDeployVars("example.com", "@", "10.0.0.1")
	out, err := RenderInstallPostfix(v)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	// 关键 shell 引用必须原样保留
	for _, ref := range []string{"${FQDN}", "${DOMAIN}", "${SERVER_IP}", `${SASL_USER_FULL}`, `${SASL_USER_FULL%@*}`} {
		if !strings.Contains(out, ref) {
			t.Errorf("shell 变量引用 %s 被 render 破坏（应原样保留）", ref)
		}
	}
	// 关键：不允许出现把域名前缀拼到 $ 后面的破坏形态（"$example" 或 "$example.com"）
	for _, broken := range []string{"$example.com", "$example ", `"$example"`} {
		if strings.Contains(out, broken) {
			t.Errorf("渲染输出含破坏形态 %q（${FQDN} 被误替换）:\n%s", broken, out)
		}
	}
}

// TestRender_RejectsInjectionDomain 锁死域名注入防线：含 shell/Lua 危险字符的域名
// 必须在渲染时被拒（render 单点校验），合法 LDH 域名正常通过。
func TestRender_RejectsInjectionDomain(t *testing.T) {
	bad := []string{
		"example.com;rm -rf /",
		"ex'ample.com",
		"$(whoami).com",
		"a b.com",
		"x`id`.com",
		"foo|bar.com",
		"a&&b.com",
		"",
	}
	for _, d := range bad {
		v := BuildDeployVars(d, "@", "1.2.3.4")
		if _, err := RenderSmtpAuthLua(v); err == nil {
			t.Errorf("含注入字符的域名 %q 应被拒绝渲染，但通过了", d)
		}
		if _, err := RenderInstallPostfix(v); err == nil {
			t.Errorf("含注入字符的域名 %q 应被 Postfix 模板拒绝，但通过了", d)
		}
	}
	// 合法域名（含连字符、多级、子域）应通过
	for _, good := range []string{"good-example.co.jp", "mail.example.com", "a1-b2.example.org"} {
		if _, err := RenderSmtpAuthLua(BuildDeployVars(good, "mail", "1.2.3.4")); err != nil {
			t.Errorf("合法域名 %q 被误拒: %v", good, err)
		}
	}
}
