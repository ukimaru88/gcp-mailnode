package deploy

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"gcp-mailnode/internal/dns"
	"gcp-mailnode/internal/dnsbl"
	"gcp-mailnode/internal/gcp"
	"gcp-mailnode/internal/ssh"
	"gcp-mailnode/internal/sshkey"
	"gcp-mailnode/internal/store"
)

type StageARequest struct {
	GCPCredIDs      []string `json:"gcp_cred_ids"`
	TemplateID      string   `json:"template_id"`
	Count           int      `json:"count"`
	Regions         []string `json:"regions"`
	DNSBLThreshold  int      `json:"dnsbl_threshold"`
	MaxRetryPerSlot int      `json:"max_retry_per_slot"`
	IPPrefixFilter  []string `json:"ip_prefix_filter"`
	IPPrefixExclude []string `json:"ip_prefix_exclude"`
}

type StageBRequest struct {
	TemplateID   string `json:"template_id"`
	RootPassword string `json:"root_password"`
}

type StageCRequest struct {
	DomainIPMap  map[string]string `json:"domain_ip_map"`
	AliyunCredID string            `json:"aliyun_cred_id"`
	HideClientIP bool              `json:"hide_client_ip"`
	PersonaID    string            `json:"persona_id"`
}

type DeployOpts struct {
	HideClientIP bool   `json:"hide_client_ip"`
	PersonaID    string `json:"persona_id"`
}

type PersonaSpec struct {
	ID               string
	Name             string
	ReceivedTemplate string
	UserAgent        string
	XMailer          string
	ExtraHeaders     []struct {
		Name  string
		Value string
	}
}

