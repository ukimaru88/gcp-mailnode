package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gcp-mailnode/internal/parser"
	"gcp-mailnode/internal/ssh"
	"gcp-mailnode/internal/sshkey"
)

type ServerStatusDTO struct {
	VPSID           string             `json:"vps_id"`
	Name            string             `json:"name"`
	IP              string             `json:"ip"`
	FQDN            string             `json:"fqdn"`
	Zone            string             `json:"zone"`
	CheckedAt       string             `json:"checked_at"`
	ServiceActive   bool               `json:"service_active"`
	ServiceState    string             `json:"service_state"`
	ServiceEnabled  string             `json:"service_enabled"`
	Uptime          string             `json:"uptime"`
	Ports           []string           `json:"ports"`
	LoadAverage     string             `json:"load_average"`
	RootDiskUsed    string             `json:"root_disk_used"`
	SpoolDiskUsed   string             `json:"spool_disk_used"`
	QueueFiles      int                `json:"queue_files"`
	QueueBytes      int64              `json:"queue_bytes"`
	QueueBytesHuman string             `json:"queue_bytes_human"`
	MetaFiles       int                `json:"meta_files"`
	DataFiles       int                `json:"data_files"`
	LogFilesScanned int                `json:"log_files_scanned"`
	LastLogFile     string             `json:"last_log_file"`
	Submitted       int                `json:"submitted"`
	Delivered       int                `json:"delivered"`
	Bounced         int                `json:"bounced"`
	Deferred        int                `json:"deferred"`
	UniqueDomains   int                `json:"unique_domains"`
	TopDomains      []ServerCounterDTO `json:"top_domains"`
	BounceReasons   []ServerReasonDTO  `json:"bounce_reasons"`
	// v0.2.33：深度诊断字段
	DeferredReasons  []ServerReasonDTO  `json:"deferred_reasons"`           // 临时失败 (4xx) 原因分类
	RecentSmtpReplies []SmtpReplySample `json:"recent_smtp_replies"`        // 最近真实 SMTP 响应原话样本
	QueueSummary     string             `json:"queue_summary"`              // kcli queue-summary 输出原文
	TopQueueDomains  []ServerCounterDTO `json:"top_queue_domains"`          // 队列堵塞 Top 域名（解析 queue-summary）
	RecentErrors    []string           `json:"recent_errors"`
	Recommendations []string           `json:"recommendations"`
	RawStatus       map[string]string  `json:"raw_status"`
}

// SmtpReplySample 最近一条 SMTP 真实响应样本（对方服务器返回的原话）
type SmtpReplySample struct {
	Time      string `json:"time"`       // 时间
	Domain    string `json:"domain"`     // 收件域名
	Recipient string `json:"recipient"`  // 收件人（部分脱敏）
	Kind      string `json:"kind"`       // "Bounce" / "Deferred"
	Code      int    `json:"code"`       // SMTP 状态码
	Content   string `json:"content"`    // 对方服务器响应原文（截断）
}

type ServerCounterDTO struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type ServerReasonDTO struct {
	Reason     string             `json:"reason"`
	Count      int                `json:"count"`
	TopDomains []ServerCounterDTO `json:"top_domains"`
	Sample     string             `json:"sample"`
}

type kumoStatusEvent struct {
	Type      string `json:"type"`
	Recipient string `json:"recipient"`
	Queue     string `json:"queue"`
	Timestamp int64  `json:"timestamp"`
	Response  struct {
		Code    int    `json:"code"`
		Content string `json:"content"`
	} `json:"response"`
}

