package dnsbl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"gcp-mailnode/internal/logger"
)

// Zone 表示一个 DNSBL 区。
type Zone struct {
	Name string
	Host string
}

// DefaultZones 默认查询的 DNSBL 区。v0.1.9 扩充到 ~20 个主流 RBL，覆盖 mxtoolbox 主要结果。
// 选择标准：公共免费可查询，无需 API key；活跃在维护；mxtoolbox 默认会查。
// 故意排除的：Abusix/Invaluement（需付费订阅）、已停服的 Mailspike/Truncate 等。
var DefaultZones = []Zone{
	// Spamhaus 系（ZEN 已聚合 SBL+XBL+PBL+CSS+DROP）
	{"Spamhaus ZEN", "zen.spamhaus.org"},
	// Barracuda
	{"Barracuda", "b.barracudacentral.org"},
	// SpamCop
	{"SpamCop", "bl.spamcop.net"},
	// SORBS 系（dnsbl 聚合 + 几个子列表）
	{"SORBS", "dnsbl.sorbs.net"},
	{"SORBS-SPAM", "spam.dnsbl.sorbs.net"},
	{"SORBS-WEB", "web.dnsbl.sorbs.net"},
	{"SORBS-ZOMBIE", "zombie.dnsbl.sorbs.net"},
	// UCEPROTECT 三级
	{"UCEPROTECT-L1", "dnsbl-1.uceprotect.net"},
	{"UCEPROTECT-L2", "dnsbl-2.uceprotect.net"},
	{"UCEPROTECT-L3", "dnsbl-3.uceprotect.net"},
	// Passive Spam Block List
	{"PSBL", "psbl.surriel.com"},
	// Spamhaus CSS（单独查一次，虽然 ZEN 含了，额外印证）
	{"Spamhaus CSS", "css.spamhaus.org"},
	// Swinog
	{"Swinog URIBL", "uribl.swinog.ch"},
	// Nordspam
	{"Nordspam BL", "bl.nordspam.com"},
	// 0spam
	{"0spam", "bl.0spam.org"},
	// BlockedServers
	{"BlockedServers", "rbl.blockedservers.com"},
	// GBUdb Truncate
	{"GBUdb Truncate", "truncate.gbudb.net"},
	// SpfBL
	{"SpfBL", "bl.spfbl.net"},
	// Imp-SL（Interserver）
	{"Interserver", "rbl.interserver.net"},
	// JustSpam
	{"JustSpam", "bl.mailspike.net"},
	// ix.dnsbl
	{"ixBL", "ix.dnsbl.manitu.net"},
	// WPBL
	{"WPBL", "db.wpbl.info"},
	// SpamRATS 系（含 RATS Dyna / RATS NoPtr / RATS Spam）— v0.1.16 补
	{"SpamRATS-All", "all.spamrats.com"},
	{"SpamRATS-Dyna", "dyna.spamrats.com"},
	{"SpamRATS-NoPtr", "noptr.spamrats.com"},
	{"SpamRATS-Spam", "spam.spamrats.com"},
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
	HitCount int
	HitLists []string
	Zones    []ZoneResult
}

const (
	defaultThreshold = 1 // v0.1.9：命中任何一个 RBL 即判脏，最严标准
	defaultTimeout   = 3 * time.Second
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
	var r net.Resolver
	_, err := r.LookupHost(ctx, qname)
	res := ZoneResult{Zone: zone}
	if err == nil {
		res.Hit = true
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
	for _, r := range results {
		if r.Hit {
			cr.HitCount++
			cr.HitLists = append(cr.HitLists, r.Zone.Name)
		}
		if r.Err != nil {
			errorCount++
		}
	}
	cr.Clean = cr.HitCount < opts.Threshold
	if cr.HitCount < opts.Threshold && errorCount > 0 && (errorCount == len(results) || errorCount > len(results)/2) {
		cr.Clean = false
		return cr, fmt.Errorf("dnsbl 结果不确定：%d/%d 个 zone 查询失败", errorCount, len(results))
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