func StartStageA(ctx context.Context, req StageARequest, onLog LogCallback) (string, error) {
	if req.Count <= 0 {
		return "", fmt.Errorf("Count 必须 > 0")
	}
	if len(req.GCPCredIDs) == 0 {
		return "", fmt.Errorf("至少选择一个 GCP 凭证")
	}
	if req.TemplateID == "" {
		return "", fmt.Errorf("必须指定模板 ID")
	}
	if req.DNSBLThreshold <= 0 {
		req.DNSBLThreshold = 1
	}
	// MaxRetryPerSlot == 0 表示无限循环直到达到目标；保持不变传到 worker 逻辑
	if req.MaxRetryPerSlot < 0 {
		req.MaxRetryPerSlot = 0
	}
	tmpl, err := loadTemplate(req.TemplateID)
	if err != nil {
		return "", err
	}
	regions := req.Regions
	if len(regions) == 0 {
		regions = tmpl.Regions
	}
	if len(regions) == 0 {
		regions = []string{"asia-northeast1", "asia-northeast2"}
	}
	if onLog == nil {
		onLog = func(string, int, string, string) {}
	}
	db := store.DB()
	if db == nil {
		return "", fmt.Errorf("数据库未就绪")
	}

	batchID := uuid.NewString()
	reqJSON, _ := json.Marshal(req)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO batch_tasks (id, request_json, status, total) VALUES (?,?,?,?)`,
		batchID, string(reqJSON), "stage-a-running", req.Count); err != nil {
		return "", fmt.Errorf("写入 batch_tasks 失败: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	state := &batchState{id: batchID, total: req.Count, status: "stage-a-running", cancel: cancel}
	runningBatches.Store(batchID, state)

	go func() {
		defer finishStageTask(db, batchID, state, "stage-a-done")

		gcpClients := map[string]*gcp.Client{}
		var gcpMu sync.Mutex
		getGCP := func(credID string) (*gcp.Client, error) {
			gcpMu.Lock()
			defer gcpMu.Unlock()
			if cli, ok := gcpClients[credID]; ok {
				return cli, nil
			}
			cli, err := loadGCPClient(runCtx, credID)
			if err != nil {
				return nil, err
			}
			gcpClients[credID] = cli
			return cli, nil
		}
		defer func() {
			for _, cli := range gcpClients {
				_ = cli.Close()
			}
		}()

		var exhausted sync.Map
		// 目标驱动模型（v0.1.20）：并发 worker 持续抽，累计 succeeded 到目标 req.Count 就退。
		// 单次尝试失败（前缀/黑名单/DNSBL/预留 err）不占用成功计数，只累计 totalAttempts。
		// 全局 totalAttempts 上限 = req.Count * req.MaxRetryPerSlot，防止无限循环烧钱。
		concurrency := req.Count
		if concurrency > 10 {
			concurrency = 10
		}
		// MaxRetryPerSlot=0 表示"无限循环直到达到目标 N 或全部 region 配额耗尽或用户取消"
		// 非零时，总尝试上限 = N * MaxRetryPerSlot（防误触烧钱）
		var maxTotalAttempts int64 = 0
		if req.MaxRetryPerSlot > 0 {
			maxTotalAttempts = int64(req.Count) * int64(req.MaxRetryPerSlot)
			if maxTotalAttempts < 50 {
				maxTotalAttempts = 50
			}
		}
		var totalAttempts int64
		var wg sync.WaitGroup

		// CAS 抢占成功槽位（精确达到 N，不超）
		claimSlot := func() bool {
			for {
				cur := atomic.LoadInt64(&state.succeeded)
				if cur >= int64(req.Count) {
					return false
				}
				if atomic.CompareAndSwapInt64(&state.succeeded, cur, cur+1) {
					return true
				}
			}
		}

		worker := func(workerID int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					onLog(batchID, workerID, "ERROR", fmt.Sprintf("worker panic: %v", r))
				}
			}()
			for {
				select {
				case <-runCtx.Done():
					return
				default:
				}
				if atomic.LoadInt64(&state.succeeded) >= int64(req.Count) {
					return
				}
				attempts := atomic.AddInt64(&totalAttempts, 1)
				if maxTotalAttempts > 0 && attempts > maxTotalAttempts {
					return
				}
				// 所有 region 都耗尽配额就退出，没必要死循环
				allBlown := true
				for _, r := range regions {
					if _, blown := exhausted.Load(r); !blown {
						allBlown = false
						break
					}
				}
				if allBlown {
					return
				}

				attempt := atomic.LoadInt64(&totalAttempts)
				gcpCredID := req.GCPCredIDs[int(attempt-1)%len(req.GCPCredIDs)]
				region := regions[int(attempt-1)%len(regions)]

				if _, blown := exhausted.Load(region); blown {
					continue
				}

				cli, err := getGCP(gcpCredID)
				if err != nil {
					onLog(batchID, workerID, "ERROR", fmt.Sprintf("获取 GCP 客户端失败: %v", err))
					continue
				}
				// 成功拿 clean IP 前先检查是否还需要（减少浪费）
				if atomic.LoadInt64(&state.succeeded) >= int64(req.Count) {
					return
				}
				if err := reserveAndFilterOnce(runCtx, batchID, workerID, gcpCredID, region,
					req.DNSBLThreshold, req.IPPrefixFilter, req.IPPrefixExclude, &exhausted, cli, onLog, claimSlot); err != nil {
					continue
				}
				// 成功写 clean 的槽位已由 reserveAndFilterOnce 通过 claimSlot 原子占上
			}
		}

		for i := 1; i <= concurrency; i++ {
			wg.Add(1)
			go worker(i)
		}
		wg.Wait()

		// 收尾统计
		got := int(atomic.LoadInt64(&state.succeeded))
		used := int(atomic.LoadInt64(&totalAttempts))
		if got < req.Count {
			atomic.StoreInt64(&state.failed, int64(req.Count-got))
			if maxTotalAttempts > 0 && int64(used) >= maxTotalAttempts {
				onLog(batchID, 0, "WARN", fmt.Sprintf("达到总尝试上限 %d 次，筛到 %d/%d 个 clean IP，停止。建议：把「每槽最大重试」改为 0（无限）或放宽过滤条件。", maxTotalAttempts, got, req.Count))
			} else {
				onLog(batchID, 0, "WARN", fmt.Sprintf("已取消或两个 region 配额全部耗尽，筛到 %d/%d 个 clean IP。请到 GCP Console → Quotas 给 asia-northeast1/2 提额。", got, req.Count))
			}
		} else {
			onLog(batchID, 0, "INFO", fmt.Sprintf("目标达成：%d 个 clean IP（总尝试 %d 次）", got, used))
		}
	}()

	return batchID, nil
}

// reserveAndFilterOnce 单次尝试：预留 + 前缀 + 黑段 + DNSBL 判定。
// 判定为 clean 时调用 claimSlot 原子抢占槽位；抢到才写 status=clean，抢不到（已达目标）立刻释放 IP 避免烧钱。
// 返回 nil 表示本次成功占位+写入；其他情况返回 err，调用方继续下一轮抽取。
func reserveAndFilterOnce(ctx context.Context, batchID string, workerID int, gcpCredID, region string, threshold int, prefixFilter, prefixExclude []string, exhausted *sync.Map, gcpClient *gcp.Client, onLog LogCallback, claimSlot func() bool) error {
	log := func(level, format string, args ...interface{}) {
		onLog(batchID, workerID, level, fmt.Sprintf(format, args...))
	}
	db := store.DB()
	if db == nil {
		return fmt.Errorf("数据库未就绪")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	matchesPrefix := func(ip string) bool {
		if len(prefixFilter) == 0 {
			return true
		}
		for _, p := range prefixFilter {
			if strings.HasPrefix(ip, p) {
				return true
			}
		}
		return false
	}
	matchesExclude := func(ip string) string {
		for _, p := range prefixExclude {
			if strings.HasPrefix(ip, p) {
				return p
			}
		}
		return ""
	}
	addr, err := gcpClient.ReserveStaticAddress(ctx, region, "")
	if err != nil {
		if gcp.IsQuotaExceeded(err) {
			if exhausted != nil {
				exhausted.Store(region, true)
			}
			log("WARN", "region %s 静态 IP 配额耗尽，暂停该 region 抽取。请到 GCP Console 提额。", region)
			return fmt.Errorf("region %s QUOTA_EXCEEDED", region)
		}
		log("ERROR", "预留静态 IP 失败: %v", err)
		return err
	}
	ipID := uuid.NewString()
	_, _ = db.ExecContext(ctx,
		`INSERT INTO static_ips (id, gcp_cred_id, gcp_address_name, ip, region, status, batch_id) VALUES (?,?,?,?,?,?,?)`,
		ipID, gcpCredID, addr.Name, addr.IP, region, "reserved", batchID)

	release := func(status, result, hitLists string) {
		_, _ = db.ExecContext(ctx,
			`UPDATE static_ips SET status=?, dnsbl_result=?, dnsbl_hit_lists=?, bound_instance_id='' WHERE id=?`,
			status, result, hitLists, ipID)
		_ = gcpClient.ReleaseStaticAddress(ctx, region, addr.Name)
	}
	if !matchesPrefix(addr.IP) {
		log("WARN", "IP %s 不匹配前缀白名单 %v，释放重试", addr.IP, prefixFilter)
		release("released", "prefix_mismatch", "")
		return fmt.Errorf("prefix_mismatch")
	}
	if hit := matchesExclude(addr.IP); hit != "" {
		log("WARN", "IP %s 命中前缀黑名单 %s，释放重试", addr.IP, hit)
		release("released", "prefix_excluded", hit)
		return fmt.Errorf("prefix_excluded")
	}
	if seg, note, berr := dnsbl.ContainsIP(ctx, addr.IP); berr == nil && seg != "" {
		log("WARN", "IP %s 命中黑段 %s(%s)，释放", addr.IP, seg, note)
		release("released", "blacklisted", "blackseg:"+seg)
		return fmt.Errorf("blacklisted")
	}

	verdict, reason, detail, derr := dnsbl.Decide(ctx, addr.IP, dnsbl.CheckOptions{Threshold: threshold}, 6*time.Hour)
	hitLists := ""
	if detail != nil {
		hitLists = strings.Join(detail.HitLists, ",")
	}
	_, _ = db.ExecContext(ctx, `UPDATE static_ips SET dnsbl_result=?, dnsbl_hit_lists=? WHERE id=?`, verdict, hitLists, ipID)
	if derr != nil {
		log("WARN", "DNSBL 检测失败/不确定: %v，释放 IP 后重试", derr)
		release("released", "dnsbl_error", hitLists)
		return derr
	}
	if verdict == "clean" {
		// CAS 抢占槽位：抢到才保留并标 clean；抢不到（已达目标 N）则释放 IP 避免烧钱
		if claimSlot != nil && !claimSlot() {
			log("INFO", "IP %s 判定 clean 但已达目标 N，释放", addr.IP)
			release("released", "target_reached", "")
			return fmt.Errorf("target_reached")
		}
		log("INFO", "IP %s 检测通过 (%s)", addr.IP, reason)
		_, _ = db.ExecContext(ctx, `UPDATE static_ips SET status='clean' WHERE id=?`, ipID)
		return nil
	}
	log("WARN", "IP %s 判定: %s (%s)，释放重试", addr.IP, verdict, reason)
	release("released", verdict, hitLists)
	return fmt.Errorf("%s", verdict)
}

func StartStageB(ctx context.Context, batchID string, req StageBRequest, onLog LogCallback) error {
	if batchID == "" {
		return fmt.Errorf("batchID 不能为空")
	}
	if req.TemplateID == "" {
		return fmt.Errorf("必须指定模板 ID")
	}
	if req.RootPassword == "" {
		req.RootPassword = "ChangeMe!" + uuid.NewString()[:6]
	}
	if onLog == nil {
		onLog = func(string, int, string, string) {}
	}
	db := store.DB()
	if db == nil {
		return fmt.Errorf("数据库未就绪")
	}
	tmpl, err := loadTemplate(req.TemplateID)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, gcp_cred_id, gcp_address_name, ip, region FROM static_ips WHERE batch_id=? AND status='clean' AND bound_instance_id='' ORDER BY created_at ASC`, batchID)
	if err != nil {
		return fmt.Errorf("查询 clean IP 失败: %w", err)
	}
	type slotIP struct{ ipID, gcpCredID, addrName, ip, region string }
	var ips []slotIP
	for rows.Next() {
		var s slotIP
		if err := rows.Scan(&s.ipID, &s.gcpCredID, &s.addrName, &s.ip, &s.region); err != nil {
			rows.Close()
			return err
		}
		ips = append(ips, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(ips) == 0 {
		return fmt.Errorf("该批次没有 clean IP，是否先跑阶段 A？")
	}

	runCtx, cancel := context.WithCancel(ctx)
	stRaw, ok := runningBatches.Load(batchID)
	var state *batchState
	if ok {
		state = stRaw.(*batchState)
		state.mu.Lock()
		state.status = "stage-b-running"
		state.total = len(ips)
		state.cancel = cancel
		atomic.StoreInt64(&state.succeeded, 0)
		atomic.StoreInt64(&state.failed, 0)
		state.mu.Unlock()
	} else {
		state = &batchState{id: batchID, total: len(ips), status: "stage-b-running", cancel: cancel}
		runningBatches.Store(batchID, state)
	}
	_, _ = db.Exec(`UPDATE batch_tasks SET status='stage-b-running', finished_at=NULL WHERE id=?`, batchID)

	go func() {
		defer func() {
			got := int(atomic.LoadInt64(&state.succeeded))
			failed := int(atomic.LoadInt64(&state.failed))
			if failed > 0 {
				onLog(batchID, 0, "WARN", fmt.Sprintf("====== 阶段 B 完成：成功 %d / 失败 %d（共 %d 台）======", got, failed, len(ips)))
				onLog(batchID, 0, "WARN", fmt.Sprintf("⚠ 有 %d 台 VPS 创建/探测失败（通常是 zone 资源不足或 IP 占用）。处理建议：", failed))
				onLog(batchID, 0, "WARN", "  1. 去「资源清单」查看 status=deleted 的 VPS 记录，deploy_error 里有具体原因")
				onLog(batchID, 0, "WARN", "  2. 失败对应的 clean IP 也已被释放回 GCP，可以回 Step 1 重新筛补齐缺口")
				onLog(batchID, 0, "WARN", "  3. 或直接用当前成功的 "+fmt.Sprintf("%d", got)+" 台进入 Step 3")
			} else {
				onLog(batchID, 0, "SUCCESS", fmt.Sprintf("====== 阶段 B 完成：全部 %d 台 VPS 就绪 ======", got))
			}
			finishStageTask(db, batchID, state, "stage-b-done")
		}()

		gcpClients := map[string]*gcp.Client{}
		firewallReady := map[string]bool{}
		var gcpMu sync.Mutex
		getGCP := func(credID string) (*gcp.Client, error) {
			gcpMu.Lock()
			defer gcpMu.Unlock()
			if cli, ok := gcpClients[credID]; ok {
				return cli, nil
			}
			cli, err := loadGCPClient(runCtx, credID)
			if err != nil {
				return nil, err
			}
			gcpClients[credID] = cli
			if !firewallReady[credID] {
				if err := cli.EnsureMailNodeFirewall(runCtx); err != nil {
					onLog(batchID, 0, "WARN", fmt.Sprintf("确保防火墙规则失败 cred=%s: %v", credID, err))
				} else {
					firewallReady[credID] = true
				}
			}
			return cli, nil
		}
		defer func() {
			for _, cli := range gcpClients {
				_ = cli.Close()
			}
		}()

		concurrency := len(ips)
		if concurrency > 10 {
			concurrency = 10
		}
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
	dispatchLoop:
		for i, s := range ips {
			select {
			case <-runCtx.Done():
				onLog(batchID, 0, "WARN", "阶段 B 已取消，停止派发剩余 slot")
				atomic.AddInt64(&state.failed, int64(len(ips)-i))
				break dispatchLoop
			default:
			}
			slot := i + 1
			sem <- struct{}{}
			wg.Add(1)
			go func(slot int, s slotIP) {
				defer wg.Done()
				defer func() { <-sem }()
				cli, err := getGCP(s.gcpCredID)
				if err != nil {
					onLog(batchID, slot, "ERROR", fmt.Sprintf("获取 GCP 客户端失败: %v", err))
					atomic.AddInt64(&state.failed, 1)
					return
				}
				if err := createVPSOnly(runCtx, batchID, slot, s.ipID, s.gcpCredID, s.addrName, s.ip, s.region, req.RootPassword, tmpl, cli, onLog); err != nil {
					atomic.AddInt64(&state.failed, 1)
					return
				}
				atomic.AddInt64(&state.succeeded, 1)
			}(slot, s)
		}
		wg.Wait()
	}()
	return nil
}

func createVPSOnly(ctx context.Context, batchID string, slot int, staticIPID, gcpCredID, addrName, ip, region, rootPassword string, tmpl VPSTemplate, gcpClient *gcp.Client, onLog LogCallback) error {
	log := func(level, format string, args ...interface{}) {
		onLog(batchID, slot, level, fmt.Sprintf(format, args...))
	}
	db := store.DB()
	if db == nil {
		return fmt.Errorf("数据库未就绪")
	}
	startupScript := buildStartupScript(tmpl.MetadataScript)
	instanceName := fmt.Sprintf("mn-%s", uuid.NewString()[:8])
	machineType := defaultString(tmpl.MachineType, "e2-micro")
	imageFamily := defaultString(tmpl.ImageFamily, "debian-12")
	imageProject := defaultString(tmpl.ImageProject, "debian-cloud")
	diskSize := tmpl.DiskSizeGB
	if diskSize <= 0 {
		diskSize = 10
	}
	diskType := defaultString(tmpl.DiskType, "pd-balanced")

	var zone string
	var createErr error
	for i, sfx := range []string{"a", "b", "c"} {
		tryZone := region + "-" + sfx
		if i > 0 {
			// 指数退避：第 2 次 30s，第 3 次 60s。GCP 的 "IP already in-use" 元数据
			// 通常在上次失败后 30-60s 才真正清除，15s 不够。
			waitSecs := 15 + i*15
			log("INFO", "切换到 zone %s 前等待 %ds（GCP 可能需要时间清理上次 IP 占用元数据）", tryZone, waitSecs)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(waitSecs) * time.Second):
			}
		}
		_, createErr = gcpClient.CreateInstance(ctx, gcp.InstanceSpec{
			Name:          instanceName,
			Zone:          tryZone,
			MachineType:   machineType,
			ImageFamily:   imageFamily,
			ImageProject:  imageProject,
			DiskSizeGB:    diskSize,
			DiskType:      diskType,
			Tags:          mergeMailNodeTag(tmpl.Tags),
			StartupScript: startupScript,
			StaticIP:      ip,
			NetworkName:   "default",
		})
		if createErr == nil {
			zone = tryZone
			break
		}
		msg := createErr.Error()
		if strings.Contains(msg, "ZONE_RESOURCE_POOL_EXHAUSTED") ||
			strings.Contains(msg, "does not have enough resources") ||
			strings.Contains(msg, "already in-use") ||
			strings.Contains(msg, "IP_IN_USE_BY_ANOTHER_RESOURCE") {
			// 查 address.Users 看谁占着这个 IP（可能是 GCP 元数据滞后，或上次删除未完成）
			if info, gerr := gcpClient.GetAddress(ctx, region, addrName); gerr == nil && len(info.Users) > 0 {
				log("WARN", "zone %s 失败，IP %s 当前被占用: %v", tryZone, ip, info.Users)
			} else {
				log("WARN", "zone %s 失败: %v（IP %s 查询 Users 为空，可能 GCP 元数据滞后）", tryZone, createErr, ip)
			}
			continue
		}
		break
	}
	if createErr != nil {
		_, _ = db.ExecContext(ctx, `UPDATE static_ips SET status='released', bound_instance_id='' WHERE id=?`, staticIPID)
		_ = gcpClient.ReleaseStaticAddress(context.Background(), region, addrName)
		return createErr
	}

	vpsID := uuid.NewString()
	deployType := tmpl.DeployType
	if deployType == "" {
		deployType = "kumomta"
	}
	_, _ = db.ExecContext(ctx,
		`INSERT INTO vps_instances (id, gcp_cred_id, gcp_instance_id, name, region, zone, machine_type, status, ip, fqdn, root_password, deploy_status, batch_id, deploy_type)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		vpsID, gcpCredID, instanceName, instanceName, region, zone, machineType, "pending", ip, "", rootPassword, "vps_pending", batchID, deployType)
	cleanup := func(reason error) {
		msg := reason.Error()
		log("WARN", "创建后的探测失败，清理 VM 和静态 IP: %v", reason)
		// 不继承 runCtx（用户 Cancel 时 ctx 已 done，但仍要清理），但限 2 分钟总超时避免卡死
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancelCleanup()
		if err := gcpClient.DeleteInstance(cleanupCtx, zone, instanceName); err != nil && !gcp.IsNotFound(err) {
			log("WARN", "删除 VM 失败，需要人工检查: %v", err)
		}
		if err := gcpClient.ReleaseStaticAddress(cleanupCtx, region, addrName); err != nil && !gcp.IsNotFound(err) {
			log("WARN", "释放静态 IP 失败，需要人工检查: %v", err)
		}
		_, _ = db.Exec(`UPDATE vps_instances SET status='deleted', deploy_status='failed', deploy_error=? WHERE id=?`, msg, vpsID)
		_, _ = db.Exec(`UPDATE static_ips SET status='released', bound_instance_id='' WHERE id=?`, staticIPID)
	}

	if _, err := gcpClient.WaitForRunning(ctx, zone, instanceName, 5*time.Minute); err != nil {
		cleanup(err)
		return err
	}
	_, _ = db.ExecContext(ctx, `UPDATE vps_instances SET status='running' WHERE id=?`, vpsID)
	_, _ = db.ExecContext(ctx, `UPDATE static_ips SET status='in_use', bound_instance_id=? WHERE id=?`, vpsID, staticIPID)

	sshCfg := ssh.Config{Host: ip, Port: 22, Username: "root", KeyContent: string(sshkey.PrivatePEM())}
	for i := 1; i <= 10; i++ {
		select {
		case <-ctx.Done():
			err := fmt.Errorf("cancelled")
			cleanup(err)
			return ctx.Err()
		default:
		}
		if err := ssh.TestConnection(sshCfg); err == nil {
			_, _ = db.ExecContext(ctx, `UPDATE vps_instances SET deploy_status='vps_running', deploy_error='' WHERE id=?`, vpsID)
			// 验证 VM 的 network tags 已包含 mail-node，否则外部 25/587 等端口会被防火墙拦。
			if info, gerr := gcpClient.GetInstance(ctx, zone, instanceName); gerr == nil {
				hasMailTag := false
				for _, t := range info.Tags {
					if t == gcp.MailNodeTag {
						hasMailTag = true
						break
					}
				}
				if !hasMailTag {
					log("WARN", "VM %s 未命中 %s tag，自动补打（否则 25/587 等外部端口会被防火墙拦）", instanceName, gcp.MailNodeTag)
					newTags := append([]string{}, info.Tags...)
					newTags = append(newTags, gcp.MailNodeTag)
					if setErr := gcpClient.SetInstanceTags(ctx, zone, instanceName, newTags, info.TagsFingerprint); setErr != nil {
						log("WARN", "自动补 %s tag 失败（需手动去 GCP Console 添加）: %v", gcp.MailNodeTag, setErr)
					} else {
						log("INFO", "✅ 已自动补 %s tag", gcp.MailNodeTag)
					}
				}
			}
			log("INFO", "阶段 B 完成: ip=%s", ip)
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	err := fmt.Errorf("SSH 探测 10 次未成功")
	cleanup(err)
	return err
}

func StartStageC(ctx context.Context, req StageCRequest, onLog LogCallback) (string, error) {
	return StartStageCWithPersona(ctx, req, nil, onLog)
}

func StartStageCWithPersona(ctx context.Context, req StageCRequest, personaSpec *PersonaSpec, onLog LogCallback) (string, error) {
	if len(req.DomainIPMap) == 0 {
		return "", fmt.Errorf("域名到 IP 映射不能为空")
	}
	if req.AliyunCredID == "" {
		return "", fmt.Errorf("必须选择阿里云凭证")
	}
	if onLog == nil {
		onLog = func(string, int, string, string) {}
	}
	db := store.DB()
	if db == nil {
		return "", fmt.Errorf("数据库未就绪")
	}
	aliyunDNS, err := loadAliyunDns(req.AliyunCredID)
	if err != nil {
		return "", fmt.Errorf("加载阿里云凭证失败: %w", err)
	}
	domains := make([]string, 0, len(req.DomainIPMap))
	for d := range req.DomainIPMap {
		d = strings.TrimSpace(d)
		if d != "" {
			domains = append(domains, d)
		}
	}
	sort.Strings(domains)
	if len(domains) == 0 {
		return "", fmt.Errorf("域名到 IP 映射不能为空")
	}

	taskID := uuid.NewString()
	reqJSON, _ := json.Marshal(req)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO batch_tasks (id, request_json, status, total) VALUES (?,?,?,?)`,
		taskID, string(reqJSON), "stage-c-running", len(domains)); err != nil {
		return "", fmt.Errorf("写入 batch_tasks 失败: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	state := &batchState{id: taskID, total: len(domains), status: "stage-c-running", cancel: cancel}
	runningBatches.Store(taskID, state)

	go func() {
		defer finishStageTask(db, taskID, state, "stage-c-done")
		// 并发：最多 10 个 domain 同时搭建。每台 2-5 分钟（docker pull + apt install + 生成 DKIM + 启动 KumoMTA），
		// 串行 6 台 = 20-30 分钟；并发后 ~5 分钟全部完成。
		concurrency := len(domains)
		if concurrency > 10 {
			concurrency = 10
		}
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
		for i, domain := range domains {
			slot := i + 1
			d := domain
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				defer func() {
					if r := recover(); r != nil {
						onLog(taskID, slot, "ERROR", fmt.Sprintf("Stage C slot panic: %v", r))
						atomic.AddInt64(&state.failed, 1)
					}
				}()
				select {
				case <-runCtx.Done():
					onLog(taskID, slot, "WARN", "Stage C 已取消")
					atomic.AddInt64(&state.failed, 1)
					return
				default:
				}
				ip := strings.TrimSpace(req.DomainIPMap[d])
				logC := func(level, format string, args ...interface{}) {
					onLog(taskID, slot, level, fmt.Sprintf(format, args...))
				}
				var vpsID, rootPwd string
				row := db.QueryRowContext(runCtx,
					`SELECT id, root_password FROM vps_instances
					 WHERE ip=? AND deploy_status IN ('vps_running','ptr_ready')
					   AND status!='deleted'
					 ORDER BY created_at DESC LIMIT 1`, ip)
				if err := row.Scan(&vpsID, &rootPwd); err != nil {
					if err == sql.ErrNoRows {
						logC("ERROR", "IP %s 没有可进入 Stage C 的 VPS（仅允许 vps_running / ptr_ready，且未删除）", ip)
					} else {
						logC("ERROR", "查询 VPS 失败: %v", err)
					}
					atomic.AddInt64(&state.failed, 1)
					return
				}
				fqdn := d
				_, _ = db.ExecContext(runCtx,
					`UPDATE vps_instances SET domain=?, fqdn=?, aliyun_cred_id=?, deploy_status='mta_deploying', persona_id=?, hide_client_ip=? WHERE id=?`,
					d, fqdn, req.AliyunCredID, req.PersonaID, boolToInt(req.HideClientIP), vpsID)
				if err := upsertAliyunRecordAndSyncLocal(runCtx, db, aliyunDNS, req.AliyunCredID, d, vpsID, dns.DnsRecordSpec{RR: "@", RecordType: "A", Value: ip}, logC); err != nil {
					logC("WARN", "UpsertRecord A 失败: %v", err)
				}
				if err := deployMTAOnVPS(runCtx, vpsID, ip, fqdn, "@", d, rootPwd, DeployOpts{HideClientIP: req.HideClientIP, PersonaID: req.PersonaID}, personaSpec, aliyunDNS, req.AliyunCredID, logC); err != nil {
					logC("ERROR", "部署 KumoMTA 失败: %v", err)
					_, _ = db.Exec(`UPDATE vps_instances SET deploy_status='failed', deploy_error=? WHERE id=?`, err.Error(), vpsID)
					atomic.AddInt64(&state.failed, 1)
					return
				}
				_, _ = db.Exec(`UPDATE vps_instances SET deploy_status='mta_ready', deploy_error='' WHERE id=?`, vpsID)
				atomic.AddInt64(&state.succeeded, 1)
				logC("INFO", "Stage C 完成: %s -> %s", d, ip)
			}()
		}
		wg.Wait()
	}()
	return taskID, nil
}

