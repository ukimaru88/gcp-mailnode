package dnsbl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gcp-mailnode/internal/logger"
)

// v0.2.12：DoH (DNS over HTTPS) 共享客户端，走 443 端口绕开 53 端口封锁。
//
// 修的问题链：
//   - 原版：零值 net.Resolver → 系统 stub resolver（getaddrinfo / systemd-resolved）
//     → 25 个 RBL 并发查询被串行化，单 IP DNSBL ~25s
//   - 改 PreferGo + 直打公共 DNS：UDP/53 和 TCP/53 都被路由器封死（家用/企业常见，
//     强制 DNS 走本地路由 192.168.1.1），实测 timeout
//   - 最终方案：DoH 走 HTTPS/443，路由器无法拦截，Cloudflare JSON API 返回 ≤200ms
//
// 单 IP DNSBL 从 ~25s → ≤500ms（25 RBL 并发 HTTP + HTTP/2 连接复用）。
// 20 IP 整批从 ~60s → ~5s。
//
// Endpoint 轮询：Cloudflare + Google JSON API（都不过滤 DNSBL；Quad9 会过滤恶意域排除）。
var (
	dohEndpoints = []string{
		"https://1.1.1.1/dns-query",  // Cloudflare 主
		"https://1.0.0.1/dns-query",  // Cloudflare 备
		"https://dns.google/resolve", // Google
	}
	// v0.2.20：SpamRATS 系列 RBL 被 Cloudflare/Google DoH 限速（实测 RCODE=2 全失败），
	// 但 NextDNS 能查到（实测 0.5s 返回 LISTED [127.0.0.36]）。
	// 单查询路径，不参与主轮询。NextDNS 公共匿名端点速率限制：每 IP 每分钟数百次，
	// 单批 Stage A 筛 20 IP × 4 SpamRATS = 80 次查询，远在限额内。
	dohEndpointsSpamRATS = []string{
		"https://dns.nextdns.io",
	}
	dohRoundRobin uint64

	// HTTP/2 连接复用：MaxIdleConnsPerHost 大让 25 个并发查询共享 keep-alive，
	// 避免每次 TLS 握手（~100ms）；复用后单查询 ~50ms。
	dohHTTPClient = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 50,
			MaxConnsPerHost:     50,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}
)

// dohJSONResponse RFC 8484 兼容的 Cloudflare/Google JSON DNS API 返回结构。
type dohJSONResponse struct {
	Status int `json:"Status"` // 0=NOERROR, 3=NXDOMAIN
	Answer []struct {
		Name string `json:"name"`
		Type int    `json:"type"` // 1=A, 28=AAAA, 16=TXT, 5=CNAME
		TTL  int    `json:"TTL"`
		Data string `json:"data"`
	} `json:"Answer"`
}

// dohLookupOnce 单次 DoH 调用（指定 endpoint），不重试。
func dohLookupOnce(ctx context.Context, endpoint, name string) ([]string, error) {
	u := endpoint + "?name=" + url.QueryEscape(name) + "&type=A"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := dohHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DoH HTTP %d", resp.StatusCode)
	}
	var d dohJSONResponse
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	// NXDOMAIN（Status=3）：明确未命中
	if d.Status == 3 {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	// SERVFAIL/FORMERR/REFUSED/NOTIMP（Status 1/2/4/5）：服务异常，返回错误让上层重试
	if d.Status != 0 {
		return nil, fmt.Errorf("DoH RCODE=%d", d.Status)
	}
	var addrs []string
	for _, a := range d.Answer {
		if a.Type == 1 { // A 记录
			addrs = append(addrs, a.Data)
		}
	}
	if len(addrs) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return addrs, nil
}

