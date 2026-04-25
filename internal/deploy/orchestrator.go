package deploy

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"gcp-mailnode/internal/crypto"
	"gcp-mailnode/internal/dns"
	"gcp-mailnode/internal/dnsbl"
	"gcp-mailnode/internal/gcp"
	"gcp-mailnode/internal/logger"
	"gcp-mailnode/internal/ssh"
	"gcp-mailnode/internal/sshkey"
	"gcp-mailnode/internal/store"
)

// BatchRequest 前端传入的批量部署请求
type BatchRequest struct {
	GCPCredIDs      []string `json:"gcp_cred_ids"`
	AliyunCredID    string   `json:"aliyun_cred_id"`
	TemplateID      string   `json:"template_id"`
	Count           int      `json:"count"`
	Regions         []string `json:"regions"`
	Domains         []string `json:"domains"`
	RootPassword    string   `json:"root_password"`
	DNSBLThreshold  int      `json:"dnsbl_threshold"`
	MaxRetryPerSlot int      `json:"max_retry_per_slot"`
}

// BatchProgress 批量任务当前进度
type BatchProgress struct {
	ID        string `json:"id"`
	Total     int    `json:"total"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	Status    string `json:"status"`
}

// LogCallback 日志回调
type LogCallback func(batchID string, slot int, level, msg string)

// VPSTemplate 模板表映射（轻量，用于编排）
type VPSTemplate struct {
	ID             string
	Name           string
	Regions        []string
	AutoSpread     bool
	MachineType    string
	ImageFamily    string
	ImageProject   string
	DiskSizeGB     int64
	DiskType       string // pd-standard / pd-balanced / pd-ssd
	Tags           []string
	MetadataScript string
	RootPassword   string
	DeployType     string // kumomta（默认）/ mailcow
}

// batchState 运行中的批量任务状态
type batchState struct {
	id        string
	total     int
	succeeded int64
	failed    int64
	status    string
	mu        sync.Mutex
	cancel    context.CancelFunc
}

var (
	runningBatches sync.Map // batchID -> *batchState
	domainCounters sync.Map // domain -> *int64
)

// Start 启动一次批量部署；异步执行，返回 batchID
func Start(ctx context.Context, req BatchRequest, onLog LogCallback) (string, error) {
	if req.Count <= 0 {
		return "", fmt.Errorf("Count 必须 > 0")
	}
	if len(req.GCPCredIDs) == 0 {
		return "", fmt.Errorf("至少选择一个 GCP 凭证")
	}
	if len(req.Domains) == 0 {
		return "", fmt.Errorf("至少选择一个根域名")
	}
	if req.TemplateID == "" {
		return "", fmt.Errorf("必须指定模板 ID")
	}
	if req.DNSBLThreshold <= 0 {
		req.DNSBLThreshold = 1
	}
	if req.MaxRetryPerSlot <= 0 {
		req.MaxRetryPerSlot = 10
	}

	batchID := uuid.NewString()

	db := store.DB()
	if db == nil {
		return "", fmt.Errorf("数据库未就绪")
	}
	reqJSON, _ := json.Marshal(req)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO batch_tasks (id, request_json, status, total) VALUES (?,?,?,?)`,
		batchID, string(reqJSON), "running", req.Count); err != nil {
		return "", fmt.Errorf("写入 batch_tasks 失败: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	state := &batchState{id: batchID, total: req.Count, status: "running", cancel: cancel}
	runningBatches.Store(batchID, state)

	if onLog == nil {
		onLog = func(string, int, string, string) {}
	}

	go runBatch(runCtx, batchID, req, state, onLog)

	return batchID, nil
}

// Cancel 请求取消指定批量任务
func Cancel(batchID string) error {
	v, ok := runningBatches.Load(batchID)
	if !ok {
		return fmt.Errorf("未找到批量任务: %s", batchID)
	}
	st := v.(*batchState)
	st.mu.Lock()
	st.status = "cancelling"
	if st.cancel != nil {
		st.cancel()
	}
	st.mu.Unlock()
	return nil
}

// Progress 返回批量任务进度
func Progress(batchID string) (BatchProgress, error) {
	v, ok := runningBatches.Load(batchID)
	if ok {
		st := v.(*batchState)
		st.mu.Lock()
		defer st.mu.Unlock()
		return BatchProgress{
			ID:        st.id,
			Total:     st.total,
			Succeeded: int(atomic.LoadInt64(&st.succeeded)),
			Failed:    int(atomic.LoadInt64(&st.failed)),
			Status:    st.status,
		}, nil
	}
	// 回落到 DB
	db := store.DB()
	if db == nil {
		return BatchProgress{}, fmt.Errorf("数据库未就绪")
	}
	var p BatchProgress
	row := db.QueryRow(`SELECT id, total, succeeded, failed, status FROM batch_tasks WHERE id=?`, batchID)
	if err := row.Scan(&p.ID, &p.Total, &p.Succeeded, &p.Failed, &p.Status); err != nil {
		return BatchProgress{}, fmt.Errorf("查询 batch_tasks 失败: %w", err)
	}
	return p, nil
}

func runBatch(ctx context.Context, batchID string, req BatchRequest, st *batchState, onLog LogCallback) {
	defer func() {
		// 更新任务终态
		db := store.DB()
		finalStatus := "finished"
		st.mu.Lock()
		if st.status == "cancelling" {
			finalStatus = "cancelled"
		}
		st.status = finalStatus
		st.mu.Unlock()
		if db != nil {
			_, _ = db.Exec(`UPDATE batch_tasks SET status=?, succeeded=?, failed=?, finished_at=CURRENT_TIMESTAMP WHERE id=?`,
				finalStatus,
				int(atomic.LoadInt64(&st.succeeded)),
				int(atomic.LoadInt64(&st.failed)),
				batchID)
		}
		// 保留 state 一段时间便于前端查询最终结果，30 分钟后移除
		time.AfterFunc(30*time.Minute, func() { runningBatches.Delete(batchID) })
	}()

	// 加载模板
	tmpl, err := loadTemplate(req.TemplateID)
	if err != nil {
		onLog(batchID, 0, "ERROR", fmt.Sprintf("加载模板失败: %v", err))
		atomic.AddInt64(&st.failed, int64(req.Count))
		return
	}

	// 预加载 GCP 客户端（按 credID 缓存）
	gcpClients := make(map[string]*gcp.Client)
	var gcpMu sync.Mutex
	getGCP := func(credID string) (*gcp.Client, error) {
		gcpMu.Lock()
		defer gcpMu.Unlock()
		if cli, ok := gcpClients[credID]; ok {
			return cli, nil
		}
		cli, err := loadGCPClient(ctx, credID)
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

	// 加载阿里云
	var aliyunDNS *dns.AliyunDns
	if req.AliyunCredID != "" {
		aliyunDNS, err = loadAliyunDns(req.AliyunCredID)
		if err != nil {
			onLog(batchID, 0, "ERROR", fmt.Sprintf("加载阿里云凭证失败: %v", err))
			atomic.AddInt64(&st.failed, int64(req.Count))
			return
		}
	}

	// region 列表
	regions := req.Regions
	if len(regions) == 0 {
		regions = tmpl.Regions
	}
	if len(regions) == 0 {
		onLog(batchID, 0, "ERROR", "没有可用 region")
		atomic.AddInt64(&st.failed, int64(req.Count))
		return
	}

	// 并发控制
	concurrency := req.Count
	if concurrency > 10 {
		concurrency = 10
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

dispatchLoop:
	for i := 1; i <= req.Count; i++ {
		select {
		case <-ctx.Done():
			onLog(batchID, 0, "WARN", "批量任务已取消，停止派发剩余 slot")
			atomic.AddInt64(&st.failed, int64(req.Count-i+1))
			break dispatchLoop
		default:
		}

		slot := i
		gcpCredID := req.GCPCredIDs[(slot-1)%len(req.GCPCredIDs)]
		region := regions[(slot-1)%len(regions)]
		domain := req.Domains[(slot-1)%len(req.Domains)]

		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					onLog(batchID, slot, "ERROR", fmt.Sprintf("slot panic: %v", r))
					atomic.AddInt64(&st.failed, 1)
				}
			}()

			cli, err := getGCP(gcpCredID)
			if err != nil {
				onLog(batchID, slot, "ERROR", fmt.Sprintf("获取 GCP 客户端失败: %v", err))
				atomic.AddInt64(&st.failed, 1)
				return
			}

			if err := runSlot(ctx, batchID, slot, req, tmpl, cli, aliyunDNS, domain, region, onLog); err != nil {
				atomic.AddInt64(&st.failed, 1)
				return
			}
			atomic.AddInt64(&st.succeeded, 1)
		}()
	}
	wg.Wait()
}