func (a *App) GetServerStatus(vpsID string, logFiles int) (ServerStatusDTO, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(vpsID) == "" {
		return ServerStatusDTO{}, fmt.Errorf("请选择 VPS")
	}
	if logFiles <= 0 {
		logFiles = 8
	}
	if logFiles > 30 {
		logFiles = 30
	}
	db, err := requireDB()
	if err != nil {
		return ServerStatusDTO{}, err
	}

	var deployType string
	res := ServerStatusDTO{VPSID: vpsID, CheckedAt: time.Now().Format(time.RFC3339)}
	err = db.QueryRowContext(ctx, `SELECT name, ip, fqdn, zone, COALESCE(deploy_type,'kumomta')
		FROM vps_instances WHERE id=?`, vpsID).Scan(&res.Name, &res.IP, &res.FQDN, &res.Zone, &deployType)
	if err != nil {
		return ServerStatusDTO{}, err
	}
	if deployType != "" && deployType != "kumomta" {
		return ServerStatusDTO{}, fmt.Errorf("该 VPS deploy_type=%s，不是 KumoMTA 节点", deployType)
	}
	if strings.TrimSpace(res.IP) == "" {
		return ServerStatusDTO{}, fmt.Errorf("VPS 缺少外网 IP")
	}

	sshCfg := ssh.Config{Host: res.IP, Port: 22, Username: "root", KeyContent: string(sshkey.PrivatePEM())}
	statusOut, err := ssh.RunCommand(ctx, sshCfg, serverStatusCommand())
	if err != nil {
		return ServerStatusDTO{}, err
	}
	res.RawStatus = parseStatusKV(statusOut)
	applyStatusFields(&res)

	logContent, scanned, lastFile, logErr := readRecentKumoStatusLogs(ctx, sshCfg, logFiles)
	res.LogFilesScanned = scanned
	res.LastLogFile = lastFile
	if logErr != nil {
		res.RecentErrors = append(res.RecentErrors, "读取 KumoMTA 日志失败: "+logErr.Error())
	} else {
		applyKumoLogStats(&res, logContent)
	}
	res.Recommendations = buildServerRecommendations(res)
	return res, nil
}

func serverStatusCommand() string {
	return `set +e
printf 'service_state=%s\n' "$(systemctl is-active kumomta 2>/dev/null || true)"
printf 'service_enabled=%s\n' "$(systemctl is-enabled kumomta 2>/dev/null || true)"
printf 'active_since=%s\n' "$(systemctl show kumomta -p ActiveEnterTimestamp --value 2>/dev/null || true)"
printf 'ports=%s\n' "$(ss -tlnp 2>/dev/null | grep -E ':(25|465|587)\b' | tr '\n' '|' || true)"
printf 'load_average=%s\n' "$(cut -d' ' -f1-3 /proc/loadavg 2>/dev/null || true)"
printf 'root_disk=%s\n' "$(df -P / 2>/dev/null | awk 'NR==2{print $5 " used, free " $4 " KB"}')"
printf 'spool_disk=%s\n' "$(df -P /var/spool/kumomta 2>/dev/null | awk 'NR==2{print $5 " used, free " $4 " KB"}')"
printf 'queue_files=%s\n' "$(find /var/spool/kumomta -type f 2>/dev/null | wc -l)"
printf 'queue_bytes=%s\n' "$(du -sb /var/spool/kumomta 2>/dev/null | awk '{print $1}')"
printf 'meta_files=%s\n' "$(find /var/spool/kumomta/meta -type f 2>/dev/null | wc -l)"
printf 'data_files=%s\n' "$(find /var/spool/kumomta/data -type f 2>/dev/null | wc -l)"
printf 'recent_journal=%s\n' "$(journalctl -u kumomta --since '1 hour ago' -p warning --no-pager -n 20 2>/dev/null | tr '\n' '|' | sed 's/=/ /g')"

# v0.2.33：kcli queue-summary —— 看哪些队列堵塞最多
KCLI=""
for p in /opt/kumomta/sbin/kcli /opt/kumomta/bin/kcli /usr/bin/kcli /usr/local/bin/kcli $(command -v kcli 2>/dev/null); do
  if [ -x "$p" ]; then KCLI="$p"; break; fi
done
if [ -n "$KCLI" ]; then
  qs=$("$KCLI" queue-summary 2>&1 | head -100)
  # v0.2.34：用 base64 避免特殊字符破坏 kv 解析；用 printf '%s' 而非 '%%s'
  # （这是 raw string 不经 fmt.Sprintf，'%%s' 在 shell 里是字面 '%s' 不替换 $qs）
  printf 'queue_summary_b64=%s\n' "$(printf '%s' "$qs" | base64 -w0 2>/dev/null)"
else
  printf 'queue_summary_b64=%s\n' "$(printf '%s' 'kcli not found in PATH or /opt/kumomta/{sbin,bin}/' | base64 -w0)"
fi
`
}

