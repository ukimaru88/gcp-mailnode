package parser

import (
	"regexp"
	"strings"
)

// ── Postfix 格式 ────────────────────────────────────────────────────────────────
// Apr  3 10:15:23 mail postfix/smtp[12345]: ABC123: to=<user@example.com>, status=sent
//
// ── Haraka (poste.io) 格式 ──────────────────────────────────────────────────────
// 入站→出站 RCPT TO（相同 transaction UUID）：
//   [PROTOCOL] [UUID.1.1] [outbound] C: RCPT TO:<user@example.com>
//   [NOTICE]   [UUID.1.1] [outbound]  delivered ... domain=example.com ... mode=SMTP
//
// 本地 LMTP 投递（UUID.1，不含 mode=SMTP）：
//   [NOTICE]   [UUID.1]   [outbound]  delivered ... host=127.0.0.1 mode=LMTP
//
// 同一个 transaction UUID 同时出现在 RCPT TO 行和 delivered NOTICE 行。
// 只提取 mode=SMTP 的外部投递成功记录。

var (
	// Postfix: to=<email> + status=sent/bounced/deferred
	postfixToRe       = regexp.MustCompile(`to=<([^>@\s]+@[^>\s]+)>`)
	postfixSentRe     = regexp.MustCompile(`status=sent\b`)
	postfixBouncedRe  = regexp.MustCompile(`status=bounced\b`)
	postfixDeferredRe = regexp.MustCompile(`status=deferred\b`)
	postfixSaidRe     = regexp.MustCompile(`said:\s*(\d{3})\s`)
	postfixDsnRe      = regexp.MustCompile(`dsn=([0-9.]+)`)

	// Haraka outbound RCPT TO: [PROTOCOL] [UUID.X.Y] [outbound] C: RCPT TO:<email>
	harakaRcptRe = regexp.MustCompile(`\[PROTOCOL\].*\bC:\s*RCPT TO:<([^>@\s]+@[^>\s]+)>`)

	// Haraka delivered NOTICE — external SMTP only (mode=SMTP, not LMTP local delivery)
	harakaDelivRe = regexp.MustCompile(`\[NOTICE\].*\[outbound\].*\bdelivered\b.*\bmode=SMTP\b`)

	// Transaction UUID with any number of .N suffixes: UUID, UUID.1, UUID.1.1 …
	txRe = regexp.MustCompile(`\[([0-9A-Fa-f\-]{36}(?:\.\d+)*)\]`)
)

// BounceDomain 退信域名统计
type BounceDomain struct {
	Domain string `json:"domain"`
	Count  int    `json:"count"`
}

// BounceCategory 退信分类
type BounceCategory struct {
	Reason    string         `json:"reason"`
	Count     int            `json:"count"`
	TopDomains []BounceDomain `json:"top_domains"`
}

// BMTaskStat brutal-mailer 任务维度统计（由 X-BM-Task-ID 头反查）
type BMTaskStat struct {
	TaskID    string `json:"task_id"`
	Reception int    `json:"reception"` // 接收事件数（相当于"提交给 MTA 的封数"）
	Bounce    int    `json:"bounce"`    // 硬退信
	Deferred  int    `json:"deferred"`  // 软退信
}

// ParseResult 解析结果
type ParseResult struct {
	Emails       []string // 去重后的邮箱列表
	TotalLines   int      // 总行数
	SentLines    int      // status=sent 行数
	BouncedLines int      // status=bounced 行数
	DeferredLines int     // status=deferred 行数
	BounceBreakdown []BounceCategory // 退信原因分类
	// BMTaskBreakdown 仅 KumoMTA 日志会填（依赖邮件头里带 X-BM-Task-ID）。
	// Postfix/Haraka 不写头，这里永远是空；前端自己判断是否展示。
	BMTaskBreakdown []BMTaskStat `json:"bm_task_breakdown,omitempty"`
}

// ParseMailLog 自动识别 Postfix / Haraka 日志格式并提取收件人邮箱
func ParseMailLog(content string) ParseResult {
	result := ParseResult{}
	seen := make(map[string]struct{})

	lines := strings.Split(content, "\n")
	result.TotalLines = len(lines)

	// 用前 200 行探测格式
	isHaraka := detectHaraka(lines)

	if isHaraka {
		parseHaraka(lines, &result, seen)
	} else {
		parsePostfix(lines, &result, seen)
	}

	return result
}

// detectHaraka 检测是否是 Haraka 日志格式
func detectHaraka(lines []string) bool {
	limit := 200
	if len(lines) < limit {
		limit = len(lines)
	}
	for _, l := range lines[:limit] {
		if strings.Contains(l, "[PROTOCOL]") || strings.Contains(l, "[outbound]") {
			return true
		}
	}
	return false
}

