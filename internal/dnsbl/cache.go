package dnsbl

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gcp-mailnode/internal/store"
)

// CacheEntry dnsbl_cache 一行。
type CacheEntry struct {
	IP        string
	CheckedAt time.Time
	HitCount  int
	HitLists  []string
	Verdict   string
}

// Lookup 查缓存。超过 ttl 或不存在返回 (nil, nil)。
func Lookup(ctx context.Context, ip string, ttl time.Duration) (*CacheEntry, error) {
	db := store.DB()
	if db == nil {
		return nil, fmt.Errorf("dnsbl: store.DB() 未初始化")
	}
	row := db.QueryRowContext(ctx,
		`SELECT ip, checked_at, hit_count, hit_lists, verdict FROM dnsbl_cache WHERE ip = ?`, ip)

	var (
		entry     CacheEntry
		checkedAt time.Time
		hitLists  string
	)
	err := row.Scan(&entry.IP, &checkedAt, &entry.HitCount, &hitLists, &entry.Verdict)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("查询 dnsbl_cache 失败: %w", err)
	}
	entry.CheckedAt = checkedAt
	if hitLists != "" {
		entry.HitLists = strings.Split(hitLists, ",")
	}
	if ttl > 0 && time.Since(entry.CheckedAt) > ttl {
		return nil, nil
	}
	return &entry, nil
}

// Upsert 插入或覆盖一条缓存记录。checked_at 始终使用数据库 CURRENT_TIMESTAMP。
func Upsert(ctx context.Context, entry CacheEntry) error {
	db := store.DB()
	if db == nil {
		return fmt.Errorf("dnsbl: store.DB() 未初始化")
	}
	hitLists := strings.Join(entry.HitLists, ",")
	_, err := db.ExecContext(ctx, `
INSERT INTO dnsbl_cache(ip, checked_at, hit_count, hit_lists, verdict)
VALUES (?, CURRENT_TIMESTAMP, ?, ?, ?)
ON CONFLICT(ip) DO UPDATE SET
    checked_at = CURRENT_TIMESTAMP,
    hit_count  = excluded.hit_count,
    hit_lists  = excluded.hit_lists,
    verdict    = excluded.verdict
`, entry.IP, entry.HitCount, hitLists, entry.Verdict)
	if err != nil {
		return fmt.Errorf("upsert dnsbl_cache 失败: %w", err)
	}
	return nil
}

// Purge 清理超过 ttl 的缓存，返回删除行数。
func Purge(ctx context.Context, ttl time.Duration) (int, error) {
	db := store.DB()
	if db == nil {
		return 0, fmt.Errorf("dnsbl: store.DB() 未初始化")
	}
	cutoff := time.Now().Add(-ttl)
	res, err := db.ExecContext(ctx, `DELETE FROM dnsbl_cache WHERE checked_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge dnsbl_cache 失败: %w", err)
	}
	aff, _ := res.RowsAffected()
	return int(aff), nil
}
