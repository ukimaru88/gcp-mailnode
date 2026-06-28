package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"gcp-mailnode/internal/export"
	"gcp-mailnode/internal/logger"
	"gcp-mailnode/internal/parser"
	"gcp-mailnode/internal/ssh"
	"gcp-mailnode/internal/sshkey"
)

// ExtractResult 单台 VPS 的提取结果
type ExtractResult struct {
	VPSID  string `json:"vps_id"`
	Name   string `json:"name"`   // VPS 名（用于前端显示）
	IP     string `json:"ip"`
	Lines  int    `json:"lines"`  // 拉取的日志行数（KumoMTA JSON 行）
	Parsed int    `json:"parsed"` // 解析出来的投递事件数（Reception+Delivery+Bounce+TransientFailure）
	Emails int    `json:"emails"` // 唯一邮箱数（本台 VPS 内去重后）
	Error  string `json:"error,omitempty"`
}

// WriteSummary 写文件结果（手动展开 export.WriteResult，避免 wails TS binding
// 把 "export" 作为 namespace 名 —— 在 TypeScript 里 export 是保留字会报语法错）
type WriteSummary struct {
	TotalEmails   int      `json:"total_emails"`
	NewEmails     int      `json:"new_emails"`
	DuplicateSkip int      `json:"duplicate_skip"`
	FilesCreated  []string `json:"files_created"`
}

// ExtractSummary 整体汇总
type ExtractSummary struct {
	BatchID     string          `json:"batch_id"`
	Results     []ExtractResult `json:"results"`
	OutputDir   string          `json:"output_dir"`
	TotalEmails int             `json:"total_emails"` // 跨 VPS 合并去重后唯一邮箱数
	WriteResult WriteSummary    `json:"write_result"` // 写文件细节
}

// GetExtractOutputDir 返回输出目录绝对路径（与 toolkit 一致）。
// 不存在则尝试创建——前端"打开输出目录"前调一次即可。
func (a *App) GetExtractOutputDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取 home 目录失败: %w", err)
	}
	dir := filepath.Join(home, "Desktop", "邮箱提取结果")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建输出目录失败: %w", err)
	}
	return dir, nil
}

// ExtractFromVPS 对指定 VPS 列表并发跑 KumoMTA 日志提取。
//
// 每台 VPS：SSH 连接 → ReadKumoMTALogs 拉日志 → ParseKumoMTAStream 提邮箱
// 全部 VPS 完成后：跨 VPS 合并去重 → WriteCategorized 按域名分类写 txt
// 输出到 ~/Desktop/邮箱提取结果/。
//
// 语义：parser 只把 'Delivery' 事件（成功投递到远端 MX 的 250 OK）的 Recipient 加入导出，
// 'Reception' / 'Bounce' / 'TransientFailure' 不导出。所以输出文件里全是发送成功的邮箱。
//
// 仅支持 deploy_type='kumomta' 的 VPS；mailcow 节点会在 Result.Error 里标注跳过。
//
// v0.1.77：本入口默认不删服务器日志（向后兼容旧调用方）；
// 自动调度走 ExtractFromVPSWithDelete（deleteAfter=true）。
func (a *App) ExtractFromVPS(vpsIDs []string) (ExtractSummary, error) {
	return a.extractCore(vpsIDs, false, "")
}

// ExtractFromVPSWithDelete 同 ExtractFromVPS，但 deleteAfter=true 时提取成功后调用 DeleteKumoMTALogsBefore
// 删除服务器上 ≤ cursor 的所有日志（包括成功+失败的，已读到本地的就不再保留）。
// 安全：只在写本地文件成功后才删；当台 VPS SSH 失败 / 解析失败 / 写文件失败时不删，下次还能重读。
func (a *App) ExtractFromVPSWithDelete(vpsIDs []string, deleteAfter bool) (ExtractSummary, error) {
	return a.extractCore(vpsIDs, deleteAfter, "")
}