// runSlot 单个部署槽位的完整流程
func runSlot(
	ctx context.Context,
	batchID string,
	slot int,
	req BatchRequest,
	tmpl VPSTemplate,
	gcpClient *gcp.Client,
	aliyunDNS *dns.AliyunDns,
	domain string,
	region string,
	onLog LogCallback,
) error {
	log := func(level, format string, args ...interface{}) {
		onLog(batchID, slot, level, fmt.Sprintf(format, args...))
	}

	// 1. 选子域
	counterRaw, _ := domainCounters.LoadOrStore(domain, new(int64))
	counter := counterRaw.(*int64)
	idx := atomic.AddInt64(counter, 1)
	subdomain := fmt.Sprintf("mail-%03d", idx)
	fqdn := subdomain + "." + domain
	log("INFO", "slot=%d 开始: domain=%s subdomain=%s region=%s", slot, domain, subdomain, region)

	db := store.DB()
	if db == nil {
		return fmt.Errorf("数据库未就绪")
	}

	// 2. 预留+筛 IP
	var chosenAddr gcp.AddressInfo
	var staticIPID string
	rootPassword := req.RootPassword
	if rootPassword == "" {
		rootPassword = tmpl.RootPassword
	}
	if rootPassword == "" {
		rootPassword = "ChangeMe!" + uuid.NewString()[:6]
	}

	gcpCredID := req.GCPCredIDs[(slot-1)%len(req.GCPCredIDs)]

	var reservedButDirty []gcp.AddressInfo
	defer func() {
		// 未使用的 dirty IP 统一释放
		for _, a := range reservedButDirty {
			if err := gcpClient.ReleaseStaticAddress(context.Background(), a.Region, a.Name); err != nil {
				logger.Warn("释放 dirty IP 失败 %s: %v", a.Name, err)
			}
		}
	}()

	found := false
	for attempt := 1; attempt <= req.MaxRetryPerSlot; attempt++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("已取消")
		default:
		}
		addr, err := gcpClient.ReserveStaticAddress(ctx, region, "")
		if err != nil {
			log("ERROR", "预留静态 IP 失败 (attempt=%d): %v", attempt, err)
			continue
		}
		log("INFO", "已预留 IP: %s (%s)", addr.IP, addr.Name)

		ipID := uuid.NewString()
		if _, err := db.ExecContext(ctx,
			`INSERT INTO static_ips (id, gcp_cred_id, gcp_address_name, ip, region, status) VALUES (?,?,?,?,?,?)`,
			ipID, gcpCredID, addr.Name, addr.IP, region, "reserved"); err != nil {
			log("WARN", "记录 static_ips 失败: %v", err)
		}

		// 黑段检查
		if seg, note, err := dnsbl.ContainsIP(ctx, addr.IP); err == nil && seg != "" {
			log("WARN", "IP %s 命中黑段 %s(%s)，释放重试", addr.IP, seg, note)
			_, _ = db.ExecContext(ctx, `UPDATE static_ips SET dnsbl_result='blacklisted', dnsbl_hit_lists=?, status='released' WHERE id=?`, "blackseg:"+seg, ipID)
			if relErr := gcpClient.ReleaseStaticAddress(ctx, region, addr.Name); relErr != nil {
				log("WARN", "释放 IP 失败: %v", relErr)
			}
			continue
		}

		// DNSBL 检测
		verdict, reason, detail, derr := dnsbl.Decide(ctx, addr.IP, dnsbl.CheckOptions{Threshold: req.DNSBLThreshold}, 6*time.Hour)
		hitLists := ""
		if detail != nil {
			hitLists = strings.Join(detail.HitLists, ",")
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE static_ips SET dnsbl_result=?, dnsbl_hit_lists=? WHERE id=?`, verdict, hitLists, ipID); err != nil {
			log("WARN", "更新 DNSBL 结果失败: %v", err)
		}
		if derr != nil {
			log("WARN", "DNSBL 检测出错: %v", derr)
		}
		if verdict == "clean" {
			log("INFO", "IP %s 检测通过 (reason=%s)", addr.IP, reason)
			chosenAddr = addr
			staticIPID = ipID
			found = true
			break
		}

		log("WARN", "IP %s 判脏: %s (%s)，释放重试", addr.IP, verdict, reason)
		_, _ = db.ExecContext(ctx, `UPDATE static_ips SET status='released' WHERE id=?`, ipID)
		if relErr := gcpClient.ReleaseStaticAddress(ctx, region, addr.Name); relErr != nil {
			log("WARN", "释放 IP 失败: %v", relErr)
		}
	}
	if !found {
		return fmt.Errorf("超过最大重试次数仍未找到干净 IP")
	}

	// 3. startup-script（v0.1.7+ 改用 SSH 密钥登录；模板 metadata_script 会被包装后运行）
	startupScript := buildStartupScript(tmpl.MetadataScript)

	// 4. 建 VPS
	zone := region + "-a"
	instanceName := fmt.Sprintf("mn-%s", uuid.NewString()[:8])
	machineType := tmpl.MachineType
	if machineType == "" {
		machineType = "e2-micro"
	}
	imageFamily := tmpl.ImageFamily
	if imageFamily == "" {
		imageFamily = "debian-12"
	}
	imageProject := tmpl.ImageProject
	if imageProject == "" {
		imageProject = "debian-cloud"
	}
	diskSize := tmpl.DiskSizeGB
	if diskSize <= 0 {
		diskSize = 10
	}
	diskType := tmpl.DiskType
	if diskType == "" {
		diskType = "pd-balanced"
	}

	if err := gcpClient.EnsureMailNodeFirewall(ctx, loadFirewallAllowlist(gcpCredID)); err != nil {
		log("WARN", "确保防火墙规则失败: %v", err)
	}

	spec := gcp.InstanceSpec{
		Name:          instanceName,
		Zone:          zone,
		MachineType:   machineType,
		ImageFamily:   imageFamily,
		ImageProject:  imageProject,
		DiskSizeGB:    diskSize,
		DiskType:      diskType,
		Tags:          mergeMailNodeTag(tmpl.Tags),
		StartupScript: startupScript,
		StaticIP:      chosenAddr.IP,
		NetworkName:   "default",
	}

	log("INFO", "创建 VM: name=%s zone=%s mt=%s", instanceName, zone, machineType)
	inst, err := gcpClient.CreateInstance(ctx, spec)
	if err != nil {
		log("ERROR", "CreateInstance 失败: %v", err)
		// 释放 IP
		_, _ = db.ExecContext(ctx, `UPDATE static_ips SET status='released' WHERE id=?`, staticIPID)
		_ = gcpClient.ReleaseStaticAddress(context.Background(), region, chosenAddr.Name)
		return err
	}
	_ = inst

	vpsID := uuid.NewString()
	_, err = db.ExecContext(ctx,
		`INSERT INTO vps_instances (id, gcp_cred_id, gcp_instance_id, name, region, zone, machine_type, status, ip, fqdn, root_password, deploy_status, aliyun_cred_id, domain)
         VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		vpsID, gcpCredID, instanceName, instanceName, region, zone, machineType, "pending",
		chosenAddr.IP, fqdn, rootPassword, "pending", req.AliyunCredID, domain)
	if err != nil {
		log("WARN", "记录 vps_instances 失败: %v", err)
	}

	// 出错时回滚：删 VPS + 释放 IP
	instDeleted := false
	defer func() {
		if instDeleted {
			return
		}
	}()

	cleanup := func(reason error) {
		log("WARN", "发生错误 %v，开始回滚资源", reason)
		if delErr := gcpClient.DeleteInstance(context.Background(), zone, instanceName); delErr != nil {
			log("WARN", "删除 VM 失败: %v", delErr)
		} else {
			instDeleted = true
		}
		_, _ = db.Exec(`UPDATE vps_instances SET status='deleted', deploy_status='failed', deploy_error=? WHERE id=?`, reason.Error(), vpsID)
		_, _ = db.Exec(`UPDATE static_ips SET status='released' WHERE id=?`, staticIPID)
		if relErr := gcpClient.ReleaseStaticAddress(context.Background(), region, chosenAddr.Name); relErr != nil {
			log("WARN", "释放 IP 失败: %v", relErr)
		}
	}

	// 等 VM running
	log("INFO", "等待 VM running...")
	if _, err := gcpClient.WaitForRunning(ctx, zone, instanceName, 5*time.Minute); err != nil {
		cleanup(err)
		return err
	}
	_, _ = db.ExecContext(ctx, `UPDATE vps_instances SET status='running' WHERE id=?`, vpsID)
	_, _ = db.ExecContext(ctx, `UPDATE static_ips SET status='in_use', bound_instance_id=? WHERE id=?`, vpsID, staticIPID)

	// 5. SSH 拨测
	sshCfg := ssh.Config{Host: chosenAddr.IP, Port: 22, Username: "root", KeyContent: string(sshkey.PrivatePEM())}
	_ = rootPassword // v0.1.7+ 保留字段但不用于登录
	var sshOK bool
	for i := 1; i <= 10; i++ {
		select {
		case <-ctx.Done():
			cleanup(fmt.Errorf("已取消"))
			return ctx.Err()
		default:
		}
		if err := ssh.TestConnection(sshCfg); err == nil {
			sshOK = true
			log("INFO", "SSH 连通 (attempt=%d)", i)
			break
		} else {
			log("INFO", "SSH 拨测失败 attempt=%d: %v", i, err)
		}
		time.Sleep(10 * time.Second)
	}
	if !sshOK {
		err := fmt.Errorf("SSH 拨测失败，10 次未成功")
		cleanup(err)
		return err
	}

	// 6. 阿里云 A 记录（先建一条，后面 DKIM 生成后再补齐 TXT/MX/SPF/DMARC）
	if aliyunDNS != nil {
		rrs := DNSRRsForSubdomain(subdomain)
		if err := upsertAliyunRecordAndSyncLocal(ctx, db, aliyunDNS, req.AliyunCredID, domain, vpsID, dns.DnsRecordSpec{
			RR: rrs.A, RecordType: "A", Value: chosenAddr.IP,
		}, log); err != nil {
			log("WARN", "UpsertRecord A 失败: %v", err)
		}
	}

	// 7. 部署 KumoMTA
	v := BuildDeployVars(domain, subdomain, chosenAddr.IP)

	installScript, err := RenderInstallKumoMTA(v)
	if err != nil {
		cleanup(err)
		return err
	}
	log("INFO", "远程执行 install_kumomta.sh...")
	if _, err := ssh.RunScript(ctx, sshCfg, installScript, nil); err != nil {
		log("ERROR", "install_kumomta 执行失败: %v", err)
		cleanup(err)
		return err
	}

	dkimScript, err := RenderDkimSetup(v)
	if err != nil {
		cleanup(err)
		return err
	}
	log("INFO", "远程执行 dkim_setup.sh...")
	out, err := ssh.RunScript(ctx, sshCfg, dkimScript, nil)
	if err != nil {
		log("ERROR", "dkim_setup 执行失败: %v", err)
		cleanup(err)
		return err
	}
	dkimPub := extractDKIMPublicKey(out)
	if dkimPub == "" {
		err := fmt.Errorf("DKIM 脚本输出未包含 DKIM_PUBLIC_KEY")
		cleanup(err)
		return err
	}
	log("INFO", "获得 DKIM 公钥长度=%d", len(dkimPub))

	initLua, err := RenderInitLua(v)
	if err != nil {
		cleanup(err)
		return err
	}
	authLua, err := RenderSmtpAuthLua(v)
	if err != nil {
		cleanup(err)
		return err
	}
	initB64 := base64.StdEncoding.EncodeToString([]byte(initLua))
	authB64 := base64.StdEncoding.EncodeToString([]byte(authLua))

	deployCfgScript, err := GetDeployConfigScript()
	if err != nil {
		cleanup(err)
		return err
	}
	cmd := fmt.Sprintf(`cat > /tmp/deploy_config.sh <<'EOFDEPLOY'
%s
EOFDEPLOY
chmod +x /tmp/deploy_config.sh
/tmp/deploy_config.sh %q %q`, deployCfgScript, initB64, authB64)
	log("INFO", "远程执行 deploy_config.sh...")
	if _, err := ssh.RunCommand(ctx, sshCfg, cmd); err != nil {
		log("ERROR", "deploy_config 失败: %v", err)
		cleanup(err)
		return err
	}

	_, _ = db.ExecContext(ctx,
		`UPDATE vps_instances SET dkim_public_key=?, smtp_account=?, smtp_password=? WHERE id=?`,
		dkimPub, v.Username, v.Password, vpsID)

	// 8. DKIM/MX/SPF/DMARC
	if aliyunDNS != nil {
		rrs := DNSRRsForSubdomain(subdomain)
		dkimRR := rrs.DKIM
		dkimValue := fmt.Sprintf("v=DKIM1; k=rsa; p=%s", dkimPub)
		mxRR := rrs.MX
		mxPriority := 10
		mxValue := fqdn
		spfRR := rrs.SPF
		spfValue := fmt.Sprintf("v=spf1 ip4:%s -all", chosenAddr.IP)
		dmarcRR := rrs.DMARC
		dmarcValue := fmt.Sprintf("v=DMARC1; p=reject; rua=mailto:dmarc@%s", domain)

		records := []struct {
			rr, rtype, value string
			priority         *int
		}{
			{dkimRR, "TXT", dkimValue, nil},
			{mxRR, "MX", mxValue, &mxPriority},
			{spfRR, "TXT", spfValue, nil},
			{dmarcRR, "TXT", dmarcValue, nil},
		}
		for _, r := range records {
			if err := upsertAliyunRecordAndSyncLocal(ctx, db, aliyunDNS, req.AliyunCredID, domain, vpsID, dns.DnsRecordSpec{
				RR: r.rr, RecordType: r.rtype, Value: r.value, Priority: r.priority,
			}, log); err != nil {
				log("WARN", "UpsertRecord %s/%s 失败: %v", r.rr, r.rtype, err)
				continue
			}
		}
	}

	// 9. GCP PTR
	log("INFO", "配置 PTR: %s -> %s", chosenAddr.IP, fqdn)
	if err := gcpClient.SetInstancePTR(ctx, zone, instanceName, chosenAddr.IP, fqdn); err != nil {
		log("WARN", "SetInstancePTR 失败（非致命）: %v", err)
	}

	// 10. 完成
	_, _ = db.ExecContext(ctx, `UPDATE vps_instances SET deploy_status='success', deploy_error='' WHERE id=?`, vpsID)
	log("INFO", "slot=%d 部署完成: %s -> %s", slot, fqdn, chosenAddr.IP)
	return nil
}

