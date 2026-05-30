package deploy

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	Count           int      `json:"count"` // = 服务器数 × NICCount
	Regions         []string `json:"regions"`
	DNSBLThreshold  int      `json:"dnsbl_threshold"`
	MaxRetryPerSlot int      `json:"max_retry_per_slot"`
	IPPrefixFilter  []string `json:"ip_prefix_filter"`
	IPPrefixExclude []string `json:"ip_prefix_exclude"`
	// v0.1.57：每台 VPS 的 NIC 数（=每台绑几个静态 IP）。0/1=单 NIC 模式
	// 来自 vps_templates.nic_count；前端始终从模板读后透传
	NICCount int `json:"nic_count"`
	// v0.2.4：跳过 DNSBL 检测，只做 IP 前缀过滤。
	// 用户场景：主流邮箱不查 UCEPROTECT/SORBS-DUL/SpamRATS-Dyna 等噪声列表，
	// 全栈检查会把对实际发件无影响的 IP 误判为脏。开此开关后仅前缀过滤，速度大幅提升。
	SkipDNSBL bool `json:"skip_dnsbl"`
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
	// v0.2.9：第三步当场选的部署方式，覆盖模板默认。空=跟随各 VPS 模板的 deploy_type；
	// "kumomta"/"postfix"=本批统一用该方式（postfix/mailcow 仅单 NIC，多 NIC 自动回退 kumomta）。
	DeployType string `json:"deploy_type"`
}

