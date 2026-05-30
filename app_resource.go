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
	InternalIP    string    `json:"internal_ip"`
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
	SlotGroup       string    `json:"slot_group"` // v0.1.57：多 NIC 模式下，同 group 的 IP 必须整组保留或释放
	NICIndex        int       `json:"nic_index"`  // v0.1.57：组内位置（0..nic_count-1）
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
		`SELECT id, gcp_cred_id, gcp_instance_id, name, region, zone, machine_type, status, ip, COALESCE(internal_ip,''), fqdn, root_password, deploy_status, deploy_error, COALESCE(ptr_status,'none'), smtp_account, smtp_password, dkim_public_key, aliyun_cred_id, domain, COALESCE(batch_id,''), COALESCE(deploy_type,'kumomta'), created_at FROM vps_instances ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VPSInstanceDTO{}
	for rows.Next() {
		var v VPSInstanceDTO
		if err := rows.Scan(&v.ID, &v.GCPCredID, &v.GCPInstanceID, &v.Name, &v.Region, &v.Zone, &v.MachineType, &v.Status, &v.IP, &v.InternalIP, &v.FQDN, &v.RootPassword, &v.DeployStatus, &v.DeployError, &v.PTRStatus, &v.SMTPAccount, &v.SMTPPassword, &v.DKIMPublicKey, &v.AliyunCredID, &v.Domain, &v.BatchID, &v.DeployType, &v.CreatedAt); err != nil {
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
		`SELECT id, gcp_cred_id, gcp_address_name, ip, region, status, bound_instance_id, dnsbl_result, dnsbl_hit_lists,
		        COALESCE(batch_id,''), COALESCE(slot_group,''), COALESCE(nic_index,0), created_at
		   FROM static_ips ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StaticIPDTO{}
	for rows.Next() {
		var s StaticIPDTO
		if err := rows.Scan(&s.ID, &s.GCPCredID, &s.GCPAddressName, &s.IP, &s.Region, &s.Status, &s.BoundInstanceID, &s.DNSBLResult, &s.DNSBLHitLists, &s.BatchID, &s.SlotGroup, &s.NICIndex, &s.CreatedAt); err != nil {
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
		if !has {
			newTags := append([]string{}, info.Tags...)
			newTags = append(newTags, gcp.MailNodeTag)
			if err := cli.SetInstanceTags(ctx, zone, name, newTags, info.TagsFingerprint); err != nil {
				logger.Warn("SetTags %s 失败: %v", name, err)
				continue
			}
			logger.Info("VM %s 已补 %s tag", name, gcp.MailNodeTag)
		} else {
			logger.Info("VM %s 已有 %s tag，继续校正防火墙", name, gcp.MailNodeTag)
		}
		if err := cli.EnsureMailNodeFirewall(ctx, "default"); err != nil {
			logger.Warn("校正 firewall 失败 %s: %v", name, err)
			continue
		}
		done++
		logger.Info("✅ VM %s 的 %s tag / firewall 已修复", name, gcp.MailNodeTag)
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
				var credID, name, zone string
				row := db.QueryRow(`SELECT gcp_cred_id, name, zone FROM vps_instances WHERE id=?`, vpsID)
				if err := row.Scan(&credID, &name, &zone); err != nil {
					logger.Warn("查询 VPS %s 失败: %v", vpsID, err)
					return
				}
				type boundIP struct {
					id       string
					region   string
					addrName string
					ip       string
				}
				ipRows, err := db.Query(`SELECT id, region, gcp_address_name, ip FROM static_ips WHERE bound_instance_id=?`, vpsID)
				if err != nil {
					logger.Warn("查询 VPS %s 绑定静态 IP 失败: %v", vpsID, err)
					return
				}
				var boundIPs []boundIP
				for ipRows.Next() {
					var r boundIP
					if err := ipRows.Scan(&r.id, &r.region, &r.addrName, &r.ip); err != nil {
						ipRows.Close()
						logger.Warn("读取 VPS %s 绑定静态 IP 失败: %v", vpsID, err)
						return
					}
					boundIPs = append(boundIPs, r)
				}
				if err := ipRows.Err(); err != nil {
					ipRows.Close()
					logger.Warn("遍历 VPS %s 绑定静态 IP 失败: %v", vpsID, err)
					return
				}
				ipRows.Close()
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
				releaseFailed := false
				for _, r := range boundIPs {
					if r.id == "" || r.addrName == "" || r.region == "" {
						continue
					}
					var releaseErr error
					for attempt := 1; attempt <= 3; attempt++ {
						releaseErr = cli.ReleaseStaticAddress(ctx, r.region, r.addrName)
						if releaseErr == nil || gcp.IsNotFound(releaseErr) {
							break
						}
						if attempt < 3 {
							time.Sleep(time.Duration(attempt*3) * time.Second)
						}
					}
					if releaseErr != nil && !gcp.IsNotFound(releaseErr) {
						logger.Warn("释放 VPS %s 绑定静态 IP %s (%s) 失败: %v", name, r.ip, r.addrName, releaseErr)
						releaseFailed = true
						continue
					}
					if gcp.IsNotFound(releaseErr) {
						logger.Info("IP %s 在云端已不存在，仅清理本地状态", r.addrName)
					}
					_, _ = db.Exec(`UPDATE static_ips SET status='released', bound_instance_id='' WHERE id=?`, r.id)
				}
				_, _ = db.Exec(`UPDATE vps_instances SET status='deleted' WHERE id=?`, vpsID)
				if releaseFailed {
					logger.Warn("VPS %s 已删除，但仍有绑定静态 IP 释放失败，请到 IP 列表或 GCP Console 复查", name)
					return
				}
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
//	"smtp_v3"   → ip:port:user:pass:persona_type:hide_ip:unsub_url:unsub_secret（v0.1.74+，含一键退订）
//	"toolkit"   → CSV with header: domain,smtp_host,smtp_port,account,password,security
//	              （v0.2.5：与 mail-toolkit ExportSmtpCsv 完全一致；可直接合并两边的 CSV）
// csvField 按 RFC4180 转义 toolkit CSV 的单个字段：含 , " CR LF 时整体加双引号并把
// 内部 " 转义成 ""。当前账号/密码为 info@域 + [A-Za-z0-9] 密码、域名为 LDH，本不含特殊
// 字符（输出与旧版逐字节一致，不破坏 mail-toolkit 现有兼容）；此为防御性转义，仅在未来
// 字段含分隔符时生效，避免密码/域名里的逗号把 CSV 列冲乱。
func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\r\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func (a *App) ExportSMTP(format string) (string, error) {
	db, err := requireDB()
	if err != nil {
		return "", err
	}
	// 兼容：一键模式 success；4 阶段 mta_ready（Stage C 完）；ptr_ready（Stage D 完）
	// v0.1.74：加 unsub_secret 字段（缺失时为空字符串）
	rows, err := db.Query(`
        SELECT v.ip, v.fqdn, v.domain, v.smtp_account, v.smtp_password, v.root_password,
               COALESCE(v.hide_client_ip, 1), COALESCE(p.name, ''), COALESCE(v.unsub_secret, '')
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
	// v0.2.5：toolkit 格式与 mail-toolkit ExportSmtpCsv 一致，先打 CSV 表头
	if format == "toolkit" {
		lines = append(lines, "domain,smtp_host,smtp_port,account,password,security")
	}
	for rows.Next() {
		var (
			ip, fqdn, domain, acct, pass, rootPwd, personaName, unsubSecret string
			hideIP                                                          int
		)
		if err := rows.Scan(&ip, &fqdn, &domain, &acct, &pass, &rootPwd, &hideIP, &personaName, &unsubSecret); err != nil {
			return "", err
		}
		// personaName → brutal-mailer 的 persona_type 字面量
		personaType := personaNameToBrutalType(personaName)
		// toolkit 系格式统一用归一化账号 info@根域（从 smtp_account 取 @ 前缀重拼根域，
		// 兼容老数据里的 info@fqdn）。smtp / smtp_v2 / smtp_v3 仍用原始 acct（ip:port:user 结构）。
		_ = rootPwd
		_ = fqdn
		tkAccount := acct
		if at := strings.LastIndex(tkAccount, "@"); at >= 0 {
			tkAccount = tkAccount[:at] + "@" + domain
		} else if tkAccount != "" {
			tkAccount = tkAccount + "@" + domain
		}
		switch format {
		case "toolkit":
			// 与 mail-toolkit ExportSmtpCsv 完全一致：domain,smtp_host,smtp_port,account,password,security
			lines = append(lines, strings.Join([]string{
				csvField(domain), csvField("smtp." + domain), "587",
				csvField(tkAccount), csvField(pass), "STARTTLS",
			}, ","))
		case "toolkit_short":
			// mail-toolkit 简短格式 account----password（同域 smtp.根域；丢 host/port，
			// 发件器靠 From 域自动连 smtp.根域）。即用户用 toolkit 搭多台时导出的那种格式。
			lines = append(lines, tkAccount+"----"+pass)
		case "toolkit_full":
			// mail-toolkit / brutal-mailer 完整 ---- 格式 account----password----host:port----security
			lines = append(lines, fmt.Sprintf("%s----%s----smtp.%s:587----STARTTLS", tkAccount, pass, domain))
		case "smtp_v2":
			lines = append(lines, fmt.Sprintf("%s:587:%s:%s:%s:%d", ip, acct, pass, personaType, hideIP))
		case "smtp_v3":
			// v0.1.74：含一键退订。退订 URL 用根域 https://<域>/u
			unsubURL := ""
			if unsubSecret != "" && domain != "" {
				unsubURL = fmt.Sprintf("https://%s/u", domain)
			}
			lines = append(lines, fmt.Sprintf("%s:587:%s:%s:%s:%d:%s:%s",
				ip, acct, pass, personaType, hideIP, unsubURL, unsubSecret))
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

// OrphanCleanupReport 描述孤立资源清理结果。
type OrphanCleanupReport struct {
	VPSDeleted        int `json:"vps_deleted"`         // 孤立 VPS 记录被删除的条数
	StaticIPsDeleted  int `json:"static_ips_deleted"`  // 孤立静态 IP 记录被删除的条数
	DNSRecordsDeleted int `json:"dns_records_deleted"` // 孤立 DNS 记录被删除的条数
}

// CleanupOrphanResources 清理本地数据库中"对应 GCP 凭证已被删除"的孤立资源记录。
//
// 仅清理本地 SQLite 表，不会调用任何云端 API（你需要自行确保 GCP 上对应资源已被释放）。
// 适用场景：删除 GCP 凭证后，旧的 VPS / 静态 IP / DNS 记录在资源页继续显示但无法操作。
func (a *App) CleanupOrphanResources() (OrphanCleanupReport, error) {
	var report OrphanCleanupReport
	db, err := requireDB()
	if err != nil {
		return report, err
	}

	// 1. 孤立 VPS：gcp_cred_id 不在 gcp_credentials 表里
	res, err := db.Exec(`DELETE FROM vps_instances
		WHERE gcp_cred_id NOT IN (SELECT id FROM gcp_credentials)`)
	if err != nil {
		return report, fmt.Errorf("清理孤立 VPS 失败: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		report.VPSDeleted = int(n)
	}

	// 2. 孤立静态 IP
	res, err = db.Exec(`DELETE FROM static_ips
		WHERE gcp_cred_id NOT IN (SELECT id FROM gcp_credentials)`)
	if err != nil {
		return report, fmt.Errorf("清理孤立静态 IP 失败: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		report.StaticIPsDeleted = int(n)
	}

	// 3. 孤立 DNS 记录：related_instance_id 非空但 VPS 已不存在
	res, err = db.Exec(`DELETE FROM dns_records
		WHERE related_instance_id != ''
		  AND related_instance_id NOT IN (SELECT id FROM vps_instances)`)
	if err != nil {
		return report, fmt.Errorf("清理孤立 DNS 记录失败: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		report.DNSRecordsDeleted = int(n)
	}

	logger.Info("孤立资源清理完成: VPS=%d, IPs=%d, DNS=%d",
		report.VPSDeleted, report.StaticIPsDeleted, report.DNSRecordsDeleted)
	return report, nil
}