func readRecentKumoStatusLogs(ctx context.Context, cfg ssh.Config, maxFiles int) (string, int, string, error) {
	// v0.1.82：跳过 active segment（zstd 流式压缩，未 close 不可读），只解已归档文件。
	// 取最近 N 个文件中的"非最新"那部分（最新 = active，跳过）。
	// 配合 init.lua 的 max_segment_duration='5 minutes'，最坏 5 分钟延迟看到数据。
	cmd := fmt.Sprintf(`set +e
if ! command -v zstd >/dev/null 2>&1; then echo "__ERR__missing zstd"; exit 0; fi
if [ ! -d /var/log/kumomta ]; then echo "__ERR__/var/log/kumomta not found"; exit 0; fi
cd /var/log/kumomta || exit 0
all=$(ls -1 2>/dev/null | sort)
total=$(printf '%%s\n' "$all" | sed '/^$/d' | wc -l)
# 跳过最新（active），只取前 (total-1) 中的最近 N 个
files=$(printf '%%s\n' "$all" | sed '/^$/d' | head -n -1 | tail -n %d)
count=$(printf '%%s\n' "$files" | sed '/^$/d' | wc -l)
last=$(printf '%%s\n' "$files" | sed '/^$/d' | tail -n 1)
printf "__META__count=%%s last=%%s total=%%s\n" "$count" "$last" "$total"
printf '%%s\n' "$files" | sed '/^$/d' | while read f; do zstd -dcq "$f" 2>/dev/null || true; done`, maxFiles)
	out, err := ssh.RunCommand(ctx, cfg, cmd)
	if err != nil {
		return "", 0, "", err
	}
	if strings.Contains(out, "__ERR__") {
		return "", 0, "", fmt.Errorf("%s", strings.TrimSpace(strings.TrimPrefix(out, "__ERR__")))
	}
	scanned := 0
	last := ""
	lines := strings.SplitN(out, "\n", 2)
	if len(lines) > 0 && strings.HasPrefix(lines[0], "__META__") {
		fields := strings.Fields(strings.TrimPrefix(lines[0], "__META__"))
		for _, f := range fields {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			switch k {
			case "count":
				scanned, _ = strconv.Atoi(v)
			case "last":
				last = v
			}
		}
		if len(lines) == 2 {
			return lines[1], scanned, last, nil
		}
		return "", scanned, last, nil
	}
	return out, scanned, last, nil
}

func parseStatusKV(out string) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if ok {
			m[k] = strings.TrimSpace(v)
		}
	}
	return m
}

func applyStatusFields(res *ServerStatusDTO) {
	m := res.RawStatus
	res.ServiceState = m["service_state"]
	res.ServiceActive = res.ServiceState == "active"
	res.ServiceEnabled = m["service_enabled"]
	res.Uptime = strings.TrimSpace(m["active_since"])
	res.LoadAverage = m["load_average"]
	res.RootDiskUsed = m["root_disk"]
	res.SpoolDiskUsed = m["spool_disk"]
	res.QueueFiles = atoi(m["queue_files"])
	res.QueueBytes = atoi64(m["queue_bytes"])
	res.QueueBytesHuman = humanBytes(res.QueueBytes)
	res.MetaFiles = atoi(m["meta_files"])
	res.DataFiles = atoi(m["data_files"])
	if ports := strings.Trim(m["ports"], "|"); ports != "" {
		for _, p := range strings.Split(ports, "|") {
			p = strings.TrimSpace(p)
			if p != "" {
				res.Ports = append(res.Ports, p)
			}
		}
	}
	if j := strings.Trim(m["recent_journal"], "|"); j != "" {
		for _, line := range strings.Split(j, "|") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.Contains(line, "-- No entries --") {
				res.RecentErrors = append(res.RecentErrors, line)
			}
		}
	}
	// v0.2.33：kcli queue-summary 解 base64
	if b64 := strings.TrimSpace(m["queue_summary_b64"]); b64 != "" {
		if decoded, err := base64Decode(b64); err == nil {
			res.QueueSummary = strings.TrimSpace(decoded)
			res.TopQueueDomains = parseQueueSummaryTop(decoded, 12)
		} else {
			res.QueueSummary = "(解析 queue-summary 失败: " + err.Error() + ")"
		}
	}
}