var dkimLineRe = regexp.MustCompile(`(?m)^\s*DKIM_PUBLIC_KEY\s*=\s*(.+?)\s*$`)

func extractDKIMPublicKey(out string) string {
	m := dkimLineRe.FindStringSubmatch(out)
	if len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// loadGCPClient 按 credID 从 gcp_credentials 表加载并构造 gcp.Client
func loadGCPClient(ctx context.Context, credID string) (*gcp.Client, error) {
	db := store.DB()
	if db == nil {
		return nil, fmt.Errorf("数据库未就绪")
	}
	var (
		name, authType, projectID string
		encBlob                   []byte
	)
	row := db.QueryRowContext(ctx,
		`SELECT name, auth_type, project_id, encrypted_blob FROM gcp_credentials WHERE id=?`, credID)
	if err := row.Scan(&name, &authType, &projectID, &encBlob); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("未找到 GCP 凭证 id=%s", credID)
		}
		return nil, err
	}
	var blob []byte
	if len(encBlob) > 0 {
		dec, err := crypto.Decrypt(encBlob)
		if err != nil {
			return nil, fmt.Errorf("解密 GCP 凭证失败: %w", err)
		}
		blob = dec
	}
	cred := gcp.Credential{
		ID:        credID,
		Name:      name,
		AuthType:  gcp.AuthType(authType),
		ProjectID: projectID,
		Blob:      blob,
	}
	return gcp.NewClient(ctx, cred)
}