// dohLookup 用 DoH JSON API 查 A 记录，全部 endpoint 串行重试。
// v0.2.17：之前单次 endpoint 失败就放弃，SpamRATS 系列 RBL 经 DoH 转发慢，
// 3s 单次超时常失败 → 漏报黑名单命中（mxtoolbox 显示 LISTED 但软件判 clean）。
// 改成三个 endpoint 串行重试（Cloudflare 主/备 + Google），每次独立超时；
// 任一返回 NXDOMAIN 或 NOERROR 立即返回，全部超时/异常才报错。
//
// 返回 *net.DNSError(IsNotFound=true) 表示 NXDOMAIN/未命中（与 net.Resolver 对齐）；
// 其他错误（全部 endpoint 都失败）视为查询失败（上层标 errorCount）。
func dohLookup(ctx context.Context, name string) ([]string, error) {
	// v0.2.20：SpamRATS 域名走 NextDNS（Cloudflare/Google 经它限速 RCODE=2）。
	endpoints := dohEndpoints
	if strings.HasSuffix(strings.ToLower(name), ".spamrats.com") {
		endpoints = dohEndpointsSpamRATS
	}

	// 起始 endpoint 轮询索引：避免热点
	startIdx := atomic.AddUint64(&dohRoundRobin, 1)

	var lastErr error
	const perTryTimeout = 2500 * time.Millisecond
	for i := 0; i < len(endpoints); i++ {
		endpoint := endpoints[(startIdx+uint64(i))%uint64(len(endpoints))]
		// 单次 endpoint 独立超时，不被上一次失败累计
		tryCtx, cancel := context.WithTimeout(ctx, perTryTimeout)
		addrs, err := dohLookupOnce(tryCtx, endpoint, name)
		cancel()
		if err == nil {
			return addrs, nil
		}
		// NXDOMAIN/未命中：明确结果，不再换 endpoint
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, err
		}
		// 上层 ctx 已 done：放弃后续 endpoint
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all DoH endpoints failed")
	}
	return nil, lastErr
}

// TestResolver 暴露 DoH 解析给外部基准/诊断用，不算稳定 API。
func TestResolver(ctx context.Context, host string) ([]string, error) {
	return dohLookup(ctx, host)
}

// Zone 表示一个 DNSBL 区。
type Zone struct {
	Name string
	Host string
	// v0.2.20：仅展示不参与 verdict 判定。用于 SpamRATS 这类对所有云厂商 IP 段一刀切
	// 列入（对 Gmail/Outlook 等主流邮箱投递无影响）的 RBL：用户能看到命中状态（mxtoolbox
	// 一致），但 IP 不会被判脏导致 Stage A 全军覆没。
	DisplayOnly bool
}

// DefaultZones 默认查询的 DNSBL 区。v0.1.9 扩充到 ~20 个主流 RBL，覆盖 mxtoolbox 主要结果。
// 选择标准：公共免费可查询，无需 API key；活跃在维护；mxtoolbox 默认会查。
// 故意排除的：Abusix/Invaluement（需付费订阅）、已停服的 Mailspike/Truncate 等。
var DefaultZones = []Zone{
	// Spamhaus 系（ZEN 已聚合 SBL+XBL+PBL+CSS+DROP）
	{Name: "Spamhaus ZEN", Host: "zen.spamhaus.org"},
	// Barracuda
	{Name: "Barracuda", Host: "b.barracudacentral.org"},
	// SpamCop
	{Name: "SpamCop", Host: "bl.spamcop.net"},
	// SORBS 系（dnsbl 聚合 + 几个子列表）
	{Name: "SORBS", Host: "dnsbl.sorbs.net"},
	{Name: "SORBS-SPAM", Host: "spam.dnsbl.sorbs.net"},
	{Name: "SORBS-WEB", Host: "web.dnsbl.sorbs.net"},
	{Name: "SORBS-ZOMBIE", Host: "zombie.dnsbl.sorbs.net"},
	// UCEPROTECT 三级
	{Name: "UCEPROTECT-L1", Host: "dnsbl-1.uceprotect.net"},
	{Name: "UCEPROTECT-L2", Host: "dnsbl-2.uceprotect.net"},
	{Name: "UCEPROTECT-L3", Host: "dnsbl-3.uceprotect.net"},
	// Passive Spam Block List
	{Name: "PSBL", Host: "psbl.surriel.com"},
	// Spamhaus CSS（单独查一次，虽然 ZEN 含了，额外印证）
	{Name: "Spamhaus CSS", Host: "css.spamhaus.org"},
	// Swinog
	{Name: "Swinog URIBL", Host: "uribl.swinog.ch"},
	// Nordspam
	{Name: "Nordspam BL", Host: "bl.nordspam.com"},
	// 0spam
	{Name: "0spam", Host: "bl.0spam.org"},
	// BlockedServers
	{Name: "BlockedServers", Host: "rbl.blockedservers.com"},
	// GBUdb Truncate
	{Name: "GBUdb Truncate", Host: "truncate.gbudb.net"},
	// SpfBL
	{Name: "SpfBL", Host: "bl.spfbl.net"},
	// Imp-SL（Interserver）
	{Name: "Interserver", Host: "rbl.interserver.net"},
	// JustSpam
	{Name: "JustSpam", Host: "bl.mailspike.net"},
	// ix.dnsbl
	{Name: "ixBL", Host: "ix.dnsbl.manitu.net"},
	// WPBL
	{Name: "WPBL", Host: "db.wpbl.info"},
	// v0.2.20：SpamRATS 系列加回。理由：用户实测要求 + NextDNS 能查到（v0.2.17 删因
	// Cloudflare/Google DoH 经 SpamRATS 限速 RCODE=2 全失败，但 NextDNS 路径通且 0.5s
	// 内返回）。dohLookup 对 .spamrats.com 域名特殊路由到 NextDNS。
	// 注：SpamRATS-Dyna 把所有云厂商 IP 段一刀切列为"动态"，对 Gmail/Outlook/Yahoo 投递
	// 无影响，但用户希望看到 mxtoolbox 一致的结果。
	{Name: "SpamRATS-All", Host: "all.spamrats.com", DisplayOnly: true},
	{Name: "SpamRATS-Dyna", Host: "dyna.spamrats.com", DisplayOnly: true},
	{Name: "SpamRATS-NoPtr", Host: "noptr.spamrats.com", DisplayOnly: true},
	{Name: "SpamRATS-Spam", Host: "spam.spamrats.com", DisplayOnly: true},
}