// ExtractFromVPSForceType v0.2.37：手动指定 deploy_type 覆盖数据库字段。
// 用于老节点（v0.2.10 之前部署）数据库 deploy_type 字段不正确的场景。
// forceType: "" = 用 DB 字段；"kumomta" / "postfix" = 强制覆盖。
func (a *App) ExtractFromVPSForceType(vpsIDs []string, deleteAfter bool, forceType string) (ExtractSummary, error) {
	ft := strings.ToLower(strings.TrimSpace(forceType))
	if ft != "" && ft != "kumomta" && ft != "postfix" {
		return ExtractSummary{}, fmt.Errorf("forceType 只能是 'kumomta' / 'postfix' / 空，收到: %s", forceType)
	}
	return a.extractCore(vpsIDs, deleteAfter, ft)
}

func (a *App) extractCore(vpsIDs []string, deleteAfter bool, forceType string) (ExtractSummary, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if len(vpsIDs) == 0 {
		return ExtractSummary{}, fmt.Errorf("至少选择一台 VPS")
	}

	db, err := requireDB()
	if err != nil {
		return ExtractSummary{}, err
	}

	// 查 VPS 详情（IP + name + deploy_type）
	type vpsRow struct {
		id, name, ip, deployType string
	}
	rows := make([]vpsRow, 0, len(vpsIDs))
	for _, id := range vpsIDs {
		var v vpsRow
		v.id = id
		row := db.QueryRowContext(ctx,
			`SELECT name, ip, COALESCE(deploy_type,'kumomta') FROM vps_instances WHERE id=?`, id)
		if err := row.Scan(&v.name, &v.ip, &v.deployType); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return ExtractSummary{}, fmt.Errorf("查询 VPS %s 失败: %w", id, err)
		}
		rows = append(rows, v)
	}
	if len(rows) == 0 {
		return ExtractSummary{}, fmt.Errorf("未找到任何匹配的 VPS")
	}

	batchID := "b-" + uuid.NewString()[:12]
	logger.Info("[extract %s] 开始：%d 台 VPS", batchID, len(rows))

	// 并发提取（最多 5）
	const concurrency = 5
	sem := make(chan struct{}, concurrency)
	results := make([]ExtractResult, len(rows))
	allEmails := make([]string, 0, 4096)
	// 提取成功的 (vpsID, sshCfg, cursor) 列表，写本地文件成功后用来批量删服务器日志
	type cleanupEntry struct {
		vpsID, name string
		cfg         ssh.Config
		cursor      string
	}
	cleanupList := make([]cleanupEntry, 0, len(rows))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i, v := range rows {
		i, v := i, v
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			res := ExtractResult{VPSID: v.id, Name: v.name, IP: v.ip}

			// v0.2.37：forceType 强制覆盖（用于老节点 DB deploy_type 字段不对）
			effType := v.deployType
			if forceType != "" {
				effType = forceType
			}

			if effType == "mailcow" {
				res.Error = "mailcow 节点暂不支持提取（需另行实现）"
				results[i] = res
				return
			}
			if strings.TrimSpace(v.ip) == "" {
				res.Error = "VPS 无 IP"
				results[i] = res
				return
			}

			cfg := ssh.Config{
				Host:       v.ip,
				Port:       22,
				Username:   "root",
				KeyContent: string(sshkey.PrivatePEM()),
			}

			// v0.2.35：按 deploy_type 分支——Postfix 走 syslog，KumoMTA 走 zstd JSON
			// v0.2.37：effType 来自 forceType 覆盖或 DB 字段
			var content, cursor string
			var err error
			if effType == "postfix" {
				// Postfix 日志在 /var/log/mail.log（syslog 文本格式）
				// v0.2.36：journalctl 兜底必须用 -t SYSLOG_IDENTIFIER 拿 postfix 子进程
				// 之前 v0.2.35 用 `-u postfix` 错——Postfix 不是 systemd unit 直接输出
				// 而是 master + smtp + smtpd + cleanup + qmgr + bounce 等子进程
				// 各自通过 syslog 写日志，SYSLOG_IDENTIFIER 是 'postfix/smtp' 这种
				// status=sent 来自 postfix/smtp，status=bounced 来自 postfix/bounce
				content, err = ssh.RunCommand(ctx, cfg, `set +e
if [ -s /var/log/mail.log ]; then
  cat /var/log/mail.log
  for f in /var/log/mail.log.1 /var/log/mail.log.2; do
    [ -f "$f" ] && cat "$f"
  done
  for f in /var/log/mail.log.*.gz; do
    [ -f "$f" ] && zcat "$f" 2>/dev/null
  done
else
  journalctl --no-pager --since '7 days ago' \
    -t postfix/smtp -t postfix/smtpd -t postfix/cleanup \
    -t postfix/bounce -t postfix/error -t postfix/local \
    -t postfix/qmgr -t postfix/pickup \
    2>/dev/null
fi`)
				if err == nil {
					res.Lines = strings.Count(content, "\n")
					pr := parser.ParseMailLog(content)
					res.Parsed = pr.SentLines + pr.BouncedLines + pr.DeferredLines
					res.Emails = len(pr.Emails)
					mu.Lock()
					allEmails = append(allEmails, pr.Emails...)
					mu.Unlock()
				}
				// Postfix 暂不支持 deleteAfter（syslog 不能按 cursor 删，需 logrotate 配合）
			} else {
				// KumoMTA 走原路径
				content, cursor, err = ssh.ReadKumoMTALogs(ctx, cfg, "/var/log/kumomta/", "")
				if err == nil {
					res.Lines = strings.Count(content, "\n")
					pr := parser.ParseKumoMTAStream(strings.NewReader(content))
					res.Parsed = pr.SentLines + pr.BouncedLines + pr.DeferredLines
					res.Emails = len(pr.Emails)
					mu.Lock()
					allEmails = append(allEmails, pr.Emails...)
					if deleteAfter && cursor != "" {
						cleanupList = append(cleanupList, cleanupEntry{vpsID: v.id, name: v.name, cfg: cfg, cursor: cursor})
					}
					mu.Unlock()
				}
			}
			if err != nil {
				res.Error = err.Error()
				results[i] = res
				logger.Warn("[extract %s] %s SSH 拉日志失败: %v", batchID, v.name, err)
				return
			}

			results[i] = res
			logger.Info("[extract %s] %s type=%s(eff=%s) lines=%d parsed=%d emails=%d cursor=%s",
				batchID, v.name, v.deployType, effType, res.Lines, res.Parsed, res.Emails, cursor)
		}()
	}
	wg.Wait()

	// 跨 VPS 去重并写文件
	outputDir, err := a.GetExtractOutputDir()
	if err != nil {
		return ExtractSummary{}, err
	}
	uniq := dedupEmails(allEmails)
	summary := ExtractSummary{
		BatchID:     batchID,
		Results:     results,
		OutputDir:   outputDir,
		TotalEmails: len(uniq),
	}
	writeOK := true
	if len(uniq) > 0 {
		w := &export.Writer{OutputDir: outputDir, LinesPerFile: 50000}
		wr, werr := w.WriteCategorized(uniq)
		if werr != nil {
			writeOK = false
			return summary, fmt.Errorf("写文件失败: %w", werr)
		}
		summary.WriteResult = WriteSummary{
			TotalEmails:   wr.TotalEmails,
			NewEmails:     wr.NewEmails,
			DuplicateSkip: wr.DuplicateSkip,
			FilesCreated:  wr.FilesCreated,
		}
	}
	logger.Info("[extract %s] 完成：跨 VPS 唯一邮箱 %d → %s", batchID, len(uniq), outputDir)

	// v0.1.77：deleteAfter=true 且写文件成功后，批量删除服务器上 ≤ cursor 的日志（成功+失败一起删）
	// 安全：只删提取成功的 VPS（在 cleanupList 里）；任何 VPS 失败时不影响其他成功的删除
	if deleteAfter && writeOK && len(cleanupList) > 0 {
		var delWG sync.WaitGroup
		delSem := make(chan struct{}, concurrency)
		for _, ent := range cleanupList {
			ent := ent
			delWG.Add(1)
			delSem <- struct{}{}
			go func() {
				defer delWG.Done()
				defer func() { <-delSem }()
				n, err := ssh.DeleteKumoMTALogsBefore(ctx, ent.cfg, "/var/log/kumomta/", ent.cursor)
				if err != nil {
					logger.Warn("[extract %s] %s 删服务器日志失败（不影响下次提取，最多多读一次）: %v", batchID, ent.name, err)
				} else {
					logger.Info("[extract %s] %s 已删服务器日志 %d 个文件（≤ %s）", batchID, ent.name, n, ent.cursor)
				}
			}()
		}
		delWG.Wait()
	}
	return summary, nil
}