func BatchSetPTR(ctx context.Context, vpsIDs []string, onLog LogCallback) error {
	if len(vpsIDs) == 0 {
		return fmt.Errorf("至少选择一个 VPS")
	}
	if onLog == nil {
		onLog = func(string, int, string, string) {}
	}
	db := store.DB()
	if db == nil {
		return fmt.Errorf("数据库未就绪")
	}
	// GCP client 缓存（按 credID 共享，跨 goroutine 用 mutex 保护 map 写）
	gcpClients := map[string]*gcp.Client{}
	var cliMu sync.Mutex
	getCli := func(credID string) (*gcp.Client, error) {
		cliMu.Lock()
		defer cliMu.Unlock()
		if c, ok := gcpClients[credID]; ok {
			return c, nil
		}
		c, err := loadGCPClient(ctx, credID)
		if err != nil {
			return nil, err
		}
		gcpClients[credID] = c
		return c, nil
	}
	defer func() {
		for _, cli := range gcpClients {
			_ = cli.Close()
		}
	}()

	// 并发执行：每台独立 goroutine，6 台同时跑不串行等
	var wg sync.WaitGroup
	for i, vpsID := range vpsIDs {
		slot := i + 1
		id := vpsID
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					onLog("ptr", slot, "ERROR", fmt.Sprintf("PTR panic: %v", r))
				}
			}()
			log := func(level, format string, args ...interface{}) {
				onLog("ptr", slot, level, fmt.Sprintf(format, args...))
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
			var gcpCredID, zone, instanceName, ip, fqdn string
			row := db.QueryRowContext(ctx, `SELECT gcp_cred_id, zone, name, ip, fqdn FROM vps_instances WHERE id=?`, id)
			if err := row.Scan(&gcpCredID, &zone, &instanceName, &ip, &fqdn); err != nil {
				log("ERROR", "读取 VPS 失败: %v", err)
				return
			}
			if ip == "" || fqdn == "" {
				log("ERROR", "VPS %s 缺少 ip 或 fqdn", id)
				return
			}
			cli, err := getCli(gcpCredID)
			if err != nil {
				log("ERROR", "加载 GCP 客户端失败: %v", err)
				_, _ = db.Exec(`UPDATE vps_instances SET ptr_status='failed', deploy_error=? WHERE id=?`, err.Error(), id)
				return
			}
			_, _ = db.Exec(`UPDATE vps_instances SET ptr_status='pending' WHERE id=?`, id)
			// 直接试 SetPTR，不预先等 DNS 生效。GCP 若拒绝（A 记录还没扩散）会返回错误，
			// 用户看到 failed 后等几分钟再点"批量设 PTR"重试即可。
			log("INFO", "设置 PTR: %s -> %s", ip, fqdn)
			if err := cli.SetInstancePTR(ctx, zone, instanceName, ip, fqdn); err != nil {
				log("ERROR", "SetPTR 失败（A 记录可能未生效，等几分钟后重试）: %v", err)
				_, _ = db.Exec(`UPDATE vps_instances SET ptr_status='failed', deploy_error=? WHERE id=?`, err.Error(), id)
				return
			}
			_, _ = db.Exec(`UPDATE vps_instances SET ptr_status='set', deploy_status='ptr_ready', deploy_error='' WHERE id=?`, id)
			log("INFO", "✅ PTR 设置成功: %s", fqdn)
		}()
	}
	wg.Wait()
	return nil
}

