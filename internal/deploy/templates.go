package deploy

import (
	"crypto/rand"
	_ "embed"
	"embed"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// hexRandom32 生成 32 字节随机数据的 hex 编码（64 字符）
func hexRandom32() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

//go:embed templates/*
var templatesFS embed.FS

// v0.1.74：嵌入预编译的 unsub-server linux/amd64 二进制（~9MB）
// 部署时 base64 编码后替换 install_unsub.sh 的 {UNSUB_BINARY_B64} 占位符
//
//go:embed embed/unsub-server-linux-amd64
var unsubServerBinary []byte

const (
	DefaultMailUser = "info"
	// 邮箱密码默认留空，不硬编码（由使用者运行时填写）
	DefaultMailPassword = ""
	DefaultSelector     = "default"
	DefaultSMTPPort     = 587
)

// SourceSpec v0.1.57：KumoMTA egress source 单元（多 NIC 模式每个 NIC 一个）。
// IP 为 KumoMTA bind 的内网 IP；EHLO 是 SMTP HELO 用的主机名（一般 mail{N}.<rootDomain>）。
type SourceSpec struct {
	Name string
	IP   string
	EHLO string
}

// DeployVars 模板变量集
type DeployVars struct {
	FQDN       string
	RootDomain string
	Subdomain  string
	Selector   string
	BindIP     string // 主 NIC 内网 IP（多 NIC 模式仍然是 nic0 内网 IP，给单 source 兜底）
	Username   string
	Password   string

	HideClientIP bool

	// v0.1.57：渲染好的 KumoMTA Lua 多 source/pool 代码块（init.lua.tmpl {SOURCES_BLOCK}）。
	// 单 source 时由 BuildDeployVars 自动生成；多 NIC 时调用 BuildSourcesBlock 拼好。
	SourcesBlock string

	// v0.1.74：退订服务参数。UnsubSecret 由 Stage C 调用方生成（每台 VPS 独立），
	// 写入数据库 vps_instances.unsub_secret，导出 SMTP 时一并导出给 brutal-mailer。
	UnsubSecret string
}

// BuildDeployVars 根据根域名+子域名+绑定 IP 构造模板变量（单 NIC 模式或多 NIC 兜底）
func BuildDeployVars(rootDomain, subdomain, bindIP string) DeployVars {
	return BuildDeployVarsMultiNIC(rootDomain, subdomain, bindIP, nil)
}

// SanitizeMailUser 校验邮箱 local-part：只允 [a-z0-9._-]，1-32 字符；空或非法回退 DefaultMailUser。
// v0.2.19：用户在 Stage C UI 自定义账号前缀（"info"→"sales"/"hello"/...）。
func SanitizeMailUser(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 32 {
		return DefaultMailUser
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
		default:
			return DefaultMailUser
		}
	}
	// 不能以点或连字符开头/结尾（postfix/sasldb 兼容性）
	if s[0] == '.' || s[0] == '-' || s[len(s)-1] == '.' || s[len(s)-1] == '-' {
		return DefaultMailUser
	}
	return s
}

// OverrideMailUser 用指定的 local-part 覆盖 v.Username（=local + "@" + RootDomain）。
// 空字符串保持原值不变。调用方应先 SanitizeMailUser。
func (v *DeployVars) OverrideMailUser(local string) {
	local = SanitizeMailUser(local)
	if local == "" || v.RootDomain == "" {
		return
	}
	v.Username = local + "@" + v.RootDomain
}

// BuildDeployVarsMultiNIC v0.1.57：多 NIC 模式下传 sources（每 NIC 一个 SourceSpec）。
// sources 为空时按单 source 行为生成（用 bindIP + fqdn 自动填）。
func BuildDeployVarsMultiNIC(rootDomain, subdomain, bindIP string, sources []SourceSpec) DeployVars {
	subdomain = NormalizeDeploySubdomain(subdomain)
	fqdn := rootDomain
	if subdomain != "@" {
		fqdn = subdomain + "." + rootDomain
	}
	if len(sources) == 0 {
		sources = []SourceSpec{{Name: "primary", IP: bindIP, EHLO: fqdn}}
	}
	return DeployVars{
		FQDN:       fqdn,
		RootDomain: rootDomain,
		Subdomain:  subdomain,
		Selector:   DefaultSelector,
		BindIP:     bindIP,
		// v0.2.6：账号统一用 RootDomain（与 mail-toolkit 约定一致：From=info@根域，
		// 发件器靠根域自动找 smtp.根域）。之前 KumoMTA 路径用 info@fqdn 在 subdomain
		// 非 @ 时会变成 info@mail.x，跟用户预期不一致。
		Username:     DefaultMailUser + "@" + rootDomain,
		Password:     GenerateMailPassword(rootDomain),
		SourcesBlock: BuildSourcesBlock(sources),
	}
}