// dedupEmails 全小写 + 去重，保持首次出现顺序
func dedupEmails(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	return out
}

// 占位避免未使用 time 时 Go 报错（如果未来加超时控制）
var _ = time.Second

// ============================================================================
// v0.1.77：自动提取调度器
// ============================================================================

// ExtractScheduleConfig 自动提取配置（前后端共享 DTO）
type ExtractScheduleConfig struct {
	Enabled       bool   `json:"enabled"`         // 总开关
	IntervalMin   int    `json:"interval_min"`    // 间隔分钟（最小 1，建议 ≥5 避免过频 SSH）
	DeleteAfter   bool   `json:"delete_after"`    // 提取完是否删服务器日志（默认 true，与需求一致）
	LastRunAt     string `json:"last_run_at"`     // 上次跑的时间（ISO8601）
	LastRunStatus string `json:"last_run_status"` // ok / error
	LastRunMsg    string `json:"last_run_msg"`    // 上次结果摘要
	NextRunAt     string `json:"next_run_at"`     // 下次预定时间
}

const (
	settingExtractSchedule = "extract_schedule_config"
)

var (
	scheduleMu      sync.Mutex
	scheduleStarted bool
)

// GetExtractSchedule 返回当前自动提取配置（settings 表 key=extract_schedule_config）
func (a *App) GetExtractSchedule() (ExtractScheduleConfig, error) {
	cfg := ExtractScheduleConfig{Enabled: false, IntervalMin: 15, DeleteAfter: true}
	db, err := requireDB()
	if err != nil {
		return cfg, err
	}
	var raw string
	row := db.QueryRow(`SELECT value FROM settings WHERE key=?`, settingExtractSchedule)
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return cfg, nil // 默认值
		}
		return cfg, err
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		logger.Warn("解析 extract_schedule_config 失败，回默认值: %v", err)
		return ExtractScheduleConfig{Enabled: false, IntervalMin: 15, DeleteAfter: true}, nil
	}
	if cfg.IntervalMin < 1 {
		cfg.IntervalMin = 15
	}
	return cfg, nil
}