// ZoneResult 单个 zone 的查询结果。TXT 首版保留字段，始终为空串。
type ZoneResult struct {
	Zone Zone
	Hit  bool
	TXT  string
	Err  error
}

// CheckOptions 控制 Query / Decide 的行为。
type CheckOptions struct {
	Zones     []Zone
	Threshold int           // 命中几个判脏，默认 2
	Timeout   time.Duration // 每 zone 查询超时，默认 3s
}

// CheckResult 聚合一次 DNSBL 并行检测的结果。
type CheckResult struct {
	IP       string
	Clean    bool
	HitCount int      // 仅计参与判定的 RBL 命中数（不含 DisplayOnly）
	HitLists []string // 同上，仅参与判定的 RBL 名
	// v0.2.20：仅展示用的 RBL 命中（如 SpamRATS 系列），不参与 Clean 判定。
	// 用于让 UI 展示与 mxtoolbox 一致的全量命中状态，但不让 Stage A 因云 IP 通病
	// 一刀切误杀。
	DisplayOnlyHits []string
	Zones           []ZoneResult
}

const (
	defaultThreshold = 1 // v0.1.9：命中任何一个 RBL 即判脏，最严标准
	// v0.2.17：单 zone 上层超时 3s → 8s。DoH 走 HTTPS（443）有 TLS 握手开销 +
	// SpamRATS 等 RBL 经 DoH 转发慢，单次 2.5s 重试 3 个 endpoint 需要 ~7.5s 上限。
	// 之前 3s 让 SpamRATS-Dyna 等 RBL 全部超时，漏报黑名单命中。
	defaultTimeout = 8 * time.Second
)

// reverseIPv4 把 "1.2.3.4" 反转成 "4.3.2.1"。
func reverseIPv4(ip net.IP) (string, bool) {
	v4 := ip.To4()
	if v4 == nil {
		return "", false
	}
	return fmt.Sprintf("%d.%d.%d.%d", v4[3], v4[2], v4[1], v4[0]), true
}

// queryZone 查单个 zone，ctx 超时由上层控制。
func queryZone(ctx context.Context, reversed string, zone Zone) ZoneResult {
	qname := reversed + "." + zone.Host
	// v0.2.12：用 DoH（HTTPS/443）绕过 53 端口封锁，见包顶部说明
	addrs, err := dohLookup(ctx, qname)
	res := ZoneResult{Zone: zone}
	if err == nil {
		// v0.2.9：不再"只要能解析就判命中"。DNSBL 的"列入"返回码必须落在 127.0.0.0/8，
		// 且要排除 127.255.255.0/24 —— 多数 RBL（如 Spamhaus）用这段表示查询错误/超限/
		// 未授权的软拒绝应答；旧逻辑会把这种过载应答误判为命中，导致干净 IP 被丢弃。
		if dnsblListedHit(addrs) {
			res.Hit = true
		}
		return res
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		// 未命中，不算错误
		return res
	}
	res.Err = err
	return res
}

// dnsblListedHit 判断 DNSBL 返回的 A 记录是否为真正的"列入"应答：必须在 127.0.0.0/8，
// 且排除 127.255.255.0/24（RBL 普遍用作错误/超限/未授权返回码）。任一返回地址满足即命中。
func dnsblListedHit(addrs []string) bool {
	for _, a := range addrs {
		ip := net.ParseIP(a).To4()
		if ip == nil || ip[0] != 127 {
			continue
		}
		if ip[1] == 255 && ip[2] == 255 {
			// 127.255.255.x：错误/超限/未授权码，非真实列入
			continue
		}
		return true
	}
	return false
}

