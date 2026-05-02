package main

import (
	"context"
	"fmt"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"gcp-mailnode/internal/deploy"
	"gcp-mailnode/internal/gcp"
	"gcp-mailnode/internal/logger"
	"gcp-mailnode/internal/store"
)

// BatchTaskDTO 批量任务 DTO
type BatchTaskDTO struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	Total      int        `json:"total"`
	Succeeded  int        `json:"succeeded"`
	Failed     int        `json:"failed"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// StartBatch 启动批量部署
func (a *App) StartBatch(req deploy.BatchRequest) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	onLog := func(batchID string, slot int, level, msg string) {
		// 发前端事件
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "batch:log", map[string]interface{}{
				"batch_id": batchID,
				"slot":     slot,
				"level":    level,
				"msg":      msg,
			})
		}
		// 写日志库
		if db := store.DB(); db != nil {
			_, _ = db.Exec(
				`INSERT INTO batch_logs (batch_id, slot, level, message) VALUES (?,?,?,?)`,
				batchID, slot, level, msg)
		}
		logger.Info("[batch %s slot=%d %s] %s", batchID, slot, level, msg)
	}
	return deploy.Start(ctx, req, onLog)
}

// GetBatchProgress 查询进度
func (a *App) GetBatchProgress(batchID string) (deploy.BatchProgress, error) {
	return deploy.Progress(batchID)
}

// CancelBatch 取消
func (a *App) CancelBatch(batchID string) error {
	return deploy.Cancel(batchID)
}

// ListBatches 列出批量任务
func (a *App) ListBatches() ([]BatchTaskDTO, error) {
	db, err := requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT id, status, total, succeeded, failed, started_at, finished_at FROM batch_tasks ORDER BY started_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BatchTaskDTO{}
	for rows.Next() {
		var t BatchTaskDTO
		var finished *time.Time
		if err := rows.Scan(&t.ID, &t.Status, &t.Total, &t.Succeeded, &t.Failed, &t.StartedAt, &finished); err != nil {
			return nil, err
		}
		t.FinishedAt = finished
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ===== 4 阶段分步部署 API =====

// StartStageA 启动阶段 A：批量预留 + DNSBL 筛选 IP
func (a *App) StartStageA(req deploy.StageARequest) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	onLog := makeStageLogger(a, "stage-a:log")
	return deploy.StartStageA(ctx, req, onLog)
}

// StartStageB 启动阶段 B：基于 batchID 读 clean IP 开 VPS + 建 A 记录
func (a *App) StartStageB(batchID string, req deploy.StageBRequest) error {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	onLog := makeStageLogger(a, "stage-b:log")
	return deploy.StartStageB(ctx, batchID, req, onLog)
}

// BatchSetPTR 阶段 C：对选中 VPS 批量设 PTR
func (a *App) BatchSetPTR(vpsIDs []string) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	onLog := makeStageLogger(a, "stage-c:log")
	return deploy.StartBatchSetPTRTask(ctx, vpsIDs, onLog)
}

// StartMTADeploy 阶段 D：对选中 VPS 装 KumoMTA
func (a *App) StartMTADeploy(vpsIDs []string, opts deploy.DeployOpts) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	onLog := makeStageLogger(a, "stage-d:log")

	// 加载 persona（如指定）
	var personaSpec *deploy.PersonaSpec
	if opts.PersonaID != "" {
		p, err := GetPersona(opts.PersonaID)
		if err != nil {
			return "", fmt.Errorf("加载 Persona 失败: %w", err)
		}
		extra := make([]struct{ Name, Value string }, 0, len(p.ExtraHeaders))
		for _, h := range p.ExtraHeaders {
			extra = append(extra, struct{ Name, Value string }{h.Name, h.Value})
		}
		personaSpec = &deploy.PersonaSpec{
			ID:               p.ID,
			Name:             p.Name,
			ReceivedTemplate: p.ReceivedTemplate,
			UserAgent:        p.UserAgent,
			XMailer:          p.XMailer,
			ExtraHeaders:     extra,
		}
	}
	return deploy.StartMTADeployTask(ctx, vpsIDs, opts, personaSpec, onLog)
}

// StartStageC v0.1.6 新增：按"域名→IP"映射建 A 记录 + 装 KumoMTA
// 前端在 Step 3 收集到 `example1.com----104.x.x.1` 格式文本后解析成 map 传进来
func (a *App) StartStageC(req deploy.StageCRequest) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	onLog := makeStageLogger(a, "stage-c:log")
	var personaSpec *deploy.PersonaSpec
	if req.PersonaID != "" {
		p, err := GetPersona(req.PersonaID)
		if err != nil {
			return "", fmt.Errorf("加载 Persona 失败: %w", err)
		}
		extra := make([]struct{ Name, Value string }, 0, len(p.ExtraHeaders))
		for _, h := range p.ExtraHeaders {
			extra = append(extra, struct{ Name, Value string }{h.Name, h.Value})
		}
		personaSpec = &deploy.PersonaSpec{
			ID:               p.ID,
			Name:             p.Name,
			ReceivedTemplate: p.ReceivedTemplate,
			UserAgent:        p.UserAgent,
			XMailer:          p.XMailer,
			ExtraHeaders:     extra,
		}
	}
	return deploy.StartStageCWithPersona(ctx, req, personaSpec, onLog)
}

// PruneBatchIPs v0.1.9 新增：只保留用户勾选的 IP，其余未勾选的 clean IP 释放回 GCP。
// 调用时机：Step 1 筛完后，用户勾选要保留的 IP → 点"下一步（释放未勾选的）"时。
// keepIDs 中的 IP 保持不变；该 batch 内 status=clean 但 id 不在 keepIDs 里的，释放 GCP Address + 本地 status=released。
// 返回成功释放的数量；云端已不存在（404）的只清本地 status。
func (a *App) PruneBatchIPs(batchID string, keepIDs []string) (int, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if batchID == "" {
		return 0, fmt.Errorf("batchID 不能为空")
	}
	db, err := requireDB()
	if err != nil {
		return 0, err
	}
	keep := make(map[string]bool, len(keepIDs))
	for _, id := range keepIDs {
		keep[id] = true
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, gcp_cred_id, gcp_address_name, region, ip, COALESCE(slot_group,'')
		   FROM static_ips WHERE batch_id=? AND status='clean'`,
		batchID)
	if err != nil {
		return 0, fmt.Errorf("查询 batch IP 失败: %w", err)
	}
	type ipRow struct {
		id, credID, addrName, region, ip, slotGroup string
	}
	var allRows []ipRow
	for rows.Next() {
		var r ipRow
		if err := rows.Scan(&r.id, &r.credID, &r.addrName, &r.region, &r.ip, &r.slotGroup); err != nil {
			rows.Close()
			return 0, err
		}
		allRows = append(allRows, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	// v0.1.57：多 NIC 模式校验——slot_group 必须整组保留或整组释放，不允许半组。
	// 半组会导致 Stage B 创建 N<NICCount NIC 的实例，与模板不匹配。
	groupCnt := map[string]int{}
	groupKept := map[string]int{}
	for _, r := range allRows {
		if r.slotGroup == "" {
			continue
		}
		groupCnt[r.slotGroup]++
		if keep[r.id] {
			groupKept[r.slotGroup]++
		}
	}
	for sg, total := range groupCnt {
		kept := groupKept[sg]
		if kept != 0 && kept != total {
			return 0, fmt.Errorf("slot_group %s 不完整：保留 %d/%d；多 NIC 组必须整组保留或整组释放", sg[:8], kept, total)
		}
	}

	var toRelease []ipRow
	for _, r := range allRows {
		if !keep[r.id] {
			toRelease = append(toRelease, r)
		}
	}

	if len(toRelease) == 0 {
		return 0, nil
	}

	clients := map[string]*gcp.Client{}
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()
	released := 0
	for _, r := range toRelease {
		cli, ok := clients[r.credID]
		if !ok {
			c, err := loadGCPClientForApp(ctx, r.credID)
			if err != nil {
				logger.Warn("加载 GCP 客户端失败 cred=%s: %v", r.credID, err)
				continue
			}
			clients[r.credID] = c
			cli = c
		}
		if err := cli.ReleaseStaticAddress(ctx, r.region, r.addrName); err != nil {
			if !gcp.IsNotFound(err) {
				logger.Warn("释放 IP %s 失败: %v", r.ip, err)
				continue
			}
			logger.Info("IP %s 在云端已不存在，仅清本地状态", r.ip)
		}
		_, _ = db.ExecContext(ctx, `UPDATE static_ips SET status='released', bound_instance_id='' WHERE id=?`, r.id)
		released++
	}
	return released, nil
}

// makeStageLogger 构造一个把日志同时落 DB + 推前端事件的 LogCallback
func makeStageLogger(a *App, eventName string) deploy.LogCallback {
	return func(batchID string, slot int, level, msg string) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, eventName, map[string]interface{}{
				"batch_id": batchID,
				"slot":     slot,
				"level":    level,
				"msg":      msg,
			})
		}
		if db := store.DB(); db != nil {
			_, _ = db.Exec(
				`INSERT INTO batch_logs (batch_id, slot, level, message) VALUES (?,?,?,?)`,
				batchID, slot, level, msg)
		}
		logger.Info("[%s %s slot=%d %s] %s", eventName, batchID, slot, level, msg)
	}
}

// GetBatchLogs 获取指定批量任务的日志（最近 N 条）
func (a *App) GetBatchLogs(batchID string, limit int) ([]map[string]interface{}, error) {
	db, err := requireDB()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := db.Query(
		`SELECT id, slot, level, message, created_at FROM batch_logs WHERE batch_id=? ORDER BY id DESC LIMIT ?`,
		batchID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	for rows.Next() {
		var (
			id             int64
			slot           int
			level, message string
			createdAt      time.Time
		)
		if err := rows.Scan(&id, &slot, &level, &message, &createdAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]interface{}{
			"id":         id,
			"slot":       slot,
			"level":      level,
			"message":    message,
			"created_at": createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