type DeployOpts struct {
	HideClientIP bool   `json:"hide_client_ip"`
	PersonaID    string `json:"persona_id"`
	DeployType   string `json:"deploy_type"` // v0.2.9：空=跟随 VPS 模板；非空覆盖（postfix/mailcow 多 NIC 回退 kumomta）
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
	if _, err := loadTemplate(req.TemplateID); err != nil {
		return "", err
	}
	// v0.1.57：所有 mail-node 业务统一锁东京（asia-northeast1）。
	// 静态 IP 是 region 绑定的，多 NIC 模式所有 IP 必须同 region；
	// 普通单 NIC 模式也跟着锁定，避免轮转漂移到大阪。
	regions := []string{"asia-northeast1"}
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

	// v0.2.3：继承之前批次未使用的 clean+unbound IP，并入当前 batch。
	// 用户痛点：第一次筛 20 个只拿到 7 个，重新启动会让那 7 个从界面"消失"——其实只是被
	// 旧 batch_id 隔离了。把它们 reparent 到新 batch 后，剩下只需要补 (Count - 继承数) 个。
	// 同时把 state.succeeded 预填为继承数量，让"目标达到"判断和 UI 计数正确。
	// 多 NIC 的分组（slot_group）会在 Stage A 结束时由 groupCleanIPs 按当前 NICCount 重算，无需在这里处理。
	// v0.2.9：限定只过继本次 GCP 凭据下的 clean IP。静态 IP 与账号绑定，把别的账号
	// 预留的 IP 过继到本 batch 既用不了，又会把另一个并发批次的 IP 抢走（审计发现）。
	reparentArgs := []interface{}{batchID, batchID}
	credIn := ""
	if len(req.GCPCredIDs) > 0 {
		credIn = " AND gcp_cred_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(req.GCPCredIDs)), ",") + ")"
		for _, c := range req.GCPCredIDs {
			reparentArgs = append(reparentArgs, c)
		}
	}
	if res, err := db.ExecContext(ctx,
		`UPDATE static_ips SET batch_id=?, slot_group='' WHERE batch_id<>? AND status='clean' AND bound_instance_id=''`+credIn,
		reparentArgs...); err == nil {
		if inherited, _ := res.RowsAffected(); inherited > 0 {
			// v0.2.9：clamp 到 req.Count。继承数可能超过本次目标（上次筛多了），succeeded 不能
			// 虚高于 total，否则 UI 进度 >100% 且 claimSlot 的"达到 N 即停"语义错乱。
			preset := inherited
			if preset > int64(req.Count) {
				preset = int64(req.Count)
			}
			atomic.StoreInt64(&state.succeeded, preset)
			remaining := int64(req.Count) - inherited
			if remaining < 0 {
				remaining = 0
			}
			onLog(batchID, 0, "INFO",
				fmt.Sprintf("继承本账号上次未用的 clean IP %d 个，目标 %d，本次仍需筛选 %d 个",
					inherited, req.Count, remaining))
		}
	}

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
		// v0.1.58：脏 IP 批量释放——攒够 8 个一次性 release，避免 reserve→release→reserve 抽到同批脏 IP
		dirty := newDirtyIPHolder()
		// 目标驱动模型（v0.1.20）：并发 worker 持续抽，累计 succeeded 到目标 req.Count 就退。
		// 单次尝试失败（前缀/黑名单/DNSBL/预留 err）不占用成功计数，只累计 totalAttempts。
		// 全局 totalAttempts 上限 = req.Count * req.MaxRetryPerSlot，防止无限循环烧钱。
		// v0.1.61：并发上限 10→20。瓶颈是 DNSBL 26 个 RBL DNS 查询（~25s/IP），
		// 加并发能让多 IP 的 DNSBL 查询并行；本地 DNS 服务器节流通常在 50+ 才出现。
		concurrency := req.Count
		if concurrency > 20 {
			concurrency = 20
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
				// v0.2.2：region 不再永久 exhausted；每次只是 60s 软冷却。
				// 所有 region 都在冷却 → sleep 到最早一个解冻再继续，而不是退出 worker。
				// 用户语义："企业账号无配额限制，请一直筛选直到达到目标 N。"
				nextWakeup := time.Time{}
				now := time.Now()
				allCooling := true
				for _, r := range regions {
					if v, blown := exhausted.Load(r); blown {
						until := v.(time.Time)
						if now.After(until) {
							exhausted.Delete(r) // 冷却到期 → 解冻
							allCooling = false
							break
						}
						if nextWakeup.IsZero() || until.Before(nextWakeup) {
							nextWakeup = until
						}
					} else {
						allCooling = false
						break
					}
				}
				if allCooling {
					sleep := time.Until(nextWakeup) + time.Second
					if sleep > 0 {
						select {
						case <-runCtx.Done():
							return
						case <-time.After(sleep):
						}
					}
					continue
				}

				attempt := atomic.LoadInt64(&totalAttempts)
				gcpCredID := req.GCPCredIDs[int(attempt-1)%len(req.GCPCredIDs)]
				region := regions[int(attempt-1)%len(regions)]

				if v, blown := exhausted.Load(region); blown {
					until := v.(time.Time)
					if now.After(until) {
						exhausted.Delete(region)
					} else {
						continue
					}
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
					req.DNSBLThreshold, req.IPPrefixFilter, req.IPPrefixExclude, req.SkipDNSBL,
					&exhausted, cli, onLog, claimSlot, dirty); err != nil {
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

		// v0.1.58：清扫剩余 hold 中的脏 IP（不足 holdThreshold 的尾巴）
		dirty.Drain(runCtx, db, func(level, format string, args ...interface{}) {
			onLog(batchID, 0, level, fmt.Sprintf(format, args...))
		})

		// v0.1.57：post-grouping 把 clean IP 按 nic_count 分组（多 NIC 模式必须）。
		// 单 NIC 模式（NICCount<=1）也调用，把每个 IP 自成一组（slot_group=自身 ID），
		// 让 Stage B 统一按 group 拉取 IP，不再分两条路径。
		if groups, gerr := groupCleanIPs(db, batchID, req.NICCount); gerr != nil {
			onLog(batchID, 0, "WARN", fmt.Sprintf("post-grouping 失败: %v", gerr))
		} else if req.NICCount > 1 && groups > 0 {
			onLog(batchID, 0, "INFO", fmt.Sprintf("post-grouping: %d 组 × %d NIC = %d clean IP 已分配", groups, req.NICCount, groups*req.NICCount))
		}

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
func reserveAndFilterOnce(ctx context.Context, batchID string, workerID int, gcpCredID, region string, threshold int, prefixFilter, prefixExclude []string, skipDNSBL bool, exhausted *sync.Map, gcpClient *gcp.Client, onLog LogCallback, claimSlot func() bool, dirty *dirtyIPHolder) error {
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
			// v0.1.60：QUOTA_EXCEEDED 时先 flush dirty holder 让出配额，再次尝试。
			// 老逻辑直接 exhausted 整个 region，会导致 v0.1.59 hold 8 个脏 IP 占满配额时
			// worker 全部退出（实际只是配额被脏 IP 临时占满，不是真的没配额）。
			if dirty != nil {
				if flushed := dirty.Flush(ctx, db, log); flushed > 0 {
					log("INFO", "已释放 %d 个脏 IP，重试 reserve", flushed)
					// GCP IP 释放有几秒延迟（API eventual consistency），等一下再交还给 worker 主循环重试
					time.Sleep(3 * time.Second)
					return fmt.Errorf("quota_temp_full_retry")
				}
			}
			// v0.2.2：不再永久 exhausted —— 改为 60s 软冷却（cooldown），到期 worker 自动重试。
			// 企业账号场景下 quota_exceeded 通常是临时（其他批次正在用、刚释放未真正回池）；
			// 永久退出会让用户感觉"还没到目标软件就停了"。
			if exhausted != nil {
				exhausted.Store(region, time.Now().Add(60*time.Second))
			}
			log("WARN", "region %s 静态 IP 配额暂满（flush 后仍满），冷却 60s 后自动重试", region)
			return fmt.Errorf("region %s QUOTA_EXCEEDED_COOLDOWN", region)
		}
		log("ERROR", "预留静态 IP 失败: %v", err)
		return err
	}
	ipID := uuid.NewString()
	// v0.2.9：IP 已在 GCP 端 reserve 成功，后续"落库 + 释放"必须脱离 runCtx 取消——否则用户
	// 取消 Stage A 时：落库失败会留下无本地记录的 GCP 孤儿 IP；release 被取消打断则 IP 漏释放。
	// 两种都让静态 IP 持续计费。persistCtx 用 background 保证这些善后操作一定执行。
	persistCtx := context.Background()
	_, _ = db.ExecContext(persistCtx,
		`INSERT INTO static_ips (id, gcp_cred_id, gcp_address_name, ip, region, status, batch_id) VALUES (?,?,?,?,?,?,?)`,
		ipID, gcpCredID, addr.Name, addr.IP, region, "reserved", batchID)

	// release 立即释放（用于 target_reached / DNSBL 检测错误等不算"脏"的情形）
	release := func(status, result, hitLists string) {
		_, _ = db.ExecContext(persistCtx,
			`UPDATE static_ips SET status=?, dnsbl_result=?, dnsbl_hit_lists=?, bound_instance_id='' WHERE id=?`,
			status, result, hitLists, ipID)
		_ = gcpClient.ReleaseStaticAddress(persistCtx, region, addr.Name)
	}
	// holdDirty 暂存脏 IP（前缀/黑段/DNSBL 判脏），攒够 dirtyIPHolder.holdThreshold 个一次释放。
	// 目的：避免 release-then-reserve 立刻抽到同一批脏 IP；让 GCP 把池子真正翻一遍。
	holdDirty := func(result, hitLists string) {
		_, _ = db.ExecContext(persistCtx,
			`UPDATE static_ips SET status='dirty', dnsbl_result=?, dnsbl_hit_lists=?, bound_instance_id='' WHERE id=?`,
			result, hitLists, ipID)
		if dirty != nil {
			dirty.Add(dirtyIPRow{
				ipID:      ipID,
				addrName:  addr.Name,
				gcpCredID: gcpCredID,
				region:    region,
				gcpClient: gcpClient,
				reserveAt: time.Now(),
			}, persistCtx, db, log)
		} else {
			// holder 为 nil（兼容路径）：回退到立即释放
			release("released", result, hitLists)
		}
	}
	if !matchesPrefix(addr.IP) {
		log("WARN", "IP %s 不匹配前缀白名单 %v，暂存脏 IP", addr.IP, prefixFilter)
		holdDirty("prefix_mismatch", "")
		return fmt.Errorf("prefix_mismatch")
	}
	if hit := matchesExclude(addr.IP); hit != "" {
		log("WARN", "IP %s 命中前缀黑名单 %s，暂存脏 IP", addr.IP, hit)
		holdDirty("prefix_excluded", hit)
		return fmt.Errorf("prefix_excluded")
	}
	// v0.2.4：用户开"跳过 DNSBL"时直接走 claimSlot 标 clean，
	// 不再查历史 / 不查黑段 / 不发 DNSBL 请求。只受前缀过滤约束。
	if skipDNSBL {
		if claimSlot != nil && !claimSlot() {
			log("INFO", "IP %s (跳过 DNSBL) 但已达目标 N，释放", addr.IP)
			release("released", "target_reached", "")
			return fmt.Errorf("target_reached")
		}
		log("INFO", "IP %s 通过（跳过 DNSBL，仅前缀过滤）", addr.IP)
		_, _ = db.ExecContext(ctx,
			`UPDATE static_ips SET status='clean', dnsbl_result='skipped' WHERE id=?`, ipID)
		return nil
	}

	// v0.2.2：跨批次记忆——同一 IP 之前被判脏过（含 prefix_excluded/blacklisted/dnsbl 任何脏 verdict），
	// 直接 hold-dirty 跳过 DNSBL，省 ~5s 网络往返。
	// 排除自身这次刚插的 reserved 行（ipID）。
	var pastVerdict, pastHits string
	_ = db.QueryRowContext(ctx,
		`SELECT dnsbl_result, COALESCE(dnsbl_hit_lists,'') FROM static_ips
		 WHERE ip=? AND id<>? AND status IN ('dirty','released')
		 AND dnsbl_result IS NOT NULL AND dnsbl_result<>'' AND dnsbl_result<>'dnsbl_error'
		 ORDER BY rowid DESC LIMIT 1`,
		addr.IP, ipID).Scan(&pastVerdict, &pastHits)
	if pastVerdict != "" && pastVerdict != "clean" {
		log("WARN", "IP %s 历史已判脏 (%s)，跳过 DNSBL 直接暂存", addr.IP, pastVerdict)
		holdDirty(pastVerdict, pastHits)
		return fmt.Errorf("known_dirty:%s", pastVerdict)
	}
	if seg, note, berr := dnsbl.ContainsIP(ctx, addr.IP); berr == nil && seg != "" {
		log("WARN", "IP %s 命中黑段 %s(%s)，暂存脏 IP", addr.IP, seg, note)
		holdDirty("blacklisted", "blackseg:"+seg)
		return fmt.Errorf("blacklisted")
	}

	verdict, reason, detail, derr := dnsbl.Decide(ctx, addr.IP, dnsbl.CheckOptions{Threshold: threshold}, 6*time.Hour)
	hitLists := ""
	if detail != nil {
		hitLists = strings.Join(detail.HitLists, ",")
	}
	_, _ = db.ExecContext(ctx, `UPDATE static_ips SET dnsbl_result=?, dnsbl_hit_lists=? WHERE id=?`, verdict, hitLists, ipID)
	if derr != nil {
		// DNSBL 检测错误不是 IP 脏，是网络/服务问题——立即释放，下次重试可能就过了
		log("WARN", "DNSBL 检测失败/不确定: %v，立即释放 IP 重试", derr)
		release("released", "dnsbl_error", hitLists)
		return derr
	}
	if verdict == "clean" {
		// CAS 抢占槽位：抢到才保留并标 clean；抢不到（已达目标 N）则释放（不是脏）
		if claimSlot != nil && !claimSlot() {
			log("INFO", "IP %s 判定 clean 但已达目标 N，释放", addr.IP)
			release("released", "target_reached", "")
			return fmt.Errorf("target_reached")
		}
		log("INFO", "IP %s 检测通过 (%s)", addr.IP, reason)
		_, _ = db.ExecContext(ctx, `UPDATE static_ips SET status='clean' WHERE id=?`, ipID)
		return nil
	}
	log("WARN", "IP %s 判定: %s (%s)，暂存脏 IP", addr.IP, verdict, reason)
	holdDirty(verdict, hitLists)
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
	// v0.1.57：按 slot_group 拉取（单 NIC 模式：每个 IP 自成一组；多 NIC 模式：每组 nic_count 个 IP）
	// post-grouping 已在 Stage A 末尾跑过；这里直接查所有 group。
	groups, err := loadSlotGroups(ctx, db, batchID)
	if err != nil {
		return fmt.Errorf("查询 slot_group 失败: %w", err)
	}
	if len(groups) == 0 {
		// 兼容 v0.1.56 之前批次（slot_group 列还没填）：自动 grouping 一次
		if _, gerr := groupCleanIPs(db, batchID, tmpl.NICCount); gerr == nil {
			groups, _ = loadSlotGroups(ctx, db, batchID)
		}
	}
	if len(groups) == 0 {
		return fmt.Errorf("该批次没有 clean IP，是否先跑阶段 A？")
	}

	// v0.1.57：校验每个 group 的 IP 数 = 模板 NICCount，避免半组导致 NIC<NICCount 的实例
	expectedNICs := tmpl.NICCount
	if expectedNICs <= 0 {
		expectedNICs = 1
	}
	for _, g := range groups {
		if len(g.ips) != expectedNICs {
			return fmt.Errorf("slot_group %s 大小=%d 但模板 NICCount=%d；请回 Step 1 整组保留或换匹配的模板",
				g.slotGroup[:min(8, len(g.slotGroup))], len(g.ips), expectedNICs)
		}
	}
	// 多 NIC 模板必须走 KumoMTA（mailcow / postfix 不支持多 NIC 部署链路）
	if expectedNICs > 1 && tmpl.DeployType != "" && tmpl.DeployType != "kumomta" {
		return fmt.Errorf("多 NIC 模板（NICCount=%d）只支持 KumoMTA 部署，当前模板 deploy_type=%s（postfix/mailcow 均为单 NIC）",
			expectedNICs, tmpl.DeployType)
	}

	totalVPS := len(groups)
	runCtx, cancel := context.WithCancel(ctx)
	stRaw, ok := runningBatches.Load(batchID)
	var state *batchState
	if ok {
		state = stRaw.(*batchState)
		state.mu.Lock()
		state.status = "stage-b-running"
		state.total = totalVPS
		state.cancel = cancel
		atomic.StoreInt64(&state.succeeded, 0)
		atomic.StoreInt64(&state.failed, 0)
		state.mu.Unlock()
	} else {
		state = &batchState{id: batchID, total: totalVPS, status: "stage-b-running", cancel: cancel}
		runningBatches.Store(batchID, state)
	}
	_, _ = db.Exec(`UPDATE batch_tasks SET status='stage-b-running', finished_at=NULL WHERE id=?`, batchID)

	go func() {
		defer func() {
			got := int(atomic.LoadInt64(&state.succeeded))
			failed := int(atomic.LoadInt64(&state.failed))
			if failed > 0 {
				onLog(batchID, 0, "WARN", fmt.Sprintf("====== 阶段 B 完成：成功 %d / 失败 %d（共 %d 台）======", got, failed, totalVPS))
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
				if err := cli.EnsureMailNodeFirewall(runCtx, "default"); err != nil {
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

		// v0.1.61：Stage B 并发 10→20。GCP 单 zone 同时创建实例上限 ~24-32，
		// 20 留 buffer 避免触发节流；10 台 × 8 NIC 实例创建总耗时从 5-8 分钟降到 2-3 分钟。
		concurrency := totalVPS
		if concurrency > 20 {
			concurrency = 20
		}
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
	dispatchLoop:
		for i, g := range groups {
			select {
			case <-runCtx.Done():
				onLog(batchID, 0, "WARN", "阶段 B 已取消，停止派发剩余 slot")
				atomic.AddInt64(&state.failed, int64(totalVPS-i))
				break dispatchLoop
			default:
			}
			slot := i + 1
			sem <- struct{}{}
			wg.Add(1)
			go func(slot int, g slotGroupRow) {
				defer wg.Done()
				defer func() { <-sem }()
				cli, err := getGCP(g.gcpCredID)
				if err != nil {
					onLog(batchID, slot, "ERROR", fmt.Sprintf("获取 GCP 客户端失败: %v", err))
					atomic.AddInt64(&state.failed, 1)
					return
				}
				if err := createVPSOnly(runCtx, batchID, slot, g, req.RootPassword, tmpl, cli, onLog); err != nil {
					atomic.AddInt64(&state.failed, 1)
					return
				}
				atomic.AddInt64(&state.succeeded, 1)
			}(slot, g)
		}
		wg.Wait()
	}()
	return nil
}

// slotGroupRow Stage B 派发单元：一个 group 对应一台 VPS（单 NIC 时 1 个 IP，多 NIC 时 N 个）。
type slotGroupRow struct {
	slotGroup string
	gcpCredID string
	region    string
	ips       []groupIP // ORDER BY nic_index
}

type groupIP struct {
	ipID     string
	addrName string
	ip       string
	nicIndex int
}

// loadSlotGroups 查询 batch 下所有 status='clean' 且未绑定的 IP，按 slot_group 聚合。
// 同 group 的 gcp_cred_id 与 region 必须一致（Stage A worker 决定的，分组只按 batch 级别）；
// 这里取 group 内任一 IP 的 gcp_cred_id/region 作为代表。
func loadSlotGroups(ctx context.Context, db *sql.DB, batchID string) ([]slotGroupRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, gcp_cred_id, gcp_address_name, ip, region, COALESCE(slot_group,''), COALESCE(nic_index,0)
		   FROM static_ips
		  WHERE batch_id=? AND status='clean' AND bound_instance_id=''
		    AND COALESCE(slot_group,'')<>''
		  ORDER BY slot_group ASC, nic_index ASC`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groupMap := map[string]*slotGroupRow{}
	var ordered []*slotGroupRow
	for rows.Next() {
		var ipID, credID, addrName, ip, region, sg string
		var nic int
		if err := rows.Scan(&ipID, &credID, &addrName, &ip, &region, &sg, &nic); err != nil {
			return nil, err
		}
		g, ok := groupMap[sg]
		if !ok {
			g = &slotGroupRow{slotGroup: sg, gcpCredID: credID, region: region}
			groupMap[sg] = g
			ordered = append(ordered, g)
		}
		g.ips = append(g.ips, groupIP{ipID: ipID, addrName: addrName, ip: ip, nicIndex: nic})
	}
	out := make([]slotGroupRow, 0, len(ordered))
	for _, g := range ordered {
		out = append(out, *g)
	}
	return out, rows.Err()
}

func createVPSOnly(ctx context.Context, batchID string, slot int, group slotGroupRow, rootPassword string, tmpl VPSTemplate, gcpClient *gcp.Client, onLog LogCallback) error {
	log := func(level, format string, args ...interface{}) {
		onLog(batchID, slot, level, fmt.Sprintf(format, args...))
	}
	db := store.DB()
	if db == nil {
		return fmt.Errorf("数据库未就绪")
	}
	if len(group.ips) == 0 {
		return fmt.Errorf("slot_group 内无 IP")
	}
	primary := group.ips[0]
	gcpCredID := group.gcpCredID
	region := group.region
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

	// v0.1.57：多 NIC 模式预先创建 mail-vpc-1..N + 子网 + 防火墙
	var nicSpecs []gcp.NICSpec
	if len(group.ips) > 1 {
		vpcs, err := gcpClient.EnsureMailVPCs(ctx, region, len(group.ips))
		if err != nil {
			_ = releaseGroupIPs(ctx, db, gcpClient, region, group.ips)
			return fmt.Errorf("EnsureMailVPCs: %w", err)
		}
		if len(vpcs) < len(group.ips) {
			_ = releaseGroupIPs(ctx, db, gcpClient, region, group.ips)
			return fmt.Errorf("EnsureMailVPCs 返回 %d VPC < 期望 %d", len(vpcs), len(group.ips))
		}
		for i, ipRow := range group.ips {
			nicSpecs = append(nicSpecs, gcp.NICSpec{
				NetworkName: vpcs[i].NetworkName,
				SubnetURL:   vpcs[i].SubnetURL,
				StaticIP:    ipRow.ip,
			})
		}
		log("INFO", "多 NIC 模式：%d NIC × 静态 IP 已就绪", len(group.ips))
	}

	var zone string
	var createdInfo gcp.InstanceInfo
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
		createdInfo, createErr = gcpClient.CreateInstance(ctx, gcp.InstanceSpec{
			Name:              instanceName,
			Zone:              tryZone,
			MachineType:       machineType,
			ImageFamily:       imageFamily,
			ImageProject:      imageProject,
			DiskSizeGB:        diskSize,
			DiskType:          diskType,
			Tags:              mergeMailNodeTag(tmpl.Tags),
			StartupScript:     startupScript,
			StaticIP:          primary.ip, // 单 NIC 路径用；NICs 非空时忽略
			NetworkName:       "default",
			NICs:              nicSpecs, // 多 NIC：非空，每元素一个 NetworkInterface
			ProvisioningModel: tmpl.ProvisioningModel,
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
			if info, gerr := gcpClient.GetAddress(ctx, region, primary.addrName); gerr == nil && len(info.Users) > 0 {
				log("WARN", "zone %s 失败，IP %s 当前被占用: %v", tryZone, primary.ip, info.Users)
			} else {
				log("WARN", "zone %s 失败: %v（IP %s 查询 Users 为空，可能 GCP 元数据滞后）", tryZone, createErr, primary.ip)
			}
			continue
		}
		break
	}
	if createErr != nil {
		_ = releaseGroupIPs(ctx, db, gcpClient, region, group.ips)
		return createErr
	}

	vpsID := uuid.NewString()
	deployType := tmpl.DeployType
	if deployType == "" {
		deployType = "kumomta"
	}
	nicCount := len(group.ips)
	if nicCount < 1 {
		nicCount = 1
	}
	_, _ = db.ExecContext(ctx,
		`INSERT INTO vps_instances (id, gcp_cred_id, gcp_instance_id, name, region, zone, machine_type, status, ip, internal_ip, fqdn, root_password, deploy_status, batch_id, deploy_type, nic_count)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		vpsID, gcpCredID, instanceName, instanceName, region, zone, machineType, "pending", primary.ip, createdInfo.InternalIP, "", rootPassword, "vps_pending", batchID, deployType, nicCount)
	// v0.1.57：additional_ips_json 在 WaitForRunning 拿到每 NIC 内网 IP 之后再写入

	cleanup := func(reason error) {
		msg := reason.Error()
		log("WARN", "创建后的探测失败，清理 VM 和静态 IP: %v", reason)
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancelCleanup()
		if err := gcpClient.DeleteInstance(cleanupCtx, zone, instanceName); err != nil && !gcp.IsNotFound(err) {
			log("WARN", "删除 VM 失败，需要人工检查: %v", err)
		}
		for _, ipRow := range group.ips {
			if err := gcpClient.ReleaseStaticAddress(cleanupCtx, region, ipRow.addrName); err != nil && !gcp.IsNotFound(err) {
				log("WARN", "释放静态 IP %s 失败，需要人工检查: %v", ipRow.ip, err)
			}
		}
		_, _ = db.Exec(`UPDATE vps_instances SET status='deleted', deploy_status='failed', deploy_error=? WHERE id=?`, msg, vpsID)
		for _, ipRow := range group.ips {
			_, _ = db.Exec(`UPDATE static_ips SET status='released', bound_instance_id='' WHERE id=?`, ipRow.ipID)
		}
	}

	runningInfo, err := gcpClient.WaitForRunning(ctx, zone, instanceName, 5*time.Minute)
	if err != nil {
		cleanup(err)
		return err
	}
	if runningInfo.InternalIP != "" {
		createdInfo.InternalIP = runningInfo.InternalIP
	}
	if len(runningInfo.NICs) > len(createdInfo.NICs) {
		createdInfo.NICs = runningInfo.NICs
	}
	_, _ = db.ExecContext(ctx, `UPDATE vps_instances SET status='running', internal_ip=? WHERE id=?`, createdInfo.InternalIP, vpsID)
	// 整组 IP 全部 bound 到这台实例
	for _, ipRow := range group.ips {
		_, _ = db.ExecContext(ctx, `UPDATE static_ips SET status='in_use', bound_instance_id=? WHERE id=?`, vpsID, ipRow.ipID)
	}

	// v0.1.57：多 NIC 模式持久化所有 NIC 的内/外网 IP + EHLO 域名（mail{N+1}.<root>） 到 additional_ips_json
	// 写入时机：WaitForRunning 之后，因为这时 GCP 才返回每个 NIC 的内网 IP。
	// 后续 deployMTAOnVPS 读这个 JSON 拼 8 个 KumoMTA source_address；DNS 写 mail1~mail8 A 记录用同样的 IP。
	if len(group.ips) > 1 {
		extras := buildAdditionalIPsJSON(group.ips, createdInfo.NICs)
		js, _ := json.Marshal(extras)
		if _, err := db.ExecContext(ctx, `UPDATE vps_instances SET additional_ips_json=? WHERE id=?`, string(js), vpsID); err != nil {
			log("WARN", "写入 additional_ips_json 失败: %v", err)
		}
	}
	ip := primary.ip

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
	sshErr := fmt.Errorf("SSH 探测 10 次未成功")
	cleanup(sshErr)
	return sshErr
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
				var vpsID, rootPwd, internalIP string
				row := db.QueryRowContext(runCtx,
					`SELECT id, root_password, COALESCE(internal_ip,'') FROM vps_instances
					 WHERE ip=? AND deploy_status IN ('vps_running','ptr_ready')
					   AND status!='deleted'
					   ORDER BY created_at DESC LIMIT 1`, ip)
				if err := row.Scan(&vpsID, &rootPwd, &internalIP); err != nil {
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
				if err := deployMTAOnVPS(runCtx, vpsID, ip, internalIP, fqdn, "@", d, rootPwd, DeployOpts{HideClientIP: req.HideClientIP, PersonaID: req.PersonaID, DeployType: req.DeployType}, personaSpec, aliyunDNS, req.AliyunCredID, logC); err != nil {
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
			var gcpCredID, zone, instanceName, ip, fqdn, additionalIPsJSON, rootDomain string
			var nicCount int
			row := db.QueryRowContext(ctx,
				`SELECT gcp_cred_id, zone, name, ip, fqdn, COALESCE(nic_count,1), COALESCE(additional_ips_json,''), COALESCE(domain,'')
				   FROM vps_instances WHERE id=?`, id)
			if err := row.Scan(&gcpCredID, &zone, &instanceName, &ip, &fqdn, &nicCount, &additionalIPsJSON, &rootDomain); err != nil {
				log("ERROR", "读取 VPS 失败: %v", err)
				return
			}
			if ip == "" || fqdn == "" {
				log("ERROR", "VPS %s 缺少 ip 或 fqdn", id)
				return
			}
			if nicCount <= 0 {
				nicCount = 1
			}
			var addtlIPs []AdditionalIPEntry
			if additionalIPsJSON != "" && additionalIPsJSON != "[]" {
				_ = json.Unmarshal([]byte(additionalIPsJSON), &addtlIPs)
			}
			cli, err := getCli(gcpCredID)
			if err != nil {
				log("ERROR", "加载 GCP 客户端失败: %v", err)
				_, _ = db.Exec(`UPDATE vps_instances SET ptr_status='failed', deploy_error=? WHERE id=?`, err.Error(), id)
				return
			}
			_, _ = db.Exec(`UPDATE vps_instances SET ptr_status='pending' WHERE id=?`, id)
			// v0.1.74：批量重设 PTR 也走"所有 NIC 都试一遍"路径
			log("INFO", "设置 PTR nic0: %s -> %s", ip, fqdn)
			if err := cli.SetInstancePTRForNIC(ctx, zone, instanceName, 0, ip, fqdn); err != nil {
				summary := fmt.Sprintf("PTR 设置失败: nic0 (%s -> %s): %v", ip, fqdn, err)
				log("ERROR", summary)
				_, _ = db.Exec(`UPDATE vps_instances SET ptr_status='failed', deploy_error=? WHERE id=?`, summary, id)
				return
			}
			// 额外 NIC：mail{N+1}.<rootDomain> -> additional IP
			extraOK := 0
			extraFail := 0
			if rootDomain != "" {
				for _, e := range addtlIPs {
					nicFQDN := fmt.Sprintf("mail%d.%s", e.NICIndex+1, rootDomain)
					log("INFO", "设置 PTR nic%d: %s -> %s", e.NICIndex, e.IP, nicFQDN)
					if err := cli.SetInstancePTRForNIC(ctx, zone, instanceName, e.NICIndex, e.IP, nicFQDN); err != nil {
						log("WARN", "PTR nic%d 失败（不阻塞）: %v", e.NICIndex, err)
						extraFail++
						continue
					}
					extraOK++
				}
			}
			_, _ = db.Exec(`UPDATE vps_instances SET ptr_status='set', deploy_status='ptr_ready', deploy_error='' WHERE id=?`, id)
			log("INFO", "✅ PTR 完成: nic0 + %d/%d 额外 NIC（%d 失败）", extraOK, len(addtlIPs), extraFail)
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
			var ip, internalIP, fqdn, rootPwd, domain, aliyunCredID string
			row := db.QueryRowContext(ctx, `SELECT ip, COALESCE(internal_ip,''), fqdn, root_password, domain, aliyun_cred_id FROM vps_instances WHERE id=?`, id)
			if err := row.Scan(&ip, &internalIP, &fqdn, &rootPwd, &domain, &aliyunCredID); err != nil {
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
			if err := deployMTAOnVPS(ctx, id, ip, internalIP, fqdn, subdomain, domain, rootPwd, opts, persona, aliyunDNS, aliyunCredID, log); err != nil {
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

func deployMTAOnVPS(ctx context.Context, vpsID, ip, internalIP, fqdn, subdomain, domain, rootPwd string, opts DeployOpts, persona *PersonaSpec, aliyunDNS *dns.AliyunDns, aliyunCredID string, log func(level, format string, args ...interface{})) error {
	db := store.DB()
	if db == nil {
		return fmt.Errorf("数据库未就绪")
	}
	_ = rootPwd
	// 读 VPS 的 deploy_type，决定走 KumoMTA（纯发信）/ Postfix / mailcow（收发一体）
	var deployType string
	var ncOverride int
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(deploy_type,'kumomta'), COALESCE(nic_count,1) FROM vps_instances WHERE id=?`, vpsID).Scan(&deployType, &ncOverride)
	if deployType == "" {
		deployType = "kumomta"
	}
	// v0.2.9：第三步当场选的部署方式覆盖模板默认（前端可选 KumoMTA/Postfix）。
	// postfix/mailcow 仅支持单 NIC，多 NIC 自动回退 kumomta（与 Stage B 的限制一致）。
	if opts.DeployType != "" {
		if (opts.DeployType == "postfix" || opts.DeployType == "mailcow") && ncOverride > 1 {
			log("WARN", "多 NIC（nic_count=%d）只支持 KumoMTA，忽略部署方式覆盖=%s，仍用 kumomta", ncOverride, opts.DeployType)
			deployType = "kumomta"
		} else {
			deployType = opts.DeployType
			log("INFO", "部署方式由第三步指定覆盖为：%s", deployType)
		}
	}
	if deployType == "mailcow" {
		log("INFO", "部署类型=mailcow（收发一体邮件服务器）")
		return deployMailcowOnVPS(ctx, db, vpsID, ip, fqdn, subdomain, domain, aliyunDNS, aliyunCredID, log)
	}
	if deployType == "postfix" {
		log("INFO", "部署类型=postfix（Postfix + OpenDKIM 纯发信）")
		return deployPostfixOnVPS(ctx, db, vpsID, ip, fqdn, subdomain, domain, aliyunDNS, aliyunCredID, log)
	}
	log("INFO", "部署类型=kumomta（纯发信 MTA）")

	sshCfg := ssh.Config{Host: ip, Port: 22, Username: "root", KeyContent: string(sshkey.PrivatePEM())}
	internalIP = strings.TrimSpace(internalIP)
	if internalIP == "" {
		if detected, derr := detectRemoteInternalIP(ctx, sshCfg); derr == nil {
			internalIP = detected
			_, _ = db.ExecContext(ctx, `UPDATE vps_instances SET internal_ip=? WHERE id=?`, internalIP, vpsID)
			log("INFO", "检测到 VPS 内网 IP: %s", internalIP)
		} else {
			log("WARN", "检测内网 IP 失败，临时回退到外网 IP（KumoMTA source_address 可能失败）: %v", derr)
			internalIP = ip
		}
	}
	// v0.1.57：读 vps_instances 的 nic_count + additional_ips_json
	var nicCount int
	var additionalIPsJSON string
	_ = db.QueryRowContext(ctx,
		`SELECT COALESCE(nic_count,1), COALESCE(additional_ips_json,'') FROM vps_instances WHERE id=?`, vpsID,
	).Scan(&nicCount, &additionalIPsJSON)
	if nicCount <= 0 {
		nicCount = 1
	}
	var addtlIPs []AdditionalIPEntry
	if additionalIPsJSON != "" && additionalIPsJSON != "[]" {
		_ = json.Unmarshal([]byte(additionalIPsJSON), &addtlIPs)
	}
	allIPs := []string{ip}
	for _, e := range addtlIPs {
		allIPs = append(allIPs, e.IP)
	}
	if nicCount > 1 {
		prScript, perr := RenderPolicyRouting(nicCount)
		if perr != nil {
			return fmt.Errorf("RenderPolicyRouting: %w", perr)
		}
		if _, err := ssh.RunScript(ctx, sshCfg, prScript, func(stream, line string) {
			if line != "" {
				log("INFO", "[policy-routing/%s] %s", stream, line)
			}
		}); err != nil {
			return fmt.Errorf("setup_policy_routing 失败: %w", err)
		}
		log("INFO", "policy routing 配置完成（%d NICs）", nicCount)
	}

	// v0.1.57：多 NIC 时构造 8 个 SourceSpec（每 NIC 一个），KumoMTA init.lua 渲染 8 个 source 轮换
	// 单 NIC 时 sources 留空，BuildDeployVarsMultiNIC 内部退化成单 source 路径
	var sources []SourceSpec
	if nicCount > 1 {
		sources = append(sources, SourceSpec{
			Name: "ip0",
			IP:   internalIP,
			EHLO: fmt.Sprintf("mail1.%s", domain),
		})
		for _, e := range addtlIPs {
			bind := e.InternalIP
			if bind == "" {
				bind = e.IP // 兜底（理论上不该发生——v0.1.57 已经在 createVPSOnly 保证写了 internal_ip）
				log("WARN", "additional NIC nic_index=%d 缺 internal_ip，回退用外网 IP %s（KumoMTA bind 可能失败）", e.NICIndex, e.IP)
			}
			sources = append(sources, SourceSpec{
				Name: fmt.Sprintf("ip%d", e.NICIndex),
				IP:   bind,
				EHLO: fmt.Sprintf("mail%d.%s", e.NICIndex+1, domain),
			})
		}
		log("INFO", "多 NIC KumoMTA：%d 个 egress source 轮换发件", len(sources))
	}
	v := BuildDeployVarsMultiNIC(domain, subdomain, internalIP, sources)
	v.HideClientIP = opts.HideClientIP
	// Persona 伪造完全在 brutal-mailer 侧完成，KumoMTA 只做透明中继；
	// persona 参数保留是为了不破坏外部调用签名，这里不写入任何 persona 头。
	_ = persona
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
	// 流式回传 dkim_setup 的 stdout 到任务日志，便于失败时定位
	out, err := ssh.RunScript(ctx, sshCfg, dkimScript, func(stream, line string) {
		if line != "" {
			log("INFO", "[dkim/%s] %s", stream, line)
		}
	})
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
	if err := kumoMTASelfCheck(ctx, sshCfg, v); err != nil {
		return err
	}

	// v0.1.74：装退订服务（caddy + unsub-server + sqlite）
	// 失败不阻塞 KumoMTA 部署成功——退订是合规增强，缺失不影响发信
	// v0.1.78：先用 SSH stdin 流上传 unsub-server 二进制（9.8MB）到 /tmp/unsub-server，
	//          再跑 install_unsub.sh —— 避免脚本里 base64 内联二进制撞 SSH ARG_MAX (2MB) → EOF
	v.UnsubSecret = GenerateUnsubSecret()
	unsubBin := UnsubServerBinary()
	if len(unsubBin) == 0 {
		log("WARN", "unsub-server 二进制未嵌入（跳过退订服务，不影响发信）")
		v.UnsubSecret = ""
	} else {
		log("INFO", "上传 unsub-server 二进制（%d KB）到 /tmp/unsub-server", len(unsubBin)/1024)
		if err := ssh.UploadBytes(ctx, sshCfg, "/tmp/unsub-server", unsubBin); err != nil {
			log("WARN", "上传 unsub-server 失败（跳过退订服务，不影响发信）: %v", err)
			v.UnsubSecret = ""
		} else {
			unsubScript, err := RenderInstallUnsub(v)
			if err != nil {
				log("WARN", "渲染退订脚本失败（跳过）: %v", err)
				v.UnsubSecret = ""
			} else {
				if _, err := ssh.RunScript(ctx, sshCfg, unsubScript, func(stream, line string) {
					if line != "" {
						log("INFO", "[unsub/%s] %s", stream, line)
					}
				}); err != nil {
					log("WARN", "install_unsub 失败（跳过，不影响发信）: %v", err)
					v.UnsubSecret = ""
				} else {
					log("INFO", "退订服务部署成功 https://%s/u", v.RootDomain)
				}
			}
		}
	}

	_, _ = db.ExecContext(ctx,
		`UPDATE vps_instances SET dkim_public_key=?, smtp_account=?, smtp_password=?, unsub_secret=? WHERE id=?`,
		dkimPub, v.Username, v.Password, v.UnsubSecret, vpsID)

	if aliyunDNS != nil {
		rrs := DNSRRsForSubdomain(subdomain)
		mxPriority := 10
		// v0.1.57：SPF 聚合所有 IP（主 + 多 NIC 时的 additional）
		spfParts := []string{"v=spf1"}
		for _, p := range allIPs {
			spfParts = append(spfParts, "ip4:"+p)
		}
		spfParts = append(spfParts, "-all")
		spfValue := strings.Join(spfParts, " ")
		records := []dns.DnsRecordSpec{
			{RR: rrs.DKIM, RecordType: "TXT", Value: fmt.Sprintf("v=DKIM1; k=rsa; p=%s", dkimPub)},
			{RR: rrs.MX, RecordType: "MX", Value: fqdn, Priority: &mxPriority},
			{RR: rrs.SPF, RecordType: "TXT", Value: spfValue},
			{RR: rrs.DMARC, RecordType: "TXT", Value: fmt.Sprintf("v=DMARC1; p=reject; rua=mailto:dmarc@%s", domain)},
			// v0.2.6：mail-toolkit 约定的 SMTP 入口 smtp.根域 A→主 NIC IP；
			// 多 NIC 模式下也只指向主 IP，多个 IP 通过 mail1..mailN 子域分散
			{RR: "smtp", RecordType: "A", Value: ip},
		}
		// v0.1.57：多 NIC 时 mail1~mailN A 记录指向各自 IP（Received 链 / EHLO 三方匹配的关键）
		if len(allIPs) > 1 {
			for i, p := range allIPs {
				records = append(records, dns.DnsRecordSpec{
					RR:         fmt.Sprintf("mail%d", i+1),
					RecordType: "A",
					Value:      p,
				})
			}
		}
		for _, spec := range records {
			if err := upsertAliyunRecordAndSyncLocal(ctx, db, aliyunDNS, aliyunCredID, domain, vpsID, spec, log); err != nil {
				log("WARN", "UpsertRecord %s/%s 失败: %v", spec.RR, spec.RecordType, err)
			}
		}
	}

	// v0.1.75 实测确认：GCP 公网 PTR 只 nic0 真生效。nic1~7 的 AddAccessConfig + PublicPtrDomainName API 调用
	// 不返回错误（GCP "接受请求"），但实际后台不创建 PTR — dig -x <IP> 反解仍是 *.bc.googleusercontent.com。
	// 所以下面这个函数对 nic1~7 会做"调用 + 反向 DNS 校验"双重确认，假阳性会被 catch 出来标 partial。
	// SetInstancePTRForNIC 内部 Delete+Add AccessConfig，会短暂摘掉 nic0 的公网 NAT/SSH，因此放在所有 SSH 步骤之后。
	autoSetPTRForSupportedNICs(ctx, db, vpsID, ip, fqdn, addtlIPs, log)
	return nil
}

// autoSetPTRForSupportedNICs deploy 末尾自动设置所有 NIC 的 PTR。
//
// v0.1.75 修正（之前 v0.1.74 注释"实测支持 nic1+"是错的）：
// 实测 GCP 对 nic1~7 silent ignore — API 调用不报错但 PTR 不真生效，dig -x 反解仍是 *.bc.googleusercontent.com。
// 解决：调完 PTR API 后做反向 DNS 校验（PTR 反查应返回设置的 fqdn），假阳性记为 failed。
// ptr_status 语义升级：'set'=nic0+全部 nic1~N 都真生效；'partial'=nic0 真生效但 nic1+ 有假阳性；
//                       'failed'=nic0 失败；'pending'=进行中。
// 任一 nic1+ 失败不阻塞 deploy（保持兼容），但 UI 用 partial 让用户知道 87.5% 流量 PTR 残缺。
//
// 这是 aboutmy.email 评分掉 In-body 之外的关键项：nic1~7 PTR 残缺时 EHLO mail2..mail8.<域> 与反查域名
// 不一致 → Gmail/iCloud 反垃圾扣分。
func autoSetPTRForSupportedNICs(ctx context.Context, db *sql.DB, vpsID, ip, fqdn string, addtlIPs []AdditionalIPEntry, log func(level, format string, args ...interface{})) {
	var gcpCredID, zone, instanceName, rootDomain string
	if err := db.QueryRowContext(ctx,
		`SELECT gcp_cred_id, zone, name, COALESCE(domain,'') FROM vps_instances WHERE id=?`, vpsID,
	).Scan(&gcpCredID, &zone, &instanceName, &rootDomain); err != nil {
		log("WARN", "auto-PTR：读 vps 失败: %v（PTR 跳过，可手动跑 Stage D）", err)
		return
	}
	if gcpCredID == "" || zone == "" || instanceName == "" {
		log("WARN", "auto-PTR：缺 gcp_cred_id/zone/name，PTR 跳过")
		return
	}
	cli, err := loadGCPClient(ctx, gcpCredID)
	if err != nil {
		log("WARN", "auto-PTR：加载 GCP 客户端失败: %v（PTR 跳过）", err)
		return
	}
	defer cli.Close()

	_, _ = db.Exec(`UPDATE vps_instances SET ptr_status='pending' WHERE id=?`, vpsID)

	// nic0 主 PTR
	log("INFO", "auto-PTR nic0: %s -> %s", ip, fqdn)
	nic0OK := true
	if err := cli.SetInstancePTRForNIC(ctx, zone, instanceName, 0, ip, fqdn); err != nil {
		nic0OK = false
		_, _ = db.Exec(`UPDATE vps_instances SET ptr_status='failed' WHERE id=?`, vpsID)
		if errors.Is(err, gcp.ErrPTRNATLost) {
			log("ERROR", "🔴 auto-PTR nic0 失败且原公网 NAT 未能恢复——VPS %s 可能已失去外网 IP，需在 GCP 控制台/资源页手动重建 External NAT: %v", instanceName, err)
		} else {
			log("WARN", "auto-PTR nic0 失败（可走 Stage D 重试）: %v", err)
		}
	}

	// nic1~nicN: mail{N+1}.<rootDomain> -> additional IP
	// 不阻塞主流程：每个 NIC 失败只 warn，不动 ptr_status（ptr_status 反映的是主 NIC 状态）
	if rootDomain == "" {
		// 没 domain 的 VPS（一键模式或 fqdn 直接是 IP）跳过 mailN PTR
		if nic0OK {
			_, _ = db.Exec(`UPDATE vps_instances SET ptr_status='set' WHERE id=?`, vpsID)
			log("INFO", "✅ auto-PTR 设置成功: nic0（无 domain，跳过 nic1+）")
		}
		return
	}
	successCount := 0
	if nic0OK {
		successCount = 1
	}
	silentIgnoreCount := 0 // GCP API 不报错但 PTR 实际未生效（假阳性）的 NIC 数
	for _, e := range addtlIPs {
		nicIdx := e.NICIndex
		nicFQDN := fmt.Sprintf("mail%d.%s", nicIdx+1, rootDomain)
		log("INFO", "auto-PTR nic%d: %s -> %s", nicIdx, e.IP, nicFQDN)
		if err := cli.SetInstancePTRForNIC(ctx, zone, instanceName, nicIdx, e.IP, nicFQDN); err != nil {
			if errors.Is(err, gcp.ErrPTRNATLost) {
				log("ERROR", "🔴 auto-PTR nic%d 失败且原公网 NAT 未恢复——该 NIC (%s) 已失去外网出口，需在资源页重建 External NAT: %v", nicIdx, e.IP, err)
			} else {
				log("WARN", "auto-PTR nic%d API 失败（不阻塞，可在资源页批量重设）: %v", nicIdx, err)
			}
			continue
		}
		// v0.1.75：反向 DNS 校验。GCP API 对 nic1~7 经常 silent ignore，必须 dig -x 验证才知道是不是真生效。
		// v0.2.9：窗口从 5×6s=30s 放宽到 10×15s=150s。GCP PTR 传播常 30-120s，30s 太短会把"慢一拍
		// 但会真生效"的 NIC 误判成 silent ignore（partial 假警报）。校验失败才记 silentIgnore。
		if verifyReversePTR(ctx, e.IP, nicFQDN, 10, 15*time.Second) {
			successCount++
		} else {
			silentIgnoreCount++
			log("WARN", "auto-PTR nic%d 假阳性: API 接受但 dig -x %s 反解非 %s（GCP silent ignore 已知问题）", nicIdx, e.IP, nicFQDN)
		}
	}
	finalStatus := "set"
	if nic0OK && silentIgnoreCount > 0 {
		finalStatus = "partial" // nic0 真生效但 nic1+ 有假阳性，让 UI 显示橙色提示
	}
	if nic0OK {
		_, _ = db.Exec(`UPDATE vps_instances SET ptr_status=? WHERE id=?`, finalStatus, vpsID)
		if silentIgnoreCount > 0 {
			log("WARN", "⚠️ auto-PTR 完成但有假阳性: %d/%d NIC 真生效（nic0 OK，%d 个 NIC silent ignore）",
				successCount, 1+len(addtlIPs), silentIgnoreCount)
		} else {
			log("INFO", "✅ auto-PTR 完成: %d/%d NIC 全部真生效", successCount, 1+len(addtlIPs))
		}
	}
}

// verifyReversePTR 反向 DNS 校验：dig -x <ip> 是否返回预期的 fqdn。
// GCP nic1~7 的 PTR API 调用经常 silent ignore（不报错但实际未生效），必须 dig 验证。
// 比较时大小写不敏感，去尾点（DNS 标准格式）。返回 true 表示真生效。
func verifyReversePTR(ctx context.Context, ip, expectedFQDN string, maxAttempts int, interval time.Duration) bool {
	expected := strings.ToLower(strings.TrimSuffix(expectedFQDN, "."))
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		names, err := net.DefaultResolver.LookupAddr(ctx, ip)
		if err == nil {
			for _, n := range names {
				if strings.ToLower(strings.TrimSuffix(n, ".")) == expected {
					return true
				}
			}
		}
		if attempt < maxAttempts {
			time.Sleep(interval)
		}
	}
	return false
}

// deployMailcowOnVPS 部署 mailcow dockerized（收发一体，IMAP/SMTP）
func detectRemoteInternalIP(ctx context.Context, sshCfg ssh.Config) (string, error) {
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

func kumoMTASelfCheck(ctx context.Context, sshCfg ssh.Config, v DeployVars) error {
	keyPath := "/opt/kumomta/etc/keys/" + v.RootDomain + "/" + v.Selector + ".key"
	keyDir := "/opt/kumomta/etc/keys/" + v.RootDomain
	cmd := fmt.Sprintf(`set +e
fail=0
echo "===== KumoMTA self-check ====="
# v0.2.1：KumoMTA 在 e2-small (2 核) 上启动慢，加 30 秒重试避免 race。
# 每秒探一次 systemctl active + 587 监听，30 秒都没起来才判 FAIL。
ok=0
for i in $(seq 1 30); do
  if systemctl is-active --quiet kumomta && ss -tlnp 2>/dev/null | grep -E ':(587)\b' >/dev/null; then
    ok=1
    break
  fi
  sleep 1
done
if [ "$ok" != "1" ]; then
  systemctl is-active --quiet kumomta || { echo "FAIL: kumomta is not active"; fail=1; }
  ss -tlnp 2>/dev/null | grep -E ':(587)\b' >/dev/null || { echo "FAIL: 587 is not listening"; fail=1; }
fi
grep -F %s /opt/kumomta/etc/policy/init.lua >/dev/null || { echo "FAIL: init.lua source_address is not the VM internal IP %s"; fail=1; }
test -r %s || { echo "FAIL: DKIM private key is not readable: %s"; fail=1; }
echo "--- DKIM key dir 详情 (%s) ---"
ls -la %s 2>&1 || echo "(目录不存在)"
echo "--- 以 kumod 身份测试可读性 ---"
sudo -u kumod test -r %s 2>&1 && echo "kumod-readable: yes" || echo "kumod-readable: NO"
id kumod 2>&1 || echo "kumod 用户不存在"
echo "--- ip addr ---"
ip -o -4 addr show scope global || true
echo "--- ports ---"
ss -tlnp 2>/dev/null | grep -E ':(25|465|587)\b' || true
echo "--- journal tail ---"
journalctl -u kumomta -n 120 --no-pager 2>&1 || true
exit $fail
`, shellQuote("source_address = '"+v.BindIP+"'"), v.BindIP, shellQuote(keyPath), keyPath, keyDir, shellQuote(keyDir), shellQuote(keyPath))
	if out, err := ssh.RunCommand(ctx, sshCfg, cmd); err != nil {
		return fmt.Errorf("KumoMTA self-check 失败: %w\n%s", err, out)
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// deployPostfixOnVPS 在 VPS 上部署 Postfix + OpenDKIM（纯发信，与 mail-toolkit 同源脚本）
//
// 与 KumoMTA 路径的差异：
//   - 只装 Postfix + OpenDKIM（无 KumoMTA Lua 配置 / 无多 NIC source 池）
//   - 不部署 unsub-server（unsub 仅 KumoMTA 路径走）
//   - DKIM 公钥从 install_postfix.sh 的 stdout 行 "DKIM_PUBLIC_KEY=..." 捕获
//   - DNS 记录与 KumoMTA 单 NIC 场景一致：DKIM/MX/SPF/DMARC
func deployPostfixOnVPS(ctx context.Context, db *sql.DB, vpsID, ip, fqdn, subdomain, domain string, aliyunDNS *dns.AliyunDns, aliyunCredID string, log func(level, format string, args ...interface{})) error {
	sshCfg := ssh.Config{Host: ip, Port: 22, Username: "root", KeyContent: string(sshkey.PrivatePEM())}
	v := BuildDeployVars(domain, subdomain, ip)

	log("INFO", "开始安装 Postfix + OpenDKIM（域=%s 主机=%s 邮箱=%s@%s）", v.RootDomain, v.FQDN, v.Username, v.RootDomain)
	installScript, err := RenderInstallPostfix(v)
	if err != nil {
		return fmt.Errorf("RenderInstallPostfix: %w", err)
	}

	// Postfix + apt 装包耗时较长（首次 ~3-5 分钟），流式输出到日志
	out, err := ssh.RunScript(ctx, sshCfg, installScript, func(stream, line string) {
		if line != "" {
			log("INFO", "[postfix/%s] %s", stream, line)
		}
	})
	if err != nil {
		return fmt.Errorf("install_postfix 失败: %w", err)
	}

	// 从 stdout 捕获 DKIM_PUBLIC_KEY=...（脚本 Phase 6 输出）
	dkimPub := extractDKIMPublicKey(out)
	if dkimPub == "" {
		return fmt.Errorf("DKIM 公钥提取失败（脚本未输出 DKIM_PUBLIC_KEY 行）")
	}
	log("INFO", "DKIM 公钥已提取（%d chars）", len(dkimPub))

	// v0.2.6：SASL 用 sasldb，登录串 = info@根域名（v.Username）。
	// 与 KumoMTA 路径一致；mail-toolkit 约定也是 From=登录串=info@根域。
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
			// v0.2.6：mail-toolkit 约定的 SMTP 入口 smtp.根域 A→server_ip
			{RR: "smtp", RecordType: "A", Value: ip},
		}
		for _, spec := range records {
			if err := upsertAliyunRecordAndSyncLocal(ctx, db, aliyunDNS, aliyunCredID, domain, vpsID, spec, log); err != nil {
				log("WARN", "UpsertRecord %s/%s 失败: %v", spec.RR, spec.RecordType, err)
			}
		}
	}

	// 单 NIC 反向 DNS（与 KumoMTA 路径同一函数；nic0 真生效，nic1+ 假阳性会被检测）
	autoSetPTRForSupportedNICs(ctx, db, vpsID, ip, fqdn, nil, log)

	log("INFO", "✅ Postfix + OpenDKIM 部署完成。SMTP: smtp.%s:587 STARTTLS | 账号: %s | 密码: %s", domain, v.Username, v.Password)
	return nil
}

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
			// v0.2.6：mail-toolkit 约定的 SMTP 入口 smtp.根域 A→server_ip
			{RR: "smtp", RecordType: "A", Value: ip},
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

// dirtyIPRow 一个待 release 的脏 IP（reserved 但 DNSBL/前缀判定不通过）
type dirtyIPRow struct {
	ipID      string
	addrName  string
	gcpCredID string
	region    string
	gcpClient *gcp.Client
	reserveAt time.Time
}

// dirtyIPHolder 收集脏 IP，主动 hold 不释放——让 GCP 静态 IP 池被脏 IP 占满，
// 下次 reserve 必然分配到 GCP 内部"还没分配过的新 IP"，命中干净 IP 概率显著上升。
//
// v0.1.63 策略：**不设固定 hold 阈值**，hold 到以下任一条件才释放：
//  1. 触达 GCP 配额（worker reserve 拿到 QUOTA_EXCEEDED → 调 Flush 释放全部 pending，腾出配额）
//  2. Stage A 结束（Drain 清扫剩余）
//
// 这样用户配额越大（100+），hold 越多脏 IP，IP 池翻新效果越好。
// holdThreshold 改作"软警告阈值"——超过时打日志提醒，但不主动释放。
type dirtyIPHolder struct {
	mu            sync.Mutex
	holdThreshold int // v0.1.63：软警告，仅用于日志，超过仍 hold 不主动释放
	pending       []dirtyIPRow
	totalAdded    int
	totalReleased int
}

// newDirtyIPHolder v0.1.63：策略改为"hold 到 QUOTA_EXCEEDED 或 Stage A 结束才释放"。
// holdThreshold 改作软警告阈值，每 50 个 hold 打一行日志让用户感知规模。
func newDirtyIPHolder() *dirtyIPHolder {
	return &dirtyIPHolder{holdThreshold: 50}
}

// Add 累加一个脏 IP；如果攒够 holdThreshold 个，触发同步 release（在调用 goroutine 内做，
// 避免 release 失败时 goroutine 提前退出错过日志）。
// onLog 是 reserveAndFilterOnce 的 log callback，复用免去多传参。
// Add v0.1.63：hold 不主动释放——只累计 + 软警告日志。
// 真正释放靠：QUOTA_EXCEEDED 触发 Flush（reserveAndFilterOnce 内）/ Stage A 结束 Drain。
func (h *dirtyIPHolder) Add(row dirtyIPRow, _ context.Context, _ *sql.DB, log func(string, string, ...interface{})) {
	h.mu.Lock()
	h.pending = append(h.pending, row)
	h.totalAdded++
	count := len(h.pending)
	threshold := h.holdThreshold
	h.mu.Unlock()

	// 每达到 threshold 整数倍打一行进度日志（hold 50 / 100 / 150 ...）
	if threshold > 0 && count > 0 && count%threshold == 0 {
		log("INFO", "📥 已 hold %d 个脏 IP（让 GCP 池被占满，新 reserve 倾向分配新 IP）", count)
	}
}

// Drain Stage A 结束时调用，释放所有剩余 pending 脏 IP。
func (h *dirtyIPHolder) Drain(ctx context.Context, db *sql.DB, log func(string, string, ...interface{})) {
	h.mu.Lock()
	batch := h.pending
	h.pending = nil
	h.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	log("INFO", "Stage A 结束：清扫剩余 %d 个脏 IP", len(batch))
	h.releaseBatch(ctx, db, batch, log)
}

// Flush 立即释放所有 pending 脏 IP（不等到 holdThreshold）。
// 用途：Stage A worker 遇到 GCP QUOTA_EXCEEDED 时，先 flush 释放占位的脏 IP 让出配额，
// 而不是直接标 region exhausted 退出 worker。
// 返回释放的 IP 数。
func (h *dirtyIPHolder) Flush(ctx context.Context, db *sql.DB, log func(string, string, ...interface{})) int {
	h.mu.Lock()
	batch := h.pending
	h.pending = nil
	h.mu.Unlock()
	if len(batch) == 0 {
		return 0
	}
	log("INFO", "QUOTA_EXCEEDED：立即 flush 释放 %d 个 pending 脏 IP 让出配额", len(batch))
	h.releaseBatch(ctx, db, batch, log)
	return len(batch)
}

// releaseBatch 并发释放一批脏 IP（每个 IP 失败不影响其他）。
// 用 background ctx，因为 Stage A 取消时仍然要尽量回收 GCP 资源（不然 IP 持续计费）。
// v0.1.63：用 sem 限并发到 20，避免一次性 100+ 个 goroutine 把 GCP API 撑爆；
// 总超时 5 分钟（100 IP × ~3s GCP API/IP / 20 并发 ~= 15s 实际，留余地）。
func (h *dirtyIPHolder) releaseBatch(_ context.Context, db *sql.DB, batch []dirtyIPRow, log func(string, string, ...interface{})) {
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sem := make(chan struct{}, 20)
	var wg sync.WaitGroup
	var ok int64
	for _, row := range batch {
		wg.Add(1)
		sem <- struct{}{}
		go func(r dirtyIPRow) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := r.gcpClient.ReleaseStaticAddress(releaseCtx, r.region, r.addrName); err != nil && !gcp.IsNotFound(err) {
				log("WARN", "释放脏 IP %s 失败（GCP 端）: %v", r.addrName, err)
				return
			}
			_, _ = db.ExecContext(releaseCtx, `UPDATE static_ips SET status='released' WHERE id=?`, r.ipID)
			atomic.AddInt64(&ok, 1)
		}(row)
	}
	wg.Wait()
	h.mu.Lock()
	h.totalReleased += int(ok)
	total := h.totalReleased
	h.mu.Unlock()
	// v0.1.62：用 SUCCESS 级别 + 累计释放数，让用户能直观看到释放在持续工作（不是卡住）
	log("SUCCESS", "✅ 本批脏 IP 释放完成 %d/%d（本次 Stage A 累计已释放 %d 个）", ok, len(batch), total)
}

// AdditionalIPEntry vps_instances.additional_ips_json 的元素结构（v0.1.57+）
// 这是多 NIC 模式 KumoMTA 8 source 轮换 + DNS mail{N+1} A 记录 + PTR 设置 三方共用的载荷。
// 字段 stable 化后续不要随便改名，否则旧 batch 的 JSON 反序列化会失败。
type AdditionalIPEntry struct {
	NICIndex   int    `json:"nic_index"`
	IP         string `json:"ip"`
	InternalIP string `json:"internal_ip"`
	EHLO       string `json:"ehlo"`
	AddrName   string `json:"addr_name"`
}

// buildAdditionalIPsJSON 把非主 NIC（nic1..N）的信息序列化成 additional_ips_json。
// nicInfos 是 GCP 返回的全部 NIC 信息（NICs[0]..NICs[N-1]）；group.ips 是按 nic_index 排序的静态 IP 行。
// 返回的 entries 索引从 1 开始（nic_index=1..N-1），nic0 信息存在 vps_instances 主字段不重复。
// EHLO 字段存「子域 mail{N+1}」，调用方按需拼 ".<rootDomain>"；KumoMTA / DNS 遵循这个约定。
// GCP Public PTR 实测仅 nic0 真生效；nic1~N 的 PTR API 调用 silent ignore（autoSetPTRForSupportedNICs
// 会做反向 DNS 校验把假阳性识别出来标 partial）。
func buildAdditionalIPsJSON(groupIPs []groupIP, nicInfos []gcp.NICInfo) []AdditionalIPEntry {
	out := make([]AdditionalIPEntry, 0, len(groupIPs)-1)
	for i := 1; i < len(groupIPs); i++ {
		entry := AdditionalIPEntry{
			NICIndex: groupIPs[i].nicIndex,
			IP:       groupIPs[i].ip,
			AddrName: groupIPs[i].addrName,
			EHLO:     fmt.Sprintf("mail%d", i+1), // 仅子域；调用方拼 .<rootDomain>
		}
		if i < len(nicInfos) {
			entry.InternalIP = nicInfos[i].InternalIP
		}
		out = append(out, entry)
	}
	return out
}

// releaseGroupIPs Stage B 创建实例失败时回收整组 IP（DB 标 released + GCP 释放 address）。
// 不阻塞 ctx done（即便用户取消也尽量清理资源）。
func releaseGroupIPs(ctx context.Context, db *sql.DB, gcpClient *gcp.Client, region string, ips []groupIP) error {
	for _, ipRow := range ips {
		_, _ = db.ExecContext(ctx, `UPDATE static_ips SET status='released', bound_instance_id='' WHERE id=?`, ipRow.ipID)
		_ = gcpClient.ReleaseStaticAddress(context.Background(), region, ipRow.addrName)
	}
	return nil
}

// groupCleanIPs 把 batchID 下所有 status='clean' 且未分组的 IP 按 nicCount 划分组。
// 单 NIC（nicCount<=1）：每个 IP 自成一组（slot_group=自身 ID, nic_index=0）。
// 多 NIC：先按 (gcp_cred_id, region) 分区，每个分区内每 nicCount 个为一组（uuid + nic_index=0..N-1）；
//
//	余数（不够一组的尾巴）标记 status='orphan'，下批次 Stage B 不会消费。
//	**不跨账号/region 合并**——避免一组里混进多个 GCP 项目的 IP（VPS 创建时 client/region 不一致会失败）。
//
// 返回成功分组数（多 NIC 时表示 VPS 数；单 NIC 时返回 -1，调用方仅看 err）。
func groupCleanIPs(db *sql.DB, batchID string, nicCount int) (int, error) {
	if nicCount <= 1 {
		_, err := db.Exec(
			`UPDATE static_ips SET slot_group=id, nic_index=0
			   WHERE batch_id=? AND status='clean' AND COALESCE(slot_group,'')=''`,
			batchID,
		)
		return -1, err
	}
	rows, err := db.Query(
		`SELECT id, gcp_cred_id, region FROM static_ips
		   WHERE batch_id=? AND status='clean' AND COALESCE(slot_group,'')=''
		   ORDER BY gcp_cred_id ASC, region ASC, created_at ASC, id ASC`,
		batchID,
	)
	if err != nil {
		return 0, err
	}
	type row struct{ id, credID, region string }
	partitions := map[string][]string{} // key=credID|region → ids
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.credID, &r.region); err != nil {
			rows.Close()
			return 0, err
		}
		key := r.credID + "|" + r.region
		partitions[key] = append(partitions[key], r.id)
	}
	rows.Close()

	groupCount := 0
	for _, ids := range partitions {
		// 此分区内 batch 数量
		for i := 0; i+nicCount <= len(ids); i += nicCount {
			grp := uuid.NewString()
			for j := 0; j < nicCount; j++ {
				if _, err := db.Exec(
					`UPDATE static_ips SET slot_group=?, nic_index=? WHERE id=?`,
					grp, j, ids[i+j],
				); err != nil {
					return groupCount, err
				}
			}
			groupCount++
		}
		// 余数标 orphan
		fullGroups := len(ids) / nicCount
		for _, id := range ids[fullGroups*nicCount:] {
			_, _ = db.Exec(`UPDATE static_ips SET status='orphan' WHERE id=?`, id)
		}
	}
	return groupCount, nil
}
