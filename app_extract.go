package main

import (
	"context"
	"database/sql"
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
// 仅支持 deploy_type='kumomta' 的 VPS；mailcow 节点会在 Result.Error 里标注跳过。
func (a *App) ExtractFromVPS(vpsIDs []string) (ExtractSummary, error) {
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

			if v.deployType == "mailcow" {
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
			content, _, err := ssh.ReadKumoMTALogs(ctx, cfg, "/var/log/kumomta/", "")
			if err != nil {
				res.Error = err.Error()
				results[i] = res
				logger.Warn("[extract %s] %s SSH 拉日志失败: %v", batchID, v.name, err)
				return
			}
			res.Lines = strings.Count(content, "\n")

			pr := parser.ParseKumoMTAStream(strings.NewReader(content))
			res.Parsed = pr.SentLines + pr.BouncedLines + pr.DeferredLines
			res.Emails = len(pr.Emails)

			mu.Lock()
			allEmails = append(allEmails, pr.Emails...)
			mu.Unlock()

			results[i] = res
			logger.Info("[extract %s] %s lines=%d parsed=%d emails=%d",
				batchID, v.name, res.Lines, res.Parsed, res.Emails)
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
	if len(uniq) > 0 {
		w := &export.Writer{OutputDir: outputDir, LinesPerFile: 50000}
		wr, werr := w.WriteCategorized(uniq)
		if werr != nil {
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
