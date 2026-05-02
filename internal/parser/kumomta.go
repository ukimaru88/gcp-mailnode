package parser

import (
	"bufio"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// KumoMTA 日志事件（只列关心的字段，其他字段 JSON decoder 忽略）
// 对应 KumoMTA 2026.03+ 的 JSON 行格式
type kumoEvent struct {
	Type      string `json:"type"`      // Reception / Bounce / TransientFailure / Delivery
	Sender    string `json:"sender"`    // envelope from
	Recipient string `json:"recipient"` // envelope to
	Queue     string `json:"queue"`     // 一般是收件方域名
	Response  struct {
		Code    int    `json:"code"`    // SMTP 状态码
		Content string `json:"content"` // 返回的文本（含错误描述）
	} `json:"response"`
	// KumoMTA 需要在 init.lua 里把 X-BM-* 加入 trace_headers 才会写进 log。
	// 兼容两种格式：map 形式 {"X-BM-Task-ID": "123"} 和数组形式 [["X-BM-Task-ID","123"], …]。
	// 这里只解 map 形式；数组形式不常见，额外的字段 JSON decoder 会忽略。
	Headers map[string]string `json:"headers"`
}

// extractBMTaskID 从 kumoEvent 的 headers 里取 X-BM-Task-ID（大小写不敏感）。
// 没有则返回空串。
func extractBMTaskID(h map[string]string) string {
	if h == nil {
		return ""
	}
	for k, v := range h {
		if strings.EqualFold(k, "X-BM-Task-ID") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ParseKumoMTAStream 从一个 reader（通常是 zstd 解压流，逐字节读 JSON 行）解析
// KumoMTA 日志事件，返回与 ParseMailLog 结构一致的 ParseResult，方便复用现有 UI。
//
// 语义对应：
//
//	Reception       → 进入 KumoMTA 的一次接收（相当于 Postfix 的 sent 统计）
//	Delivery        → 成功投递到远端 MX
//	Bounce          → 硬退信，记入 BouncedLines 并按 SMTP 码分类
//	TransientFailure → 软退信 deferred
//
// 同一封邮件的生命周期会产生多个事件（Reception + Delivery 或 Reception + Bounce）。
// 邮箱导出只认 Delivery 事件，避免把已接收但后来退信的收件人写入结果。
func ParseKumoMTAStream(r io.Reader) ParseResult {
	result := ParseResult{}
	seen := make(map[string]struct{})
	bounceCounts := make(map[string]int)
	bounceDomains := make(map[string]map[string]int)
	// X-BM-Task-ID 维度统计：taskID → Reception/Bounce/Deferred 计数
	bmTasks := make(map[string]*BMTaskStat)

	scanner := bufio.NewScanner(r)
	// KumoMTA 的 JSON 行可能很长（含完整错误文本），提高 buffer 上限
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		result.TotalLines++

		var ev kumoEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}

		taskID := extractBMTaskID(ev.Headers)
		ensureBM := func() *BMTaskStat {
			if taskID == "" {
				return nil
			}
			st, ok := bmTasks[taskID]
			if !ok {
				st = &BMTaskStat{TaskID: taskID}
				bmTasks[taskID] = st
			}
			return st
		}

		switch ev.Type {
		case "Reception":
			// 相当于 Postfix status=sent，邮件进入 MTA 系统并被接收
			result.SentLines++
			if st := ensureBM(); st != nil {
				st.Reception++
			}
		case "Delivery":
			// 成功投递到远端 MX（250 OK），只有这类收件人进入导出邮箱集合。
			if ev.Recipient != "" {
				addEmail(strings.ToLower(strings.TrimSpace(ev.Recipient)), &result, seen)
			}
		case "Bounce":
			result.BouncedLines++
			reason := ClassifyKumoBounce(ev.Response.Code, ev.Response.Content)
			bounceCounts[reason]++

			if at := strings.LastIndex(ev.Recipient, "@"); at >= 0 {
				domain := strings.ToLower(ev.Recipient[at+1:])
				if bounceDomains[reason] == nil {
					bounceDomains[reason] = make(map[string]int)
				}
				bounceDomains[reason][domain]++
			}
			if st := ensureBM(); st != nil {
				st.Bounce++
			}
		case "TransientFailure":
			result.DeferredLines++
			if st := ensureBM(); st != nil {
				st.Deferred++
			}
		}
	}

	// 汇总 X-BM-Task-ID 维度统计（按 Bounce 降序，方便前端看"哪个任务被退信最多"）
	for _, st := range bmTasks {
		result.BMTaskBreakdown = append(result.BMTaskBreakdown, *st)
	}
	for i := 0; i < len(result.BMTaskBreakdown); i++ {
		for j := i + 1; j < len(result.BMTaskBreakdown); j++ {
			if result.BMTaskBreakdown[j].Bounce > result.BMTaskBreakdown[i].Bounce {
				result.BMTaskBreakdown[i], result.BMTaskBreakdown[j] = result.BMTaskBreakdown[j], result.BMTaskBreakdown[i]
			}
		}
	}

	// 复用 Postfix 分类的汇总逻辑：转成排序列表
	for reason, count := range bounceCounts {
		cat := BounceCategory{Reason: reason, Count: count}
		if dm, ok := bounceDomains[reason]; ok {
			for domain, cnt := range dm {
				cat.TopDomains = append(cat.TopDomains, BounceDomain{Domain: domain, Count: cnt})
			}
			for i := 0; i < len(cat.TopDomains); i++ {
				for j := i + 1; j < len(cat.TopDomains); j++ {
					if cat.TopDomains[j].Count > cat.TopDomains[i].Count {
						cat.TopDomains[i], cat.TopDomains[j] = cat.TopDomains[j], cat.TopDomains[i]
					}
				}
			}
			if len(cat.TopDomains) > 5 {
				cat.TopDomains = cat.TopDomains[:5]
			}
		}
		result.BounceBreakdown = append(result.BounceBreakdown, cat)
	}
	for i := 0; i < len(result.BounceBreakdown); i++ {
		for j := i + 1; j < len(result.BounceBreakdown); j++ {
			if result.BounceBreakdown[j].Count > result.BounceBreakdown[i].Count {
				result.BounceBreakdown[i], result.BounceBreakdown[j] = result.BounceBreakdown[j], result.BounceBreakdown[i]
			}
		}
	}

	return result
}

// DecompressZstd 把一个 zstd 压缩的字节流解压成明文 reader
// 调用方负责 Close 返回的 reader
func DecompressZstd(r io.Reader) (io.ReadCloser, error) {
	dec, err := zstd.NewReader(r)
	if err != nil {
		return nil, err
	}
	return dec.IOReadCloser(), nil
}

// ClassifyKumoBounce 把 KumoMTA 的 Bounce 事件分类成可读原因
// KumoMTA 的 response.content 往往包含目标 MX 返回的完整文本，语义上和 Postfix
// 日志里的 "said: ..." 一致，所以分类规则可以保持一致。
func ClassifyKumoBounce(code int, content string) string {
	lower := strings.ToLower(content)
	codeStr := strconv.Itoa(code)

	switch code {
	case 550:
		if strings.Contains(lower, "user unknown") || strings.Contains(lower, "unknown user") ||
			strings.Contains(lower, "invalid recipient") || strings.Contains(lower, "no such user") ||
			strings.Contains(lower, "does not exist") || strings.Contains(lower, "recipient rejected") ||
			strings.Contains(lower, "no such mailbox") {
			return "收件人不存在 (550)"
		}
		if strings.Contains(lower, "rfc 5322") || strings.Contains(lower, "multiple date") ||
			strings.Contains(lower, "not compliant") {
			return "邮件格式违例 (550)"
		}
		if strings.Contains(lower, "spamhaus") || strings.Contains(lower, "blacklist") ||
			strings.Contains(lower, "blocked") || strings.Contains(lower, "rejected") {
			return "IP/域名被封 (550)"
		}
		if strings.Contains(lower, "dmarc") || strings.Contains(lower, "spf") ||
			strings.Contains(lower, "dkim") {
			return "认证失败 (550)"
		}
		return "被拒绝 (550)"
	case 551:
		return "用户不在本地 (551)"
	case 552:
		return "邮箱已满 (552)"
	case 553:
		return "地址格式错误 (553)"
	case 554:
		if strings.Contains(lower, "spam") {
			return "被判为垃圾 (554)"
		}
		return "事务失败 (554)"
	case 421, 450, 451, 452:
		return "临时拒绝 (" + codeStr + ")"
	case 400:
		// KumoMTA 自己的内部错误（比如连不上 MX、TLS 握手失败）
		if strings.Contains(lower, "connect") || strings.Contains(lower, "connection") {
			return "连接失败 (内部 400)"
		}
		if strings.Contains(lower, "tls") || strings.Contains(lower, "certificate") {
			return "TLS 失败 (内部 400)"
		}
		return "投递失败 (内部 400)"
	default:
		if code >= 500 {
			return "永久失败 (" + codeStr + ")"
		}
		return "其他 (" + codeStr + ")"
	}
}