// BuildSourcesBlock v0.1.57：拼出 KumoMTA 2026.03+ get_egress_source / get_egress_pool Lua 代码。
// 单 source 时仍输出标准块（保持 init.lua 行为不变）；多 source 时按 source_name 分发，
// pool entries 列出全部 source 让 KumoMTA 默认按 weighted=1 随机均等轮换。
func BuildSourcesBlock(srcs []SourceSpec) string {
	if len(srcs) == 0 {
		return ""
	}
	var b strings.Builder
	if len(srcs) == 1 {
		s := srcs[0]
		fmt.Fprintf(&b, "kumo.on('get_egress_source', function(source_name)\n")
		fmt.Fprintf(&b, "  return kumo.make_egress_source { name=source_name, source_address = '%s', ehlo_domain = '%s' }\nend)\n\n", s.IP, s.EHLO)
		fmt.Fprintf(&b, "kumo.on('get_egress_pool', function(pool_name)\n")
		fmt.Fprintf(&b, "  return kumo.make_egress_pool { name=pool_name, entries={ { name='%s' } } }\nend)\n", s.Name)
		return b.String()
	}
	// 多 source：每个 IP 一个 source，pool entries 平均权重轮换
	b.WriteString("-- v0.1.57 多 NIC：每 IP 一个 egress source，pool 默认 weighted=1 随机均等轮换\n")
	b.WriteString("kumo.on('get_egress_source', function(source_name)\n")
	for _, s := range srcs {
		fmt.Fprintf(&b, "  if source_name == '%s' then return kumo.make_egress_source { name='%s', source_address = '%s', ehlo_domain = '%s' } end\n",
			s.Name, s.Name, s.IP, s.EHLO)
	}
	b.WriteString("  error('unknown egress source: '..source_name)\nend)\n\n")
	b.WriteString("kumo.on('get_egress_pool', function(pool_name)\n")
	b.WriteString("  return kumo.make_egress_pool {\n    name = pool_name,\n    entries = {\n")
	for _, s := range srcs {
		fmt.Fprintf(&b, "      { name='%s' },\n", s.Name)
	}
	b.WriteString("    },\n  }\nend)\n")
	return b.String()
}

// GenerateMailPassword 按默认规则生成邮箱密码（{domain} 替换为根域名第一段）。
//
// P0 安全修复（2026-05-30）：DefaultMailPassword 默认为空，旧实现会返回空串，
// 导致 KumoMTA smtp_auth.lua / Postfix saslpasswd2 / chpasswd 全部注册空密码
// → 587 开放中继 + 空密码系统账号。现改为：模板结果为空时回退到随机强密码。
// 密码字符集限定 [A-Za-z0-9]（剔除易混字符），保证安全进入 shell（chpasswd /
// saslpasswd2）、Lua 单引号字符串字面量、CSV 三处时都不会被特殊字符破坏转义。
// 注意：同一次部署内 BuildDeployVars 只调一次，渲染 + 落库共用同一 v.Password，
// CSV 从 DB 读，故单次部署天然一致；重跑 Stage C 会重新生成（同时更新 DB），
// 需重新导出 CSV 给 brutal-mailer。
func GenerateMailPassword(rootDomain string) string {
	prefix := rootDomain
	if i := strings.Index(rootDomain, "."); i > 0 {
		prefix = rootDomain[:i]
	}
	pw := strings.ReplaceAll(DefaultMailPassword, "{domain}", prefix)
	if pw == "" {
		pw = secureRandomPassword(20)
	}
	return pw
}

