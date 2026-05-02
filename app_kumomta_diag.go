package main

import (
	"context"
	"fmt"
	"strings"

	"gcp-mailnode/internal/ssh"
	"gcp-mailnode/internal/sshkey"
)

type KumoMTADiagnosticDTO struct {
	VPSID      string `json:"vps_id"`
	IP         string `json:"ip"`
	InternalIP string `json:"internal_ip"`
	FQDN       string `json:"fqdn"`
	OK         bool   `json:"ok"`
	Summary    string `json:"summary"`
	Detail     string `json:"detail"`
}

func (a *App) DiagnoseKumoMTA(vpsID string) (KumoMTADiagnosticDTO, error) {
	db, err := requireDB()
	if err != nil {
		return KumoMTADiagnosticDTO{}, err
	}
	var ip, internalIP, fqdn, domain, deployType string
	err = db.QueryRowContext(context.Background(), `
		SELECT ip, COALESCE(internal_ip,''), fqdn, domain, COALESCE(deploy_type,'kumomta')
		FROM vps_instances WHERE id=?`, vpsID).Scan(&ip, &internalIP, &fqdn, &domain, &deployType)
	if err != nil {
		return KumoMTADiagnosticDTO{}, err
	}
	if deployType != "" && deployType != "kumomta" {
		return KumoMTADiagnosticDTO{}, fmt.Errorf("该 VPS deploy_type=%s，不是 KumoMTA 节点", deployType)
	}
	if ip == "" {
		return KumoMTADiagnosticDTO{}, fmt.Errorf("VPS 缺少外网 IP")
	}

	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	sshCfg := ssh.Config{Host: ip, Port: 22, Username: "root", KeyContent: string(sshkey.PrivatePEM())}
	if strings.TrimSpace(internalIP) == "" {
		if detected, derr := detectRemoteInternalIPForApp(ctx, sshCfg); derr == nil {
			internalIP = detected
			_, _ = db.ExecContext(ctx, `UPDATE vps_instances SET internal_ip=? WHERE id=?`, internalIP, vpsID)
		}
	}

	cmd := fmt.Sprintf(`set +e
echo "===== IDENTITY ====="
printf 'fqdn=%%s\n' %s
printf 'domain=%%s\n' %s
printf 'external_ip=%%s\n' %s
printf 'stored_internal_ip=%%s\n' %s
echo
echo "===== IP ADDR ====="
ip -o -4 addr show scope global || true
echo
echo "===== PORTS ====="
ss -tlnp 2>/dev/null | grep -E ':(25|465|587)\b' || true
echo
echo "===== KUMOMTA STATUS ====="
systemctl status kumomta --no-pager -l 2>&1 || true
echo
echo "===== POLICY ====="
sed -n '1,240p' /opt/kumomta/etc/policy/init.lua 2>&1 || true
echo
echo "===== AUTH POLICY ====="
sed -n '1,160p' /opt/kumomta/etc/policy/smtp_auth.lua 2>&1 || true
echo
echo "===== DKIM KEYS ====="
ls -la /opt/kumomta/etc/keys 2>&1 || true
find /opt/kumomta/etc/keys -maxdepth 3 -type f -printf '%%M %%u:%%g %%p\n' 2>&1 || true
echo
echo "===== KUMOMTA JOURNAL ====="
journalctl -u kumomta --since '30 minutes ago' --no-pager -n 300 2>&1 || true
echo
echo "===== KUMOMTA LOGS ====="
for f in /var/log/kumomta/*.log; do [ -f "$f" ] && { echo "--- $f ---"; tail -n 120 "$f"; }; done 2>&1 || true
`, shellLiteral(fqdn), shellLiteral(domain), shellLiteral(ip), shellLiteral(internalIP))

	out, err := ssh.RunCommand(ctx, sshCfg, cmd)
	if err != nil {
		return KumoMTADiagnosticDTO{}, err
	}
	summary, ok := summarizeKumoMTADiagnostic(out, ip, internalIP)
	return KumoMTADiagnosticDTO{
		VPSID:      vpsID,
		IP:         ip,
		InternalIP: internalIP,
		FQDN:       fqdn,
		OK:         ok,
		Summary:    summary,
		Detail:     out,
	}, nil
}

func summarizeKumoMTADiagnostic(out, externalIP, internalIP string) (string, bool) {
	lower := strings.ToLower(out)
	findings := []string{}
	if strings.Contains(lower, "active: active") {
		findings = append(findings, "KumoMTA 服务 active")
	} else {
		findings = append(findings, "KumoMTA 服务可能未 active")
	}
	if strings.Contains(out, ":587") {
		findings = append(findings, "587 端口已监听")
	} else {
		findings = append(findings, "587 端口未确认监听")
	}
	if internalIP != "" && strings.Contains(out, "source_address = '"+externalIP+"'") {
		findings = append(findings, "init.lua 仍绑定外网 IP，需要重新部署 KumoMTA 配置")
	}
	if internalIP != "" && strings.Contains(out, "source_address = '"+internalIP+"'") {
		findings = append(findings, "init.lua 已绑定内网 IP")
	}
	for _, marker := range []string{"lua", "dkim", "permission denied", "no such file", "policy", "failed", "technical difficulties"} {
		if strings.Contains(lower, marker) {
			findings = append(findings, "日志包含 "+marker+"，请看详情")
			break
		}
	}
	ok := strings.Contains(lower, "active: active") && strings.Contains(out, ":587")
	return strings.Join(findings, "；"), ok
}

func detectRemoteInternalIPForApp(ctx context.Context, sshCfg ssh.Config) (string, error) {
	out, err := ssh.RunCommand(ctx, sshCfg, `ip -o -4 addr show scope global | awk '{split($4,a,"/"); print a[1]; exit}'`)
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(out)
	if ip == "" {
		return "", fmt.Errorf("remote internal IP is empty")
	}
	return ip, nil
}

func shellLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