// loadFirewallAllowlist 读取指定 GCP 凭证的防火墙白名单 CIDR 列表。
// 表里没有记录或为空数组时返回 nil（即维持全开）。
func loadFirewallAllowlist(credID string) []string {
	db := store.DB()
	if db == nil {
		return nil
	}
	var raw string
	row := db.QueryRow(`SELECT allowed_ips FROM gcp_firewall_allowlist WHERE cred_id=?`, credID)
	if err := row.Scan(&raw); err != nil {
		return nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var ips []string
	if err := json.Unmarshal([]byte(raw), &ips); err != nil {
		return nil
	}
	if len(ips) == 0 {
		return nil
	}
	return ips
}

// loadAliyunDns 按 credID 加载阿里云凭证并返回 AliyunDns
func loadAliyunDns(credID string) (*dns.AliyunDns, error) {
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
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("未找到阿里云凭证 id=%s", credID)
		}
		return nil, err
	}
	sk, err := crypto.Decrypt(encSec)
	if err != nil {
		return nil, fmt.Errorf("解密阿里云 SK 失败: %w", err)
	}
	return dns.NewAliyunDns(ak, string(sk)), nil
}

// loadTemplate 加载模板
func loadTemplate(id string) (VPSTemplate, error) {
	db := store.DB()
	if db == nil {
		return VPSTemplate{}, fmt.Errorf("数据库未就绪")
	}
	var (
		name, regionsJSON, machineType, imageFamily, imageProject, diskType, tagsJSON, metadataScript, rootPwd, deployType string
		autoSpread                                                                                                         int
		diskSize                                                                                                           int64
	)
	row := db.QueryRow(
		`SELECT name, regions_json, auto_spread, machine_type, image_family, image_project, disk_size_gb, COALESCE(disk_type,'pd-balanced'), tags_json, COALESCE(metadata_script,''), root_password, COALESCE(deploy_type,'kumomta') FROM vps_templates WHERE id=?`, id)
	if err := row.Scan(&name, &regionsJSON, &autoSpread, &machineType, &imageFamily, &imageProject, &diskSize, &diskType, &tagsJSON, &metadataScript, &rootPwd, &deployType); err != nil {
		if err == sql.ErrNoRows {
			return VPSTemplate{}, fmt.Errorf("未找到模板 id=%s", id)
		}
		return VPSTemplate{}, err
	}
	tmpl := VPSTemplate{
		ID:             id,
		Name:           name,
		AutoSpread:     autoSpread == 1,
		MachineType:    machineType,
		ImageFamily:    imageFamily,
		ImageProject:   imageProject,
		DiskSizeGB:     diskSize,
		DiskType:       diskType,
		MetadataScript: metadataScript,
		RootPassword:   rootPwd,
		DeployType:     deployType,
	}
	if regionsJSON != "" {
		_ = json.Unmarshal([]byte(regionsJSON), &tmpl.Regions)
	}
	if tagsJSON != "" {
		_ = json.Unmarshal([]byte(tagsJSON), &tmpl.Tags)
	}
	return tmpl, nil
}