func StartBatchSetPTRTask(ctx context.Context, vpsIDs []string, onLog LogCallback) (string, error) {
	if len(vpsIDs) == 0 {
		return "", fmt.Errorf("至少选择一个 VPS")
	}
	if onLog == nil {
		onLog = func(string, int, string, string) {}
	}
	db := store.DB()
	if db == nil {
		return "", fmt.Errorf("数据库未就绪")
	}
	reqJSON, _ := json.Marshal(map[string]interface{}{"vps_ids": vpsIDs})
	return startCancelableStageTask(ctx, db, "ptr-running", len(vpsIDs), string(reqJSON), func(runCtx context.Context, taskID string) error {
		return BatchSetPTR(runCtx, vpsIDs, func(_ string, slot int, level, msg string) {
			onLog(taskID, slot, level, msg)
		})
	})
}

func waitDNSResolves(ctx context.Context, fqdn, ip string, maxAttempts int, interval time.Duration) error {
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		addrs, err := net.DefaultResolver.LookupHost(ctx, fqdn)
		if err == nil {
			for _, a := range addrs {
				if a == ip {
					return nil
				}
			}
		}
		if attempt < maxAttempts {
			time.Sleep(interval)
		}
	}
	return fmt.Errorf("DNS 未生效（%d 次查询均未返回 %s）", maxAttempts, ip)
}