// Query 对一个 IPv4 并行查询所有 zone。
func Query(ctx context.Context, ip string, opts CheckOptions) (*CheckResult, error) {
	if opts.Threshold <= 0 {
		opts.Threshold = defaultThreshold
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	zones := opts.Zones
	if len(zones) == 0 {
		zones = DefaultZones
	}

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return nil, fmt.Errorf("invalid ip: %q", ip)
	}
	reversed, ok := reverseIPv4(parsed)
	if !ok {
		return nil, fmt.Errorf("not an IPv4 address: %q", ip)
	}

	results := make([]ZoneResult, len(zones))
	var wg sync.WaitGroup
	wg.Add(len(zones))
	for i, z := range zones {
		i, z := i, z
		go func() {
			defer wg.Done()
			zctx, cancel := context.WithTimeout(ctx, opts.Timeout)
			defer cancel()
			results[i] = queryZone(zctx, reversed, z)
		}()
	}
	wg.Wait()

	cr := &CheckResult{IP: ip, Zones: results}
	errorCount := 0
	authoritativeCount := 0
	for _, r := range results {
		if r.Hit {
			if r.Zone.DisplayOnly {
				// v0.2.20：DisplayOnly 命中仅展示给 UI，不算 HitCount 不影响 Clean
				cr.DisplayOnlyHits = append(cr.DisplayOnlyHits, r.Zone.Name)
			} else {
				cr.HitCount++
				cr.HitLists = append(cr.HitLists, r.Zone.Name)
			}
		}
		if !r.Zone.DisplayOnly {
			authoritativeCount++
		}
		if r.Err != nil && !r.Zone.DisplayOnly {
			errorCount++
		}
	}
	cr.Clean = cr.HitCount < opts.Threshold
	if cr.HitCount < opts.Threshold && errorCount > 0 && (errorCount == authoritativeCount || errorCount > authoritativeCount/2) {
		cr.Clean = false
		return cr, fmt.Errorf("dnsbl 结果不确定：%d/%d 个 zone 查询失败", errorCount, authoritativeCount)
	}
	return cr, nil
}

// Decide 三层判定：黑段库 → cache → 实测。
// 首版 IPv6 直接返回 clean + reason="IPv6 未检测"，让上层自行决定是否拦截。
func Decide(ctx context.Context, ip string, opts CheckOptions, cacheTTL time.Duration) (verdict string, reason string, detail *CheckResult, err error) {
	if opts.Threshold <= 0 {
		opts.Threshold = defaultThreshold
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}

	// 1) 黑段库
	if cidr, note, berr := ContainsIP(ctx, ip); berr != nil {
		logger.Warn("dnsbl: 黑段库查询失败 ip=%s err=%v", ip, berr)
	} else if cidr != "" {
		r := "黑段库命中 " + cidr
		if note != "" {
			r += " (" + note + ")"
		}
		return "dirty", r, nil, nil
	}

	// 2) IPv6 不查 DNSBL
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "", "", nil, fmt.Errorf("invalid ip: %q", ip)
	}
	if parsed.To4() == nil {
		return "clean", "IPv6 未检测", nil, nil
	}

	// 3) cache
	if cacheTTL > 0 {
		if entry, cerr := Lookup(ctx, ip, cacheTTL); cerr != nil {
			logger.Warn("dnsbl: 缓存查询失败 ip=%s err=%v", ip, cerr)
		} else if entry != nil {
			reason := fmt.Sprintf("缓存命中 verdict=%s hit=%d", entry.Verdict, entry.HitCount)
			if len(entry.HitLists) > 0 {
				reason += " lists=" + strings.Join(entry.HitLists, ",")
			}
			return entry.Verdict, reason, nil, nil
		}
	}

	// 4) 实测
	cr, qerr := Query(ctx, ip, opts)
	if qerr != nil {
		return "dnsbl_error", qerr.Error(), cr, qerr
	}
	v := "clean"
	if cr.HitCount >= opts.Threshold {
		v = "dirty"
	}
	reason = fmt.Sprintf("实测 %d/%d 命中", cr.HitCount, opts.Threshold)
	if len(cr.HitLists) > 0 {
		reason += ": " + strings.Join(cr.HitLists, ",")
	}

	// 回写 cache（失败只记日志，不影响判定）
	if cacheTTL > 0 {
		entry := CacheEntry{
			IP:        ip,
			HitCount:  cr.HitCount,
			HitLists:  cr.HitLists,
			Verdict:   v,
			CheckedAt: time.Now(),
		}
		if uerr := Upsert(ctx, entry); uerr != nil {
			logger.Warn("dnsbl: 缓存写入失败 ip=%s err=%v", ip, uerr)
		}
	}

	logger.Info("dnsbl: ip=%s verdict=%s %s", ip, v, reason)
	return v, reason, cr, nil
}
