package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"gcp-mailnode/internal/crypto"
	"gcp-mailnode/internal/gcp"
	"gcp-mailnode/internal/logger"
)

// loadGCPClient 加载指定凭证并构造 gcp.Client。复用 TestGCPCredential 的解密 + NewClient 模式。
func loadGCPClient(ctx context.Context, db *sql.DB, credID string) (*gcp.Client, error) {
	var name, authType, projectID string
	var encBlob []byte
	row := db.QueryRow(`SELECT name, auth_type, project_id, encrypted_blob FROM gcp_credentials WHERE id=?`, credID)
	if err := row.Scan(&name, &authType, &projectID, &encBlob); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("未找到凭证 %s", credID)
		}
		return nil, err
	}
	var blob []byte
	if len(encBlob) > 0 {
		dec, err := crypto.Decrypt(encBlob)
		if err != nil {
			return nil, fmt.Errorf("解密凭证失败: %w", err)
		}
		blob = dec
	}
	return gcp.NewClient(ctx, gcp.Credential{
		ID:        credID,
		Name:      name,
		AuthType:  gcp.AuthType(authType),
		ProjectID: projectID,
		Blob:      blob,
	})
}

// loadAllowlist 从 gcp_firewall_allowlist 表读出指定凭证的白名单。
// 表里没有记录或 allowed_ips 为空数组时，返回 nil（空白名单 = 维持全开）。
func loadAllowlist(db *sql.DB, credID string) ([]string, error) {
	var raw string
	row := db.QueryRow(`SELECT allowed_ips FROM gcp_firewall_allowlist WHERE cred_id=?`, credID)
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ips []string
	if err := json.Unmarshal([]byte(raw), &ips); err != nil {
		return nil, fmt.Errorf("解析 allowed_ips JSON 失败: %w", err)
	}
	if len(ips) == 0 {
		return nil, nil
	}
	return ips, nil
}

// normalizeCIDR 把单 IP 自动补 /32（IPv4）或 /128（IPv6）；带掩码的走 ParseCIDR 校验。
func normalizeCIDR(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("空 CIDR")
	}
	if strings.Contains(s, "/") {
		_, _, err := net.ParseCIDR(s)
		if err != nil {
			return "", fmt.Errorf("非法 CIDR %q: %w", s, err)
		}
		return s, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return "", fmt.Errorf("非法 IP %q", s)
	}
	if ip.To4() != nil {
		return s + "/32", nil
	}
	return s + "/128", nil
}

// GetFirewallAllowlist 返回指定 GCP 凭证已保存的防火墙白名单 CIDR 列表。
// 表里没有记录时返回空数组。
func (a *App) GetFirewallAllowlist(credID string) ([]string, error) {
	db, err := requireDB()
	if err != nil {
		return nil, err
	}
	ips, err := loadAllowlist(db, credID)
	if err != nil {
		return nil, err
	}
	if ips == nil {
		return []string{}, nil
	}
	return ips, nil
}

// UpdateFirewallAllowlist 校验白名单 → 写库 → 下发到 GCP。
//
// ips 为空 / nil 时视为"恢复全开"：写入空 JSON 数组，并把 GCP 上的入站规则恢复为 0.0.0.0/0、删除 v3 mx-in。
// 用户期望明确"恢复全开"语义时建议直接调 RestoreLegacyFirewall。
func (a *App) UpdateFirewallAllowlist(credID string, ips []string) error {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// 校验 + 规范化 CIDR
	normalized := make([]string, 0, len(ips))
	for _, raw := range ips {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		cidr, err := normalizeCIDR(raw)
		if err != nil {
			return err
		}
		normalized = append(normalized, cidr)
	}

	db, err := requireDB()
	if err != nil {
		return err
	}

	// 写库（UPSERT）
	payload, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("序列化 allowed_ips 失败: %w", err)
	}
	if _, err := db.Exec(`INSERT INTO gcp_firewall_allowlist (cred_id, allowed_ips, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(cred_id) DO UPDATE SET allowed_ips=excluded.allowed_ips, updated_at=CURRENT_TIMESTAMP`,
		credID, string(payload)); err != nil {
		return fmt.Errorf("保存白名单失败: %w", err)
	}

	// 下发到 GCP
	cli, err := loadGCPClient(ctx, db, credID)
	if err != nil {
		return err
	}
	defer cli.Close()
	if err := cli.EnsureMailNodeFirewall(ctx, normalized); err != nil {
		return fmt.Errorf("下发防火墙规则失败: %w", err)
	}
	logger.Info("firewall 白名单已下发到 project=%s, ips=%v", cli.ProjectID(), normalized)
	return nil
}

// RestoreLegacyFirewall 恢复全开：清空白名单 + 把 v2 SourceRanges 恢复 0.0.0.0/0 + 删除 v3 mx-in。
func (a *App) RestoreLegacyFirewall(credID string) error {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := requireDB()
	if err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO gcp_firewall_allowlist (cred_id, allowed_ips, updated_at)
		VALUES (?, '[]', CURRENT_TIMESTAMP)
		ON CONFLICT(cred_id) DO UPDATE SET allowed_ips='[]', updated_at=CURRENT_TIMESTAMP`,
		credID); err != nil {
		return fmt.Errorf("清空白名单失败: %w", err)
	}
	cli, err := loadGCPClient(ctx, db, credID)
	if err != nil {
		return err
	}
	defer cli.Close()
	if err := cli.RestoreLegacyFirewall(ctx); err != nil {
		return fmt.Errorf("恢复全开规则失败: %w", err)
	}
	logger.Info("firewall 已恢复全开 (project=%s)", cli.ProjectID())
	return nil
}