// parsePostfix 解析标准 Postfix mail.log
func parsePostfix(lines []string, result *ParseResult, seen map[string]struct{}) {
	bounceCounts := make(map[string]int)
	// reason → domain → count
	bounceDomains := make(map[string]map[string]int)

	for _, line := range lines {
		if line == "" {
			continue
		}

		// status=sent → 提取邮箱
		if postfixSentRe.MatchString(line) {
			result.SentLines++
			m := postfixToRe.FindStringSubmatch(line)
			if len(m) >= 2 {
				addEmail(strings.ToLower(strings.TrimSpace(m[1])), result, seen)
			}
			continue
		}

		// status=bounced → 统计原因 + 域名
		if postfixBouncedRe.MatchString(line) {
			result.BouncedLines++
			reason := classifyBounce(line)
			bounceCounts[reason]++

			// 提取收件人域名
			if m := postfixToRe.FindStringSubmatch(line); len(m) >= 2 {
				email := strings.ToLower(strings.TrimSpace(m[1]))
				if at := strings.LastIndex(email, "@"); at >= 0 {
					domain := email[at+1:]
					if bounceDomains[reason] == nil {
						bounceDomains[reason] = make(map[string]int)
					}
					bounceDomains[reason][domain]++
				}
			}
			continue
		}

		// status=deferred
		if postfixDeferredRe.MatchString(line) {
			result.DeferredLines++
		}
	}

	// 转换为排序后的分类列表
	for reason, count := range bounceCounts {
		cat := BounceCategory{Reason: reason, Count: count}

		// 提取 top 5 域名
		if dm, ok := bounceDomains[reason]; ok {
			for domain, cnt := range dm {
				cat.TopDomains = append(cat.TopDomains, BounceDomain{Domain: domain, Count: cnt})
			}
			// 按数量降序
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

	// 按数量降序排序
	for i := 0; i < len(result.BounceBreakdown); i++ {
		for j := i + 1; j < len(result.BounceBreakdown); j++ {
			if result.BounceBreakdown[j].Count > result.BounceBreakdown[i].Count {
				result.BounceBreakdown[i], result.BounceBreakdown[j] = result.BounceBreakdown[j], result.BounceBreakdown[i]
			}
		}
	}
}

// classifyBounce 将退信行分类为可读的原因
func classifyBounce(line string) string {
	lower := strings.ToLower(line)

	// 按 SMTP 状态码分类
	if m := postfixSaidRe.FindStringSubmatch(line); len(m) >= 2 {
		code := m[1]
		switch code {
		case "550":
			if strings.Contains(lower, "user unknown") || strings.Contains(lower, "unknown user") ||
				strings.Contains(lower, "invalid recipient") || strings.Contains(lower, "no such user") ||
				strings.Contains(lower, "does not exist") || strings.Contains(lower, "recipient rejected") {
				return "收件人不存在 (550)"
			}
			if strings.Contains(lower, "spamhaus") || strings.Contains(lower, "blocked") ||
				strings.Contains(lower, "blacklist") || strings.Contains(lower, "rejected") {
				return "IP/域名被封 (550)"
			}
			return "被拒绝 (550)"
		case "551":
			return "用户不在本地 (551)"
		case "552":
			return "邮箱已满 (552)"
		case "553":
			return "地址格式错误 (553)"
		case "554":
			return "事务失败 (554)"
		case "421", "450":
			return "临时拒绝 (" + code + ")"
		case "452":
			return "存储空间不足 (452)"
		}
		return "其他错误 (" + code + ")"
	}

	// 没有状态码，按关键词
	if strings.Contains(lower, "host not found") || strings.Contains(lower, "name or service not known") {
		return "域名无法解析"
	}
	if strings.Contains(lower, "connection timed out") || strings.Contains(lower, "connection refused") {
		return "连接超时/拒绝"
	}

	return "其他"
}

// parseHaraka 解析 Haraka (poste.io) haraka-submission 日志
//
// 策略：
//  1. 收集 [PROTOCOL] C: RCPT TO:<email> → txID → email 映射
//  2. 收集 [NOTICE] delivered mode=SMTP（外部 SMTP，非 LMTP 本地）→ txID 集合
//  3. 只输出 txID 在成功集合中的邮箱
//
// RCPT TO 行与 delivered NOTICE 行使用相同的 transaction UUID（UUID.1.1 格式）。
// 如果日志片段中完全没有 delivered 行（如纯增量片段），退化为收集全部 RCPT TO。
func parseHaraka(lines []string, result *ParseResult, seen map[string]struct{}) {
	txEmails := make(map[string][]string) // txID → []email
	deliveredTx := make(map[string]struct{})
	hasDelivered := false

	for _, line := range lines {
		if line == "" {
			continue
		}

		// RCPT TO 行
		if m := harakaRcptRe.FindStringSubmatch(line); len(m) >= 2 {
			result.SentLines++
			email := strings.ToLower(strings.TrimSpace(m[1]))
			if email == "" {
				continue
			}
			txm := txRe.FindStringSubmatch(line)
			if len(txm) >= 2 {
				txID := txm[1]
				txEmails[txID] = append(txEmails[txID], email)
			} else {
				// 无法提取 txID，直接收录
				addEmail(email, result, seen)
			}
			continue
		}

		// delivered NOTICE（仅外部 SMTP）
		if harakaDelivRe.MatchString(line) {
			hasDelivered = true
			if txm := txRe.FindStringSubmatch(line); len(txm) >= 2 {
				deliveredTx[txm[1]] = struct{}{}
			}
		}
	}

	// 合并：有 delivered 记录则只取成功投递的；否则退化为全量
	for txID, emails := range txEmails {
		if hasDelivered {
			if _, ok := deliveredTx[txID]; !ok {
				continue
			}
		}
		for _, email := range emails {
			addEmail(email, result, seen)
		}
	}
}

func addEmail(email string, result *ParseResult, seen map[string]struct{}) {
	if email == "" {
		return
	}
	if _, exists := seen[email]; exists {
		return
	}
	seen[email] = struct{}{}
	result.Emails = append(result.Emails, email)
}