func StartMTADeploy(ctx context.Context, vpsIDs []string, opts DeployOpts, persona *PersonaSpec, onLog LogCallback) error {
	if len(vpsIDs) == 0 {
		return fmt.Errorf("至少选择一个 VPS")
	}
	if onLog == nil {
		onLog = func(string, int, string, string) {}
	}
	db := store.DB()
	if db == nil {
		return fmt.Errorf("数据库未就绪")
	}
	// aliyun client 缓存（并发共享，加锁）
	aliyunCache := map[string]*dns.AliyunDns{}
	var aliMu sync.Mutex
	getAli := func(credID string) *dns.AliyunDns {
		if credID == "" {
			return nil
		}
		aliMu.Lock()
		defer aliMu.Unlock()
		if c, ok := aliyunCache[credID]; ok {
			return c
		}
		c, err := loadAliyunDns(credID)
		if err != nil {
			return nil
		}
		aliyunCache[credID] = c
		return c
	}
	// 并发部署：10 个并发槽位
	concurrency := len(vpsIDs)
	if concurrency > 10 {
		concurrency = 10
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, vpsID := range vpsIDs {
		slot := i + 1
		id := vpsID
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					onLog("mta", slot, "ERROR", fmt.Sprintf("部署 panic: %v", r))
				}
			}()
			log := func(level, format string, args ...interface{}) {
				onLog("mta", slot, level, fmt.Sprintf(format, args...))
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
			var ip, fqdn, rootPwd, domain, aliyunCredID string
			row := db.QueryRowContext(ctx, `SELECT ip, fqdn, root_password, domain, aliyun_cred_id FROM vps_instances WHERE id=?`, id)
			if err := row.Scan(&ip, &fqdn, &rootPwd, &domain, &aliyunCredID); err != nil {
				log("ERROR", "读取 VPS 失败: %v", err)
				return
			}
			if ip == "" || fqdn == "" || domain == "" {
				log("ERROR", "VPS %s 缺少 ip/fqdn/domain", id)
				return
			}
			subdomain := SubdomainFromFQDN(fqdn, domain)
			aliyunDNS := getAli(aliyunCredID)
			_, _ = db.Exec(`UPDATE vps_instances SET deploy_status='mta_deploying', persona_id=?, hide_client_ip=? WHERE id=?`,
				opts.PersonaID, boolToInt(opts.HideClientIP), id)
			if err := deployMTAOnVPS(ctx, id, ip, fqdn, subdomain, domain, rootPwd, opts, persona, aliyunDNS, aliyunCredID, log); err != nil {
				_, _ = db.Exec(`UPDATE vps_instances SET deploy_status='failed', deploy_error=? WHERE id=?`, err.Error(), id)
				return
			}
			_, _ = db.Exec(`UPDATE vps_instances SET deploy_status='mta_ready', deploy_error='' WHERE id=?`, id)
			log("INFO", "KumoMTA 部署完成: %s", fqdn)
		}()
	}
	wg.Wait()
	return nil
}

