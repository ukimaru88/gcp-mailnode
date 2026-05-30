package deploy

import (
	"crypto/rand"
	_ "embed"
	"embed"
	"encoding/hex"
	"fmt"
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

// GenerateMailPassword 按默认规则生成邮箱密码（{domain} 替换为根域名第一段）
func GenerateMailPassword(rootDomain string) string {
	prefix := rootDomain
	if i := strings.Index(rootDomain, "."); i > 0 {
		prefix = rootDomain[:i]
	}
	return strings.ReplaceAll(DefaultMailPassword, "{domain}", prefix)
}

// render 读取嵌入模板并按 DeployVars 字段替换占位符
func render(path string, v DeployVars) (string, error) {
	b, err := templatesFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	s := string(b)
	s = strings.ReplaceAll(s, "{FQDN}", v.FQDN)
	s = strings.ReplaceAll(s, "{DOMAIN}", v.RootDomain)
	s = strings.ReplaceAll(s, "{SELECTOR}", v.Selector)
	s = strings.ReplaceAll(s, "{BIND_IP}", v.BindIP)
	s = strings.ReplaceAll(s, "{USERNAME}", v.Username)
	s = strings.ReplaceAll(s, "{PASSWORD}", v.Password)
	s = strings.ReplaceAll(s, "{HIDE_CLIENT_IP}", boolLua(v.HideClientIP))
	traceHeaders := "true"
	if v.HideClientIP {
		traceHeaders = "false"
	}
	s = strings.ReplaceAll(s, "{TRACE_RECEIVED}", traceHeaders)
	s = strings.ReplaceAll(s, "{TRACE_SUPPLEMENTAL}", traceHeaders)
	s = strings.ReplaceAll(s, "{SOURCES_BLOCK}", v.SourcesBlock)
	// v0.1.74：caddy 需要"裸 FQDN"（不要 mail. 前缀，因为退订用根域 https://<域>/u）
	s = strings.ReplaceAll(s, "{FQDN_BARE}", v.RootDomain)
	s = strings.ReplaceAll(s, "{UNSUB_SECRET}", v.UnsubSecret)
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
