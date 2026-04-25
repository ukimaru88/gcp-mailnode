package deploy

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed templates/*
var templatesFS embed.FS

const (
	DefaultMailUser     = "info"
	DefaultMailPassword = "KIOuyse21@"
	DefaultSelector     = "default"
	DefaultSMTPPort     = 587
)

// DeployVars 模板变量集
type DeployVars struct {
	FQDN       string
	RootDomain string
	Subdomain  string
	Selector   string
	BindIP     string
	Username   string
	Password   string

	HideClientIP bool
}

// BuildDeployVars 根据根域名+子域名+绑定 IP 构造模板变量
func BuildDeployVars(rootDomain, subdomain, bindIP string) DeployVars {
	subdomain = NormalizeDeploySubdomain(subdomain)
	fqdn := rootDomain
	if subdomain != "@" {
		fqdn = subdomain + "." + rootDomain
	}
	return DeployVars{
		FQDN:       fqdn,
		RootDomain: rootDomain,
		Subdomain:  subdomain,
		Selector:   DefaultSelector,
		BindIP:     bindIP,
		Username:   DefaultMailUser + "@" + fqdn,
		Password:   GenerateMailPassword(rootDomain),
	}
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
	return s, nil
}

// RenderInstallKumoMTA 渲染 KumoMTA 安装脚本
func RenderInstallKumoMTA(v DeployVars) (string, error) {
	return render("templates/install_kumomta.sh", v)
}

// RenderInstallMailcow 渲染 mailcow 安装脚本（收发一体）
func RenderInstallMailcow(v DeployVars) (string, error) {
	return render("templates/install_mailcow.sh", v)
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

