package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gcp-mailnode/internal/crypto"
	"gcp-mailnode/internal/dns"
	"gcp-mailnode/internal/gcp"
	"gcp-mailnode/internal/logger"
	"gcp-mailnode/internal/store"
)

// VPSInstanceDTO
type VPSInstanceDTO struct {
	ID            string    `json:"id"`
	GCPCredID     string    `json:"gcp_cred_id"`
	GCPInstanceID string    `json:"gcp_instance_id"`
	Name          string    `json:"name"`
	Region        string    `json:"region"`
	Zone          string    `json:"zone"`
	MachineType   string    `json:"machine_type"`
	Status        string    `json:"status"`
	IP            string    `json:"ip"`
	FQDN          string    `json:"fqdn"`
	RootPassword  string    `json:"root_password"`
	DeployStatus  string    `json:"deploy_status"`
	DeployError   string    `json:"deploy_error"`
	PTRStatus     string    `json:"ptr_status"`
	SMTPAccount   string    `json:"smtp_account"`
	SMTPPassword  string    `json:"smtp_password"`
	DKIMPublicKey string    `json:"dkim_public_key"`
	AliyunCredID  string    `json:"aliyun_cred_id"`
	Domain        string    `json:"domain"`
	BatchID       string    `json:"batch_id"`
	DeployType    string    `json:"deploy_type"`
	CreatedAt     time.Time `json:"created_at"`
}

// StaticIPDTO
type StaticIPDTO struct {
	ID              string    `json:"id"`
	GCPCredID       string    `json:"gcp_cred_id"`
	GCPAddressName  string    `json:"gcp_address_name"`
	IP              string    `json:"ip"`
	Region          string    `json:"region"`
	Status          string    `json:"status"`
	BoundInstanceID string    `json:"bound_instance_id"`
	DNSBLResult     string    `json:"dnsbl_result"`
	DNSBLHitLists   string    `json:"dnsbl_hit_lists"`
	BatchID         string    `json:"batch_id"`
	CreatedAt       time.Time `json:"created_at"`
}

// DNSRecordDTO
type DNSRecordDTO struct {
	ID                string    `json:"id"`
	AliyunCredID      string    `json:"aliyun_cred_id"`
	Domain            string    `json:"domain"`
	RR                string    `json:"rr"`
	RecordType        string    `json:"record_type"`
	Value             string    `json:"value"`
	AliyunRecordID    string    `json:"aliyun_record_id"`
	RelatedInstanceID string    `json:"related_instance_id"`
	CreatedAt         time.Time `json:"created_at"`
}

