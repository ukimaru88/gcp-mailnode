package dnsbl

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"gcp-mailnode/internal/store"
)

// BlackSegment 黑段库一条记录。
type BlackSegment struct {
	ID    int64
	CIDR  string
	IPNet *net.IPNet
	Note  string
}

var (
	blackCache   []BlackSegment
	blackCacheAt time.Time
	blackCacheMu sync.RWMutex
)

const blackCacheTTL = 10 * time.Minute

// readCache 返回当前缓存的浅拷贝（如果未过期），否则返回 nil。
func readCache() []BlackSegment {
	blackCacheMu.RLock()
	defer blackCacheMu.RUnlock()
	if blackCache == nil {
		return nil
	}
	if time.Since(blackCacheAt) > blackCacheTTL {
		return nil
	}
	out := make([]BlackSegment, len(blackCache))
	copy(out, blackCache)
	return out
}

// writeCache 覆盖缓存。
func writeCache(segs []BlackSegment) {
	blackCacheMu.Lock()
	defer blackCacheMu.Unlock()
	blackCache = segs
	blackCacheAt = time.Now()
}

// invalidateCache 使缓存失效。
func invalidateCache() {
	blackCacheMu.Lock()
	defer blackCacheMu.Unlock()
	blackCache = nil
	blackCacheAt = time.Time{}
}

// loadFromDB 从 DB 读出全量黑段。
func loadFromDB(ctx context.Context) ([]BlackSegment, error) {
	db := store.DB()
	if db == nil {
		return nil, fmt.Errorf("dnsbl: store.DB() 未初始化")
	}
	rows, err := db.QueryContext(ctx, `SELECT id, cidr, COALESCE(note, '') FROM black_segments ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("查询 black_segments 失败: %w", err)
	}
	defer rows.Close()

	var out []BlackSegment
	for rows.Next() {
		var seg BlackSegment
		if err := rows.Scan(&seg.ID, &seg.CIDR, &seg.Note); err != nil {
			return nil, fmt.Errorf("扫描 black_segments 行失败: %w", err)
		}
		if _, ipnet, perr := net.ParseCIDR(seg.CIDR); perr == nil {
			seg.IPNet = ipnet
		}
		out = append(out, seg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// LoadAll 返回所有黑段，10 分钟 cache。
func LoadAll(ctx context.Context) ([]BlackSegment, error) {
	if c := readCache(); c != nil {
		return c, nil
	}
	segs, err := loadFromDB(ctx)
	if err != nil {
		return nil, err
	}
	writeCache(segs)
	return segs, nil
}

// ContainsIP 判断 ip 是否落在任一黑段内。未命中返回 ("", "", nil)。
func ContainsIP(ctx context.Context, ip string) (string, string, error) {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "", "", fmt.Errorf("invalid ip: %q", ip)
	}
	segs, err := LoadAll(ctx)
	if err != nil {
		return "", "", err
	}
	for _, s := range segs {
		if s.IPNet == nil {
			continue
		}
		if s.IPNet.Contains(parsed) {
			return s.CIDR, s.Note, nil
		}
	}
	return "", "", nil
}

// Add 添加一条黑段。
func Add(ctx context.Context, cidr, note string) error {
	cidr = strings.TrimSpace(cidr)
	if cidr == "" {
		return fmt.Errorf("cidr 为空")
	}
	if _, _, err := net.ParseCIDR(cidr); err != nil {
		return fmt.Errorf("CIDR 解析失败: %w", err)
	}
	db := store.DB()
	if db == nil {
		return fmt.Errorf("dnsbl: store.DB() 未初始化")
	}
	res, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO black_segments(cidr, note) VALUES(?, ?)`, cidr, note)
	if err != nil {
		return fmt.Errorf("插入 black_segments 失败: %w", err)
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		return fmt.Errorf("CIDR 已存在: %s", cidr)
	}
	invalidateCache()
	return nil
}

// Remove 按 id 删除。
func Remove(ctx context.Context, id int64) error {
	db := store.DB()
	if db == nil {
		return fmt.Errorf("dnsbl: store.DB() 未初始化")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM black_segments WHERE id = ?`, id); err != nil {
		return fmt.Errorf("删除 black_segments 失败: %w", err)
	}
	invalidateCache()
	return nil
}

// List 强制刷新缓存并列出所有黑段。
func List(ctx context.Context) ([]BlackSegment, error) {
	invalidateCache()
	return LoadAll(ctx)
}

// ImportText 批量导入。每行 "CIDR [note]"，# 开头为注释。
func ImportText(ctx context.Context, text string) (imported, duplicates int, parseErrors []string, err error) {
	db := store.DB()
	if db == nil {
		return 0, 0, nil, fmt.Errorf("dnsbl: store.DB() 未初始化")
	}
	stmt, err := db.PrepareContext(ctx, `INSERT OR IGNORE INTO black_segments(cidr, note) VALUES(?, ?)`)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("prepare 失败: %w", err)
	}
	defer stmt.Close()

	lines := strings.Split(text, "\n")
	for idx, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 切出 CIDR 和可选 note
		var cidr, note string
		if sp := strings.IndexAny(line, " \t"); sp > 0 {
			cidr = strings.TrimSpace(line[:sp])
			note = strings.TrimSpace(line[sp:])
		} else {
			cidr = line
		}
		if _, _, perr := net.ParseCIDR(cidr); perr != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("第 %d 行 %q: %v", idx+1, line, perr))
			continue
		}
		res, xerr := stmt.ExecContext(ctx, cidr, note)
		if xerr != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("第 %d 行 %q: 写入失败 %v", idx+1, line, xerr))
			continue
		}
		aff, _ := res.RowsAffected()
		if aff == 0 {
			duplicates++
		} else {
			imported++
		}
	}
	if imported > 0 {
		invalidateCache()
	}
	return imported, duplicates, parseErrors, nil
}