// SetExtractSchedule 写入自动提取配置（前端开关 / 修改间隔 时调用）
func (a *App) SetExtractSchedule(cfg ExtractScheduleConfig) error {
	if cfg.IntervalMin < 1 {
		cfg.IntervalMin = 15
	}
	if cfg.IntervalMin > 1440 {
		cfg.IntervalMin = 1440 // 一天最多间隔
	}
	db, err := requireDB()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO settings (key, value) VALUES (?,?)
	                  ON CONFLICT(key) DO UPDATE SET value=excluded.value`, settingExtractSchedule, string(raw))
	if err != nil {
		return err
	}
	logger.Info("自动提取配置已更新: enabled=%v interval=%dmin deleteAfter=%v", cfg.Enabled, cfg.IntervalMin, cfg.DeleteAfter)
	return nil
}

// startExtractScheduler 启动后台调度 goroutine（App.startup 调用一次）
// 每 30 秒 tick 一次，检查是否到点；到点则跑全部 KumoMTA VPS 的提取（deleteAfter=配置值）
func (a *App) startExtractScheduler() {
	scheduleMu.Lock()
	if scheduleStarted {
		scheduleMu.Unlock()
		return
	}
	scheduleStarted = true
	scheduleMu.Unlock()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		logger.Info("自动提取调度器启动（30s tick，按 settings.extract_schedule_config 决定是否执行）")
		for {
			select {
			case <-a.ctx.Done():
				logger.Info("自动提取调度器退出（ctx done）")
				return
			case <-ticker.C:
				a.tickExtractSchedule()
			}
		}
	}()
}

// tickExtractSchedule 每 tick 检查一次是否到点
func (a *App) tickExtractSchedule() {
	cfg, err := a.GetExtractSchedule()
	if err != nil || !cfg.Enabled {
		return
	}
	// 计算"上次运行 + interval"是否 ≤ 现在
	now := time.Now()
	if cfg.LastRunAt != "" {
		if last, err := time.Parse(time.RFC3339, cfg.LastRunAt); err == nil {
			if now.Sub(last) < time.Duration(cfg.IntervalMin)*time.Minute {
				return // 还没到下次时间
			}
		}
	}
	// 到点：跑提取
	a.runScheduledExtract(cfg)
}

// runScheduledExtract 执行一次自动提取（拉所有 KumoMTA VPS）
func (a *App) runScheduledExtract(cfg ExtractScheduleConfig) {
	startedAt := time.Now()
	logger.Info("自动提取触发：interval=%dmin deleteAfter=%v", cfg.IntervalMin, cfg.DeleteAfter)

	// 标记本次开始（即使失败也写，否则失败时会狂跑）
	cfg.LastRunAt = startedAt.UTC().Format(time.RFC3339)
	cfg.NextRunAt = startedAt.Add(time.Duration(cfg.IntervalMin) * time.Minute).UTC().Format(time.RFC3339)

	db, err := requireDB()
	if err != nil {
		cfg.LastRunStatus = "error"
		cfg.LastRunMsg = "DB 未就绪: " + err.Error()
		_ = a.SetExtractSchedule(cfg)
		return
	}

	// v0.2.35：KumoMTA + Postfix 都拉（之前只拉 kumomta，postfix 节点连自动调度都进不去）
	rows, err := db.Query(`SELECT id FROM vps_instances
	                       WHERE COALESCE(deploy_type,'kumomta') IN ('kumomta','postfix')
	                         AND COALESCE(status,'') != 'deleted'
	                         AND COALESCE(ip,'') != ''
	                         AND COALESCE(deploy_status,'') IN ('success','mta_ready','ptr_ready')`)
	if err != nil {
		cfg.LastRunStatus = "error"
		cfg.LastRunMsg = "查询 VPS 失败: " + err.Error()
		_ = a.SetExtractSchedule(cfg)
		return
	}
	var vpsIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			vpsIDs = append(vpsIDs, id)
		}
	}
	rows.Close()

	if len(vpsIDs) == 0 {
		cfg.LastRunStatus = "ok"
		cfg.LastRunMsg = "无可提取 VPS，跳过"
		_ = a.SetExtractSchedule(cfg)
		return
	}

	summary, err := a.ExtractFromVPSWithDelete(vpsIDs, cfg.DeleteAfter)
	if err != nil {
		cfg.LastRunStatus = "error"
		cfg.LastRunMsg = fmt.Sprintf("提取失败 (vps=%d): %v", len(vpsIDs), err)
	} else {
		cfg.LastRunStatus = "ok"
		cfg.LastRunMsg = fmt.Sprintf("提取 %d 台 VPS，新邮箱 %d / 总 %d，用时 %s",
			len(vpsIDs), summary.WriteResult.NewEmails, summary.TotalEmails, time.Since(startedAt).Truncate(time.Second))
	}
	_ = a.SetExtractSchedule(cfg)
	logger.Info("自动提取完成: %s", cfg.LastRunMsg)
}