func base64Decode(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parseQueueSummaryTop 解析 kcli queue-summary 输出，提取队列长度 Top N 域名。
// kcli queue-summary 输出格式（示例，可能因版本不同）：
//
//	ScheduledQueue                    Count    NextDue
//	plala.or.jp                       5234     ...
//	ocn.ne.jp                         3120     ...
func parseQueueSummaryTop(out string, limit int) []ServerCounterDTO {
	counts := map[string]int{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "Scheduled") ||
			strings.HasPrefix(line, "Ready") || strings.HasPrefix(line, "Source") ||
			strings.HasPrefix(line, "Site") || strings.HasPrefix(line, "MX") ||
			strings.HasPrefix(line, "===") || strings.HasPrefix(line, "---") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		// 找一列纯数字作为 count
		for _, f := range fields[1:] {
			if n, err := strconv.Atoi(f); err == nil && n > 0 {
				if strings.Contains(name, ".") || strings.Contains(name, "@") {
					counts[name] += n
				}
				break
			}
		}
	}
	return topCounters(counts, limit)
}

func applyKumoLogStats(res *ServerStatusDTO, content string) {
	domainCounts := map[string]int{}
	reasonCounts := map[string]int{}
	reasonDomains := map[string]map[string]int{}
	reasonSamples := map[string]string{}
	seenDomains := map[string]struct{}{}

	// v0.2.33：临时失败（Deferred）也分类
	defReasonCounts := map[string]int{}
	defReasonDomains := map[string]map[string]int{}
	defReasonSamples := map[string]string{}

	// v0.2.33：收集最近真实 SMTP 响应样本（Bounce + Deferred）
	var allReplies []SmtpReplySample

	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev kumoStatusEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		domain := eventDomain(ev)
		if domain != "" {
			domainCounts[domain]++
			seenDomains[domain] = struct{}{}
		}
		switch ev.Type {
		case "Reception":
			res.Submitted++
		case "Delivery":
			res.Delivered++
		case "Bounce":
			res.Bounced++
			reason := parser.ClassifyKumoBounce(ev.Response.Code, ev.Response.Content)
			reasonCounts[reason]++
			if reasonDomains[reason] == nil {
				reasonDomains[reason] = map[string]int{}
			}
			if domain != "" {
				reasonDomains[reason][domain]++
			}
			if reasonSamples[reason] == "" {
				reasonSamples[reason] = truncate(ev.Response.Content, 180)
			}
			if ev.Response.Code > 0 || ev.Response.Content != "" {
				allReplies = append(allReplies, SmtpReplySample{
					Time:      kumoTimestamp(ev.Timestamp),
					Domain:    domain,
					Recipient: maskRecipient(ev.Recipient),
					Kind:      "Bounce",
					Code:      ev.Response.Code,
					Content:   truncate(ev.Response.Content, 240),
				})
			}
		case "TransientFailure":
			res.Deferred++
			reason := parser.ClassifyKumoBounce(ev.Response.Code, ev.Response.Content)
			defReasonCounts[reason]++
			if defReasonDomains[reason] == nil {
				defReasonDomains[reason] = map[string]int{}
			}
			if domain != "" {
				defReasonDomains[reason][domain]++
			}
			if defReasonSamples[reason] == "" {
				defReasonSamples[reason] = truncate(ev.Response.Content, 180)
			}
			if ev.Response.Code > 0 || ev.Response.Content != "" {
				allReplies = append(allReplies, SmtpReplySample{
					Time:      kumoTimestamp(ev.Timestamp),
					Domain:    domain,
					Recipient: maskRecipient(ev.Recipient),
					Kind:      "Deferred",
					Code:      ev.Response.Code,
					Content:   truncate(ev.Response.Content, 240),
				})
			}
		}
	}
	res.UniqueDomains = len(seenDomains)
	res.TopDomains = topCounters(domainCounts, 12)
	for reason, count := range reasonCounts {
		res.BounceReasons = append(res.BounceReasons, ServerReasonDTO{
			Reason:     reason,
			Count:      count,
			TopDomains: topCounters(reasonDomains[reason], 5),
			Sample:     reasonSamples[reason],
		})
	}
	sort.Slice(res.BounceReasons, func(i, j int) bool {
		return res.BounceReasons[i].Count > res.BounceReasons[j].Count
	})
	for reason, count := range defReasonCounts {
		res.DeferredReasons = append(res.DeferredReasons, ServerReasonDTO{
			Reason:     reason,
			Count:      count,
			TopDomains: topCounters(defReasonDomains[reason], 5),
			Sample:     defReasonSamples[reason],
		})
	}
	sort.Slice(res.DeferredReasons, func(i, j int) bool {
		return res.DeferredReasons[i].Count > res.DeferredReasons[j].Count
	})
	// 按时间倒序，取最近 30 条响应原话样本
	sort.Slice(allReplies, func(i, j int) bool { return allReplies[i].Time > allReplies[j].Time })
	if len(allReplies) > 30 {
		allReplies = allReplies[:30]
	}
	res.RecentSmtpReplies = allReplies
}