func StartMTADeployTask(ctx context.Context, vpsIDs []string, opts DeployOpts, persona *PersonaSpec, onLog LogCallback) (string, error) {
	if len(vpsIDs) == 0 {
		return "", fmt.Errorf("至少选择一个 VPS")
	}
	if onLog == nil {
		onLog = func(string, int, string, string) {}
	}
	db := store.DB()
	if db == nil {
		return "", fmt.Errorf("数据库未就绪")
	}
	reqJSON, _ := json.Marshal(map[string]interface{}{"vps_ids": vpsIDs, "opts": opts})
	return startCancelableStageTask(ctx, db, "stage-d-running", len(vpsIDs), string(reqJSON), func(runCtx context.Context, taskID string) error {
		return StartMTADeploy(runCtx, vpsIDs, opts, persona, func(_ string, slot int, level, msg string) {
			onLog(taskID, slot, level, msg)
		})
	})
}

func deployMTAOnVPS(ctx context.Context, vpsID, ip, fqdn, subdomain, domain, rootPwd string, opts DeployOpts, persona *PersonaSpec, aliyunDNS *dns.AliyunDns, aliyunCredID string, log func(level, format string, args ...interface{})) error {
	db := store.DB()
	if db == nil {
		return fmt.Errorf("数据库未就绪")
	}
	_ = rootPwd
	// 读 VPS 的 deploy_type，决定走 KumoMTA（纯发信）还是 mailcow（收发一体）
	var deployType string
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(deploy_type,'kumomta') FROM vps_instances WHERE id=?`, vpsID).Scan(&deployType)
	if deployType == "" {
		deployType = "kumomta"
	}
	if deployType == "mailcow" {
		log("INFO", "部署类型=mailcow（收发一体邮件服务器）")
		return deployMailcowOnVPS(ctx, db, vpsID, ip, fqdn, subdomain, domain, aliyunDNS, aliyunCredID, log)
	}
	log("INFO", "部署类型=kumomta（纯发信 MTA）")

	sshCfg := ssh.Config{Host: ip, Port: 22, Username: "root", KeyContent: string(sshkey.PrivatePEM())}
	v := BuildDeployVars(domain, subdomain, ip)
	v.HideClientIP = opts.HideClientIP
	if persona != nil {
		v.PersonaReceivedTemplate = persona.ReceivedTemplate
		v.PersonaUserAgent = persona.UserAgent
		v.PersonaXMailer = persona.XMailer
		v.PersonaExtraHeadersLuaTable = BuildPersonaExtraHeadersLua(persona.ExtraHeaders)
	}
	installScript, err := RenderInstallKumoMTA(v)
	if err != nil {
		return err
	}
	if _, err := ssh.RunScript(ctx, sshCfg, installScript, nil); err != nil {
		return fmt.Errorf("install_kumomta 失败: %w", err)
	}
	dkimScript, err := RenderDkimSetup(v)
	if err != nil {
		return err
	}
	out, err := ssh.RunScript(ctx, sshCfg, dkimScript, nil)
	if err != nil {
		return fmt.Errorf("dkim_setup 失败: %w", err)
	}
	dkimPub := extractDKIMPublicKey(out)
	if dkimPub == "" {
		return fmt.Errorf("DKIM 公钥提取失败")
	}
	initLua, err := RenderInitLua(v)
	if err != nil {
		return err
	}
	authLua, err := RenderSmtpAuthLua(v)
	if err != nil {
		return err
	}
	deployCfgScript, err := GetDeployConfigScript()
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf(`cat > /tmp/deploy_config.sh <<'EOFDEPLOY'
%s
EOFDEPLOY
chmod +x /tmp/deploy_config.sh
/tmp/deploy_config.sh %q %q`, deployCfgScript, base64.StdEncoding.EncodeToString([]byte(initLua)), base64.StdEncoding.EncodeToString([]byte(authLua)))
	if _, err := ssh.RunCommand(ctx, sshCfg, cmd); err != nil {
		return fmt.Errorf("deploy_config 失败: %w", err)
	}
	_, _ = db.ExecContext(ctx,
		`UPDATE vps_instances SET dkim_public_key=?, smtp_account=?, smtp_password=? WHERE id=?`,
		dkimPub, v.Username, v.Password, vpsID)

	if aliyunDNS != nil {
		rrs := DNSRRsForSubdomain(subdomain)
		mxPriority := 10
		records := []dns.DnsRecordSpec{
			{RR: rrs.DKIM, RecordType: "TXT", Value: fmt.Sprintf("v=DKIM1; k=rsa; p=%s", dkimPub)},
			{RR: rrs.MX, RecordType: "MX", Value: fqdn, Priority: &mxPriority},
			{RR: rrs.SPF, RecordType: "TXT", Value: fmt.Sprintf("v=spf1 ip4:%s -all", ip)},
			{RR: rrs.DMARC, RecordType: "TXT", Value: fmt.Sprintf("v=DMARC1; p=reject; rua=mailto:dmarc@%s", domain)},
		}
		for _, spec := range records {
			if err := upsertAliyunRecordAndSyncLocal(ctx, db, aliyunDNS, aliyunCredID, domain, vpsID, spec, log); err != nil {
				log("WARN", "UpsertRecord %s/%s 失败: %v", spec.RR, spec.RecordType, err)
			}
		}
	}
	return nil
}

// deployMailcowOnVPS 部署 mailcow dockerized（收发一体，IMAP/SMTP）
func deployMailcowOnVPS(ctx context.Context, db *sql.DB, vpsID, ip, fqdn, subdomain, domain string, aliyunDNS *dns.AliyunDns, aliyunCredID string, log func(level, format string, args ...interface{})) error {
	sshCfg := ssh.Config{Host: ip, Port: 22, Username: "root", KeyContent: string(sshkey.PrivatePEM())}
	v := BuildDeployVars(domain, subdomain, ip)

	log("INFO", "开始安装 mailcow（Docker + Postfix + Dovecot + Rspamd）")
	installScript, err := RenderInstallMailcow(v)
	if err != nil {
		return err
	}
	// mailcow 安装时间长（~5-10 分钟），用 RunScript 流式输出
	if _, err := ssh.RunScript(ctx, sshCfg, installScript, func(stream, line string) {
		if line != "" {
			log("INFO", "[%s] %s", stream, line)
		}
	}); err != nil {
		return fmt.Errorf("install_mailcow 失败: %w", err)
	}

	// mailcow 自带 DKIM，通过 API 导出（但首次部署 API key 还没生成用户可用的，先跳过 DKIM 回填）
	// 如果需要 DKIM 公钥，用户可到 mailcow 管理面板 Configuration → ARC/DKIM keys 导出后手动填
	_, _ = db.ExecContext(ctx,
		`UPDATE vps_instances SET smtp_account=?, smtp_password=? WHERE id=?`,
		v.Username, v.Password, vpsID)

	if aliyunDNS != nil {
		rrs := DNSRRsForSubdomain(subdomain)
		mxPriority := 10
		// mailcow 自动管 SPF/DKIM/DMARC，但阿里云 DNS 需要我们设置根域到 fqdn 的路由
		// SPF: 指向 fqdn 的 IP（mailcow 只用一台 VPS 发信）
		// MX: fqdn
		// DMARC: p=reject aligns with KumoMTA/mail-toolkit defaults.
		// DKIM 暂不在此处建（等用户从 mailcow 面板导出后手动或通过 API 回填）
		records := []dns.DnsRecordSpec{
			{RR: rrs.MX, RecordType: "MX", Value: fqdn, Priority: &mxPriority},
			{RR: rrs.SPF, RecordType: "TXT", Value: fmt.Sprintf("v=spf1 ip4:%s -all", ip)},
			{RR: rrs.DMARC, RecordType: "TXT", Value: fmt.Sprintf("v=DMARC1; p=reject; rua=mailto:dmarc@%s", domain)},
			// autodiscover / autoconfig 让邮件大师等客户端自动配置
			{RR: "autodiscover", RecordType: "CNAME", Value: fqdn},
			{RR: "autoconfig", RecordType: "CNAME", Value: fqdn},
		}
		for _, spec := range records {
			if err := upsertAliyunRecordAndSyncLocal(ctx, db, aliyunDNS, aliyunCredID, domain, vpsID, spec, log); err != nil {
				log("WARN", "UpsertRecord %s/%s 失败: %v", spec.RR, spec.RecordType, err)
			}
		}
		log("INFO", "⚠ DKIM 公钥由 mailcow 自动生成，请登录 https://%s/ (admin/moohoo) → Configuration → ARC/DKIM keys 导出后手动加到阿里云 DNS", fqdn)
	}

	log("INFO", "✅ mailcow 部署完成。Web 管理: https://%s/ | 邮箱: %s | 密码: %s | IMAP 993 SSL | SMTP 587 STARTTLS", fqdn, v.Username, v.Password)
	return nil
}

func startCancelableStageTask(ctx context.Context, db *sql.DB, status string, total int, requestJSON string, run func(context.Context, string) error) (string, error) {
	taskID := uuid.NewString()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO batch_tasks (id, request_json, status, total) VALUES (?,?,?,?)`,
		taskID, requestJSON, status, total); err != nil {
		return "", fmt.Errorf("写入 batch_tasks 失败: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	state := &batchState{id: taskID, total: total, status: status, cancel: cancel}
	runningBatches.Store(taskID, state)
	go func() {
		defer finishStageTask(db, taskID, state, strings.TrimSuffix(status, "-running")+"-done")
		if err := run(runCtx, taskID); err != nil {
			atomic.AddInt64(&state.failed, int64(total))
			return
		}
		if state.status != "cancelling" {
			atomic.AddInt64(&state.succeeded, int64(total))
		}
	}()
	return taskID, nil
}

func finishStageTask(db *sql.DB, taskID string, state *batchState, doneStatus string) {
	finalStatus := doneStatus
	state.mu.Lock()
	if state.status == "cancelling" {
		finalStatus = "cancelled"
	} else if state.total > 0 && int(atomic.LoadInt64(&state.failed)) >= state.total {
		finalStatus = "failed"
	}
	state.status = finalStatus
	state.mu.Unlock()
	_, _ = db.Exec(`UPDATE batch_tasks SET status=?, succeeded=?, failed=?, finished_at=CURRENT_TIMESTAMP WHERE id=?`,
		finalStatus, int(atomic.LoadInt64(&state.succeeded)), int(atomic.LoadInt64(&state.failed)), taskID)
	time.AfterFunc(30*time.Minute, func() { runningBatches.Delete(taskID) })
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