// ListVPS 列出 VPS
func (a *App) ListVPS() ([]VPSInstanceDTO, error) {
	db, err := requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT id, gcp_cred_id, gcp_instance_id, name, region, zone, machine_type, status, ip, fqdn, root_password, deploy_status, deploy_error, COALESCE(ptr_status,'none'), smtp_account, smtp_password, dkim_public_key, aliyun_cred_id, domain, COALESCE(batch_id,''), COALESCE(deploy_type,'kumomta'), created_at FROM vps_instances ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VPSInstanceDTO{}
	for rows.Next() {
		var v VPSInstanceDTO
		if err := rows.Scan(&v.ID, &v.GCPCredID, &v.GCPInstanceID, &v.Name, &v.Region, &v.Zone, &v.MachineType, &v.Status, &v.IP, &v.FQDN, &v.RootPassword, &v.DeployStatus, &v.DeployError, &v.PTRStatus, &v.SMTPAccount, &v.SMTPPassword, &v.DKIMPublicKey, &v.AliyunCredID, &v.Domain, &v.BatchID, &v.DeployType, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListStaticIPs 列出所有静态 IP
func (a *App) ListStaticIPs() ([]StaticIPDTO, error) {
	db, err := requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT id, gcp_cred_id, gcp_address_name, ip, region, status, bound_instance_id, dnsbl_result, dnsbl_hit_lists, COALESCE(batch_id,''), created_at FROM static_ips ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StaticIPDTO{}
	for rows.Next() {
		var s StaticIPDTO
		if err := rows.Scan(&s.ID, &s.GCPCredID, &s.GCPAddressName, &s.IP, &s.Region, &s.Status, &s.BoundInstanceID, &s.DNSBLResult, &s.DNSBLHitLists, &s.BatchID, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListDNSRecords 列出 DNS 记录
func (a *App) ListDNSRecords() ([]DNSRecordDTO, error) {
	db, err := requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT id, aliyun_cred_id, domain, rr, record_type, value, aliyun_record_id, related_instance_id, created_at FROM dns_records ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DNSRecordDTO{}
	for rows.Next() {
		var d DNSRecordDTO
		if err := rows.Scan(&d.ID, &d.AliyunCredID, &d.Domain, &d.RR, &d.RecordType, &d.Value, &d.AliyunRecordID, &d.RelatedInstanceID, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// FixMailNodeTag 给指定 VPS 补打 mail-node tag（用于老机器或 Stage B 自动补失败的机器）
// 返回处理成功的台数。
func (a *App) FixMailNodeTag(vpsIDs []string) (int, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if len(vpsIDs) == 0 {
		return 0, fmt.Errorf("至少选择一台 VPS")
	}
	db, err := requireDB()
	if err != nil {
		return 0, err
	}
	clients := map[string]*gcp.Client{}
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()
	getCli := func(credID string) (*gcp.Client, error) {
		if c, ok := clients[credID]; ok {
			return c, nil
		}
		c, err := loadGCPClientForApp(ctx, credID)
		if err != nil {
			return nil, err
		}
		clients[credID] = c
		return c, nil
	}
	done := 0
	for _, id := range vpsIDs {
		var credID, zone, name string
		row := db.QueryRowContext(ctx, `SELECT gcp_cred_id, zone, name FROM vps_instances WHERE id=?`, id)
		if err := row.Scan(&credID, &zone, &name); err != nil {
			logger.Warn("读取 VPS %s 失败: %v", id, err)
			continue
		}
		cli, err := getCli(credID)
		if err != nil {
			logger.Warn("加载 GCP client 失败 %s: %v", credID, err)
			continue
		}
		info, err := cli.GetInstance(ctx, zone, name)
		if err != nil {
			logger.Warn("Get %s 失败: %v", name, err)
			continue
		}
		has := false
		for _, t := range info.Tags {
			if t == gcp.MailNodeTag {
				has = true
				break
			}
		}
		if has {
			logger.Info("VM %s 已有 %s tag，跳过", name, gcp.MailNodeTag)
			done++
			continue
		}
		newTags := append([]string{}, info.Tags...)
		newTags = append(newTags, gcp.MailNodeTag)
		if err := cli.SetInstanceTags(ctx, zone, name, newTags, info.TagsFingerprint); err != nil {
			logger.Warn("SetTags %s 失败: %v", name, err)
			continue
		}
		// 确保 project 里有 firewall 规则（幂等）；按用户配置应用白名单（空则维持全开）
		allowlist, _ := loadAllowlist(db, credID)
		_ = cli.EnsureMailNodeFirewall(ctx, allowlist)
		done++
		logger.Info("✅ VM %s 已补 %s tag", name, gcp.MailNodeTag)
	}
	return done, nil
}

// BatchDelete 批量删除资源
// resourceType: "vps" | "ip" | "dns"
func (a *App) BatchDelete(resourceType string, ids []string) (int, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := requireDB()
	if err != nil {
		return 0, err
	}
	success := 0
	switch resourceType {
	case "vps":
		// 缓存 gcp client（并发访问需要 mutex）
		clients := map[string]*gcp.Client{}
		var cliMu sync.Mutex
		defer func() {
			for _, c := range clients {
				_ = c.Close()
			}
		}()
		getCli := func(credID string) (*gcp.Client, error) {
			cliMu.Lock()
			defer cliMu.Unlock()
			if c, ok := clients[credID]; ok {
				return c, nil
			}
			c, err := loadGCPClientForApp(ctx, credID)
			if err != nil {
				return nil, err
			}
			clients[credID] = c
			return c, nil
		}
		// 并发删除：每台独立 goroutine。串行每台 30-60s，6 台要 5-10min；并发 30-60s 搞定。
		var wg sync.WaitGroup
		var okCount int32
		for _, id := range ids {
			wg.Add(1)
			vpsID := id
			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						logger.Warn("删除 VPS %s panic: %v", vpsID, r)
					}
				}()
				var credID, name, zone, region, addrName, staticIPID string
				row := db.QueryRow(`SELECT v.gcp_cred_id, v.name, v.zone, COALESCE(s.region,''), COALESCE(s.gcp_address_name,''), COALESCE(s.id,'')
					FROM vps_instances v
					LEFT JOIN static_ips s ON s.bound_instance_id=v.id
					WHERE v.id=?`, vpsID)
				if err := row.Scan(&credID, &name, &zone, &region, &addrName, &staticIPID); err != nil {
					logger.Warn("查询 VPS %s 失败: %v", vpsID, err)
					return
				}
				cli, err := getCli(credID)
				if err != nil {
					logger.Warn("加载 GCP 客户端失败 %s: %v", credID, err)
					return
				}
				if err := cli.DeleteInstance(ctx, zone, name); err != nil {
					if !gcp.IsNotFound(err) {
						logger.Warn("删除 VM %s 失败: %v", name, err)
						return
					}
					logger.Info("VM %s 在云端已不存在，仅清理本地状态", name)
				}
				if staticIPID != "" && addrName != "" && region != "" {
					if err := cli.ReleaseStaticAddress(ctx, region, addrName); err != nil {
						if !gcp.IsNotFound(err) {
							logger.Warn("释放 VPS 绑定静态 IP %s 失败: %v", addrName, err)
							return
						}
						logger.Info("IP %s 在云端已不存在，仅清理本地状态", addrName)
					}
					_, _ = db.Exec(`UPDATE static_ips SET status='released', bound_instance_id='' WHERE id=?`, staticIPID)
				}
				_, _ = db.Exec(`UPDATE vps_instances SET status='deleted' WHERE id=?`, vpsID)
				atomic.AddInt32(&okCount, 1)
			}()
		}
		wg.Wait()
		success = int(okCount)
	case "ip":
		clients := map[string]*gcp.Client{}
		var cliMuIP sync.Mutex
		defer func() {
			for _, c := range clients {
				_ = c.Close()
			}
		}()
		getCli := func(credID string) (*gcp.Client, error) {
			cliMuIP.Lock()
			defer cliMuIP.Unlock()
			if c, ok := clients[credID]; ok {
				return c, nil
			}
			c, err := loadGCPClientForApp(ctx, credID)
			if err != nil {
				return nil, err
			}
			clients[credID] = c
			return c, nil
		}
		// 并发释放（每个 IP 独立 goroutine）
		var wgIP sync.WaitGroup
		var okIP int32
		for _, id := range ids {
			wgIP.Add(1)
			ipID := id
			go func() {
				defer wgIP.Done()
				defer func() {
					if r := recover(); r != nil {
						logger.Warn("释放 IP %s panic: %v", ipID, r)
					}
				}()
				var credID, region, addrName string
				row := db.QueryRow(`SELECT gcp_cred_id, region, gcp_address_name FROM static_ips WHERE id=?`, ipID)
				if err := row.Scan(&credID, &region, &addrName); err != nil {
					logger.Warn("查询 IP %s 失败: %v", ipID, err)
					return
				}
				cli, err := getCli(credID)
				if err != nil {
					logger.Warn("加载 GCP 客户端失败: %v", err)
					return
				}
				if err := cli.ReleaseStaticAddress(ctx, region, addrName); err != nil {
					if !gcp.IsNotFound(err) {
						logger.Warn("释放 IP %s 失败: %v", addrName, err)
						return
					}
					logger.Info("IP %s 在云端已不存在，仅清理本地状态", addrName)
				}
				_, _ = db.Exec(`UPDATE static_ips SET status='released', bound_instance_id='' WHERE id=?`, ipID)
				atomic.AddInt32(&okIP, 1)
			}()
		}
		wgIP.Wait()
		success = int(okIP)
	case "dns":
		// 按 aliyun_cred_id 聚合
		aliClients := map[string]*dns.AliyunDns{}
		getAli := func(credID string) (*dns.AliyunDns, error) {
			if c, ok := aliClients[credID]; ok {
				return c, nil
			}
			c, err := loadAliyunForApp(credID)
			if err != nil {
				return nil, err
			}
			aliClients[credID] = c
			return c, nil
		}
		for _, id := range ids {
			var credID, recordID string
			row := db.QueryRow(`SELECT aliyun_cred_id, aliyun_record_id FROM dns_records WHERE id=?`, id)
			if err := row.Scan(&credID, &recordID); err != nil {
				logger.Warn("查询 DNS %s 失败: %v", id, err)
				continue
			}
			if recordID != "" {
				cli, err := getAli(credID)
				if err != nil {
					logger.Warn("加载阿里云客户端失败: %v", err)
					continue
				}
				if _, err := cli.DeleteRecord(recordID); err != nil {
					logger.Warn("删除 DNS 记录 %s 失败: %v", recordID, err)
					// 即使远端失败也删本地
				}
			}
			_, _ = db.Exec(`DELETE FROM dns_records WHERE id=?`, id)
			success++
		}
	default:
		return 0, fmt.Errorf("未知 resourceType: %s", resourceType)
	}
	return success, nil
}

// ExportSMTP 导出 SMTP 账户
// format:
//
//	"smtp"      → ip:port:user:pass（兼容老 brutal-mailer）
//	"smtp_v2"   → ip:port:user:pass:persona_type:hide_ip（brutal-mailer v2.3.37+，可直接按账号绑定 persona）
//	"toolkit"   → fqdn----ip----root----rootpass（mail-toolkit 格式）
func (a *App) ExportSMTP(format string) (string, error) {
	db, err := requireDB()
	if err != nil {
		return "", err
	}
	// 兼容：一键模式 success；4 阶段 mta_ready（Stage C 完）；ptr_ready（Stage D 完）
	rows, err := db.Query(`
        SELECT v.ip, v.fqdn, v.smtp_account, v.smtp_password, v.root_password,
               COALESCE(v.hide_client_ip, 1), COALESCE(p.name, '')
        FROM vps_instances v
        LEFT JOIN personas p ON p.id = v.persona_id
        WHERE v.deploy_status IN ('success', 'mta_ready', 'ptr_ready')
          AND v.status != 'deleted'
          AND v.smtp_account != ''`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var (
			ip, fqdn, acct, pass, rootPwd, personaName string
			hideIP                                     int
		)
		if err := rows.Scan(&ip, &fqdn, &acct, &pass, &rootPwd, &hideIP, &personaName); err != nil {
			return "", err
		}
		// personaName → brutal-mailer 的 persona_type 字面量
		personaType := personaNameToBrutalType(personaName)
		switch format {
		case "toolkit":
			lines = append(lines, fmt.Sprintf("%s----%s----root----%s", fqdn, ip, rootPwd))
		case "smtp_v2":
			lines = append(lines, fmt.Sprintf("%s:587:%s:%s:%s:%d", ip, acct, pass, personaType, hideIP))
		default: // smtp (老格式)
			lines = append(lines, fmt.Sprintf("%s:587:%s:%s", ip, acct, pass))
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

// personaNameToBrutalType 把 gcp-mailnode 里的 persona 名字映射到 brutal-mailer 的 persona_type 字面量。
// brutal-mailer 现有的 6 个类型：outlook / yahoo_jp / gmail / generic / smbc_vpass / icloud_iphone
// gcp-mailnode 的 8 个预设：Gmail 网页用户 / iPhone Mail 用户 / Outlook 桌面用户 / Thunderbird 桌面用户 /
//
//	yahoo.co.jp 用户 / NTT docomo 手机 / au by KDDI 手机 / SoftBank 手机
//
// 阶段 3 会在 brutal-mailer 侧补齐缺失的 persona 类型（thunderbird/docomo/au/softbank）。
func personaNameToBrutalType(name string) string {
	switch name {
	case "Gmail 网页用户":
		return "gmail"
	case "iPhone Mail 用户":
		return "icloud_iphone"
	case "Outlook 桌面用户":
		return "outlook"
	case "Thunderbird 桌面用户":
		return "thunderbird"
	case "yahoo.co.jp 用户":
		return "yahoo_jp"
	case "NTT docomo 手机":
		return "docomo"
	case "au by KDDI 手机":
		return "au_kddi"
	case "SoftBank 手机":
		return "softbank"
	}
	return "" // 未绑定 persona 或自定义 persona，brutal 侧回落到全局配置
}

// ExportPersonasJSON 把本机 personas 表全量导出为 JSON（供 brutal-mailer 批量导入）。
// 只导出预设（is_preset=1），让 brutal 那边知道"gcp-mailnode 部署机器时用的是哪几个 persona"。
func (a *App) ExportPersonasJSON() (string, error) {
	list, err := a.ListPersonas()
	if err != nil {
		return "", err
	}
	exportList := make([]map[string]interface{}, 0, len(list))
	for _, p := range list {
		if !p.IsPreset {
			continue
		}
		exportList = append(exportList, map[string]interface{}{
			"name":              p.Name,
			"brutal_type":       personaNameToBrutalType(p.Name),
			"description":       p.Description,
			"received_template": p.ReceivedTemplate,
			"user_agent":        p.UserAgent,
			"x_mailer":          p.XMailer,
			"extra_headers":     p.ExtraHeaders,
		})
	}
	b, err := json.MarshalIndent(exportList, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ---- 内部辅助 ----

func loadGCPClientForApp(ctx context.Context, credID string) (*gcp.Client, error) {
	db := store.DB()
	if db == nil {
		return nil, fmt.Errorf("数据库未就绪")
	}
	var (
		name, authType, projectID string
		encBlob                   []byte
	)
	row := db.QueryRowContext(ctx, `SELECT name, auth_type, project_id, encrypted_blob FROM gcp_credentials WHERE id=?`, credID)
	if err := row.Scan(&name, &authType, &projectID, &encBlob); err != nil {
		return nil, err
	}
	var blob []byte
	if len(encBlob) > 0 {
		dec, err := crypto.Decrypt(encBlob)
		if err != nil {
			return nil, err
		}
		blob = dec
	}
	cred := gcp.Credential{ID: credID, Name: name, AuthType: gcp.AuthType(authType), ProjectID: projectID, Blob: blob}
	return gcp.NewClient(ctx, cred)
}

func loadAliyunForApp(credID string) (*dns.AliyunDns, error) {
	db := store.DB()
	if db == nil {
		return nil, fmt.Errorf("数据库未就绪")
	}
	var (
		ak     string
		encSec []byte
	)
	row := db.QueryRow(`SELECT access_key_id, encrypted_secret FROM aliyun_credentials WHERE id=?`, credID)
	if err := row.Scan(&ak, &encSec); err != nil {
		return nil, err
	}
	sk, err := crypto.Decrypt(encSec)
	if err != nil {
		return nil, err
	}
	return dns.NewAliyunDns(ak, string(sk)), nil
}