// kumoTimestamp 把 KumoMTA 日志的 unix 秒时间戳转成本地 HH:MM:SS
func kumoTimestamp(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).Format("15:04:05")
}

// maskRecipient 把收件人脱敏（保留前 3 字符 + ***@domain）
func maskRecipient(r string) string {
	r = strings.TrimSpace(r)
	at := strings.LastIndex(r, "@")
	if at < 0 {
		return r
	}
	local := r[:at]
	if len(local) <= 3 {
		return local + "***" + r[at:]
	}
	return local[:3] + "***" + r[at:]
}

func eventDomain(ev kumoStatusEvent) string {
	if ev.Queue != "" && strings.Contains(ev.Queue, ".") {
		return strings.ToLower(strings.TrimSpace(ev.Queue))
	}
	if at := strings.LastIndex(ev.Recipient, "@"); at >= 0 {
		return strings.ToLower(strings.TrimSpace(ev.Recipient[at+1:]))
	}
	return ""
}

func topCounters(m map[string]int, limit int) []ServerCounterDTO {
	out := make([]ServerCounterDTO, 0, len(m))
	for k, v := range m {
		out = append(out, ServerCounterDTO{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Name < out[j].Name
		}
		return out[i].Count > out[j].Count
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func buildServerRecommendations(res ServerStatusDTO) []string {
	out := []string{}
	if !res.ServiceActive {
		out = append(out, "KumoMTA 服务未 active：先看 journal 错误，必要时重启服务或重新部署配置。")
	}
	has587 := false
	for _, p := range res.Ports {
		if strings.Contains(p, ":587") {
			has587 = true
			break
		}
	}
	if !has587 {
		out = append(out, "587 未监听：外部 SMTP 提交会失败，优先检查 KumoMTA init.lua 和 systemctl status。")
	}
	if res.QueueFiles > 1000 {
		out = append(out, "队列文件较多：观察是否持续增长；若增长，检查目标域临时拒绝、DNS、出口连通和发送速率。")
	}
	totalFinal := res.Delivered + res.Bounced
	if totalFinal > 0 {
		rate := float64(res.Bounced) / float64(totalFinal)
		if rate >= 0.2 {
			out = append(out, fmt.Sprintf("退信率 %.1f%% 偏高：优先处理 Top 退信原因和对应域名。", rate*100))
		}
	}
	if len(res.BounceReasons) > 0 {
		top := res.BounceReasons[0]
		if strings.Contains(top.Reason, "收件人不存在") {
			out = append(out, "收件人不存在较多：建议清理地址质量，避免继续给无效地址投递。")
		} else if strings.Contains(top.Reason, "IP/域名被封") || strings.Contains(top.Reason, "被判为垃圾") {
			out = append(out, "出现封锁/垃圾判定：建议暂停对应域名流量，检查 SPF/DKIM/DMARC、内容和 IP 声誉。")
		} else if strings.Contains(top.Reason, "临时拒绝") {
			out = append(out, "临时拒绝较多：建议降低并发/速率，等待队列重试后再观察。")
		}
	}
	if len(out) == 0 {
		out = append(out, "当前服务、端口、队列和最近日志没有明显异常。")
	}
	return out
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func humanBytes(v int64) string {
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%d B", v)
	}
	div, exp := int64(unit), 0
	for n := v / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(v)/float64(div), "KMGTPE"[exp])
}