// secureRandomPassword 生成 n 位 [A-Za-z0-9] 随机密码（crypto/rand 源，剔除
// 易混的 0/O/1/l/I）。仅用字母数字，确保密码可安全嵌入 shell 命令、Lua 字符串
// 字面量与 CSV，不会触发引号/反斜杠/$ 等转义问题。
func secureRandomPassword(n int) string {
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	if n <= 0 {
		n = 20
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败极罕见；退回 hex（仍非空、仍随机），不破坏 P0 修复目标
		h := hexRandom32()
		if len(h) >= n {
			return h[:n]
		}
		return h
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}

// isSafeDomainValue 仅允许 LDH 字符（字母/数字/点/连字符）。用于在模板渲染前校验
// 域名类变量，确保它们安全嵌入 root 权限 shell 命令（hostnamectl / opendkim-genkey 等）
// 与 KumoMTA Lua 单引号字符串字面量，杜绝 ' " $ ` ; | & 空格 () 等注入字符。
func isSafeDomainValue(s string) bool {
	if s == "" || len(s) > 253 || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// validateDeployDomains 在渲染任何模板前校验域名类变量（FQDN/RootDomain/Subdomain）。
// 这是防 shell/Lua 注入的单点防线：render 是所有部署模板的唯一出口，任何域名只要含
// 危险字符就在此被拒，KumoMTA/Postfix/mailcow/DKIM 全部路径一并受保护。
// 注：Username=info@RootDomain 含 '@' 不单独校验（其域名部分已由 RootDomain 覆盖）；
// Password 已在 GenerateMailPassword 限定 [A-Za-z0-9]；BindIP/Selector 为内部生成值。
func validateDeployDomains(v DeployVars) error {
	if !isSafeDomainValue(v.RootDomain) {
		return fmt.Errorf("根域名 %q 含非法字符（仅允许字母数字 . -），拒绝渲染以防注入", v.RootDomain)
	}
	if !isSafeDomainValue(v.FQDN) {
		return fmt.Errorf("FQDN %q 含非法字符（仅允许字母数字 . -），拒绝渲染以防注入", v.FQDN)
	}
	if v.Subdomain != "@" && !isSafeDomainValue(v.Subdomain) {
		return fmt.Errorf("子域名 %q 含非法字符（仅允许字母数字 . -），拒绝渲染以防注入", v.Subdomain)
	}
	return nil
}

// shellVarRE 匹配所有 bash/sh 风格的变量引用 ${VAR} 和 ${VAR%xxx} ${VAR:-xxx} 等。
// render 替换 Go 占位符 {FQDN}/{DOMAIN}/... 前先把这些"shell ${...}"用 sentinel 替换出去，
// 避免 strings.ReplaceAll("{FQDN}", ...) 误把 ${FQDN} 中的 {FQDN} 子串也替换掉。
//
// v0.2.13：修一个从 v0.2.3（Postfix 路径引入 ${FQDN} ${DOMAIN} 等 shell 变量时）就存在的
// 致命渲染 bug：install_postfix.sh line 200 `echo "${FQDN}" > /etc/hostname` 会被渲染成
// `echo "$madouchuanm.com" > /etc/hostname` —— bash 把 $madouchuanm 当未定义变量展开成空串，
// 结果 /etc/hostname 写入 ".com"，进而 main.cf 的 myhostname=.com，postfix master fatal exit。
// 之前的 v0.2.11 sanity check 拦住了部署但没修根因；本版从源头修复。
var shellVarRE = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(?:[%#:][^}]*)?\}`)

// render 读取嵌入模板并按 DeployVars 字段替换占位符
func render(path string, v DeployVars) (string, error) {
	if err := validateDeployDomains(v); err != nil {
		return "", err
	}
	b, err := templatesFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	s := string(b)

	// 阶段 1：把所有 shell ${VAR}/${VAR%xx} 等用唯一 sentinel 替换出去，保护它们不被
	// strings.ReplaceAll 误伤。sentinel 用 \x00 包围，原始模板里不可能出现。
	shellRefs := shellVarRE.FindAllString(s, -1)
	const sentinelPrefix = "\x00GMSHELL\x00"
	for i, ref := range shellRefs {
		s = strings.Replace(s, ref, fmt.Sprintf("%s%d\x00", sentinelPrefix, i), 1)
	}

	// 阶段 2：替换 Go 占位符（注意先长后短：{FQDN_BARE} 必须在 {FQDN} 之前，否则前者残留 "_BARE}"）
	s = strings.ReplaceAll(s, "{FQDN_BARE}", v.RootDomain)
	s = strings.ReplaceAll(s, "{FQDN}", v.FQDN)
	s = strings.ReplaceAll(s, "{DOMAIN}", v.RootDomain)
	s = strings.ReplaceAll(s, "{SELECTOR}", v.Selector)
	s = strings.ReplaceAll(s, "{BIND_IP}", v.BindIP)
	s = strings.ReplaceAll(s, "{USERNAME}", v.Username)
	// v0.2.19：mailcow 等模板需要"裸 local-part"（不含 @域）。从 Username 取 @ 前缀。
	mailLocal := DefaultMailUser
	if at := strings.Index(v.Username, "@"); at > 0 {
		mailLocal = v.Username[:at]
	}
	s = strings.ReplaceAll(s, "{MAIL_USER_LOCAL}", mailLocal)
	s = strings.ReplaceAll(s, "{PASSWORD}", v.Password)
	s = strings.ReplaceAll(s, "{HIDE_CLIENT_IP}", boolLua(v.HideClientIP))
	traceHeaders := "true"
	if v.HideClientIP {
		traceHeaders = "false"
	}
	s = strings.ReplaceAll(s, "{TRACE_RECEIVED}", traceHeaders)
	s = strings.ReplaceAll(s, "{TRACE_SUPPLEMENTAL}", traceHeaders)
	s = strings.ReplaceAll(s, "{SOURCES_BLOCK}", v.SourcesBlock)
	s = strings.ReplaceAll(s, "{UNSUB_SECRET}", v.UnsubSecret)

	// 阶段 3：恢复 shell ${VAR} 引用
	for i, ref := range shellRefs {
		s = strings.Replace(s, fmt.Sprintf("%s%d\x00", sentinelPrefix, i), ref, 1)
	}
	return s, nil
}

// RenderInstallUnsub v0.1.74：渲染退订服务安装脚本。
// v0.1.78：不再 base64 内联二进制（撞 SSH ARG_MAX，5 台同时 EOF）；改由调用方先 ssh.UploadBytes
// 上传二进制到 /tmp/unsub-server，脚本里 mv 即可。本函数只负责模板替换。
func RenderInstallUnsub(v DeployVars) (string, error) {
	if v.UnsubSecret == "" {
		return "", fmt.Errorf("UnsubSecret 必填")
	}
	b, err := templatesFS.ReadFile("templates/install_unsub.sh")
	if err != nil {
		return "", fmt.Errorf("读取 install_unsub.sh: %w", err)
	}
	s := string(b)
	s = strings.ReplaceAll(s, "{FQDN}", v.FQDN)
	s = strings.ReplaceAll(s, "{FQDN_BARE}", v.RootDomain)
	s = strings.ReplaceAll(s, "{DOMAIN}", v.RootDomain)
	s = strings.ReplaceAll(s, "{UNSUB_SECRET}", v.UnsubSecret)
	return s, nil
}

// UnsubServerBinary v0.1.78：暴露嵌入的 unsub-server 二进制给 stages.go 用 ssh.UploadBytes 上传。
// 长度 0 时跳过部署退订服务。
func UnsubServerBinary() []byte { return unsubServerBinary }

// GenerateUnsubSecret v0.1.74：生成 32 字节随机 HMAC 密钥（hex 编码 64 字符）
func GenerateUnsubSecret() string {
	return hexRandom32()
}

// RenderInstallKumoMTA 渲染 KumoMTA 安装脚本
func RenderInstallKumoMTA(v DeployVars) (string, error) {
	return render("templates/install_kumomta.sh", v)
}

// RenderPolicyRouting v0.1.57：渲染多 NIC policy routing 脚本（只换 {NIC_COUNT} 占位符）
func RenderPolicyRouting(nicCount int) (string, error) {
	b, err := templatesFS.ReadFile("templates/setup_policy_routing.sh")
	if err != nil {
		return "", fmt.Errorf("读取 setup_policy_routing.sh: %w", err)
	}
	return strings.ReplaceAll(string(b), "{NIC_COUNT}", fmt.Sprintf("%d", nicCount)), nil
}

// RenderInstallMailcow 渲染 mailcow 安装脚本（收发一体）
func RenderInstallMailcow(v DeployVars) (string, error) {
	return render("templates/install_mailcow.sh", v)
}

// RenderInstallPostfix 渲染 Postfix + OpenDKIM 一站式部署脚本（纯发信，与 mail-toolkit 同源）
// 脚本末尾打印 DKIM_PUBLIC_KEY=... 单行 base64，调用方用 extractDKIMPublicKey 捕获
func RenderInstallPostfix(v DeployVars) (string, error) {
	return render("templates/install_postfix.sh", v)
}

// RenderDkimSetup 渲染 DKIM 生成脚本
func RenderDkimSetup(v DeployVars) (string, error) {
	return render("templates/dkim_setup.sh", v)
}

// RenderInitLua 渲染 init.lua
func RenderInitLua(v DeployVars) (string, error) {
	return render("templates/init.lua.tmpl", v)
}

// RenderSmtpAuthLua 渲染 smtp_auth.lua
func RenderSmtpAuthLua(v DeployVars) (string, error) {
	return render("templates/smtp_auth.lua.tmpl", v)
}

// GetDeployConfigScript 返回 deploy_config.sh 原文（不做变量替换，由脚本通过参数接受 base64）
func GetDeployConfigScript() (string, error) {
	b, err := templatesFS.ReadFile("templates/deploy_config.sh")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// boolLua 把 Go bool 转成 Lua 字面量 "true" / "false"
func boolLua(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
