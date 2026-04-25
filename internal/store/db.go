package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

var (
	db   *sql.DB
	mu   sync.Mutex
	path string
)

const schema = `
CREATE TABLE IF NOT EXISTS gcp_credentials (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    auth_type TEXT NOT NULL,
    project_id TEXT DEFAULT '',
    encrypted_blob BLOB NOT NULL,
    enabled INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS aliyun_credentials (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    access_key_id TEXT NOT NULL,
    encrypted_secret BLOB NOT NULL,
    enabled INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS vps_templates (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    regions_json TEXT DEFAULT '[]',
    auto_spread INTEGER DEFAULT 1,
    machine_type TEXT DEFAULT 'e2-micro',
    image_family TEXT DEFAULT 'debian-12',
    image_project TEXT DEFAULT 'debian-cloud',
    disk_size_gb INTEGER DEFAULT 10,
    disk_type TEXT DEFAULT 'pd-balanced',
    tags_json TEXT DEFAULT '[]',
    metadata_script TEXT DEFAULT '',
    root_password TEXT DEFAULT '',
    is_preset INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS black_segments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    cidr TEXT NOT NULL UNIQUE,
    note TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS dnsbl_cache (
    ip TEXT PRIMARY KEY,
    checked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    hit_count INTEGER DEFAULT 0,
    hit_lists TEXT DEFAULT '',
    verdict TEXT DEFAULT 'clean'
);

CREATE TABLE IF NOT EXISTS vps_instances (
    id TEXT PRIMARY KEY,
    gcp_cred_id TEXT NOT NULL,
    gcp_instance_id TEXT DEFAULT '',
    name TEXT NOT NULL,
    region TEXT NOT NULL,
    zone TEXT NOT NULL,
    machine_type TEXT NOT NULL,
    status TEXT DEFAULT 'pending',
    ip TEXT DEFAULT '',
    internal_ip TEXT DEFAULT '',
    fqdn TEXT DEFAULT '',
    root_password TEXT DEFAULT '',
    deploy_status TEXT DEFAULT 'pending',
    deploy_error TEXT DEFAULT '',
    smtp_account TEXT DEFAULT '',
    smtp_password TEXT DEFAULT '',
    dkim_public_key TEXT DEFAULT '',
    aliyun_cred_id TEXT DEFAULT '',
    domain TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS static_ips (
    id TEXT PRIMARY KEY,
    gcp_cred_id TEXT NOT NULL,
    gcp_address_name TEXT NOT NULL,
    ip TEXT NOT NULL,
    region TEXT NOT NULL,
    status TEXT DEFAULT 'reserved',
    bound_instance_id TEXT DEFAULT '',
    dnsbl_result TEXT DEFAULT '',
    dnsbl_hit_lists TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS dns_records (
    id TEXT PRIMARY KEY,
    aliyun_cred_id TEXT NOT NULL,
    domain TEXT NOT NULL,
    rr TEXT NOT NULL,
    record_type TEXT NOT NULL,
    value TEXT NOT NULL,
    aliyun_record_id TEXT DEFAULT '',
    related_instance_id TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS batch_tasks (
    id TEXT PRIMARY KEY,
    request_json TEXT NOT NULL,
    status TEXT DEFAULT 'running',
    total INTEGER DEFAULT 0,
    succeeded INTEGER DEFAULT 0,
    failed INTEGER DEFAULT 0,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    finished_at DATETIME
);

CREATE TABLE IF NOT EXISTS batch_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id TEXT NOT NULL,
    slot INTEGER DEFAULT 0,
    level TEXT DEFAULT 'INFO',
    message TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS personas (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT DEFAULT '',
    received_template TEXT NOT NULL,
    user_agent TEXT DEFAULT '',
    x_mailer TEXT DEFAULT '',
    extra_headers_json TEXT DEFAULT '[]',
    is_preset INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_vps_status ON vps_instances(status, deploy_status);
CREATE INDEX IF NOT EXISTS idx_static_status ON static_ips(status);
CREATE INDEX IF NOT EXISTS idx_dns_domain ON dns_records(domain);
CREATE INDEX IF NOT EXISTS idx_batch_logs_batch ON batch_logs(batch_id);
`

// Init opens (or creates) the store.db under dataDir and applies migrations.
func Init(dataDir string) error {
	mu.Lock()
	defer mu.Unlock()

	if db != nil {
		return nil
	}

	if dataDir == "" {
		return fmt.Errorf("store: dataDir is empty")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("store: create data dir: %w", err)
	}

	path = filepath.Join(dataDir, "store.db")
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", path)

	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("store: open db: %w", err)
	}

	if err := conn.Ping(); err != nil {
		conn.Close()
		return fmt.Errorf("store: ping db: %w", err)
	}

	// Belt-and-suspenders: ensure pragmas are set even if DSN form is ignored.
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, p := range pragmas {
		if _, err := conn.Exec(p); err != nil {
			conn.Close()
			return fmt.Errorf("store: apply pragma %q: %w", p, err)
		}
	}

	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return fmt.Errorf("store: apply schema: %w", err)
	}

	// 幂等迁移：老库补列（失败即忽略，通常是"列已存在"）
	migrations := []string{
		`ALTER TABLE vps_templates ADD COLUMN disk_type TEXT DEFAULT 'pd-balanced'`,
		`ALTER TABLE static_ips ADD COLUMN batch_id TEXT DEFAULT ''`,
		`ALTER TABLE vps_instances ADD COLUMN batch_id TEXT DEFAULT ''`,
		`ALTER TABLE vps_instances ADD COLUMN ptr_status TEXT DEFAULT 'none'`,
		`ALTER TABLE vps_instances ADD COLUMN persona_id TEXT DEFAULT ''`,
		`ALTER TABLE vps_instances ADD COLUMN hide_client_ip INTEGER DEFAULT 1`,
		`ALTER TABLE vps_instances ADD COLUMN internal_ip TEXT DEFAULT ''`,
		// v0.1.27：部署类型 kumomta（纯发信）或 mailcow（收发一体）
		`ALTER TABLE vps_templates ADD COLUMN deploy_type TEXT DEFAULT 'kumomta'`,
		`ALTER TABLE vps_instances ADD COLUMN deploy_type TEXT DEFAULT 'kumomta'`,
		// v0.1.54：Spot VM + 多 NIC 多 IP
		`ALTER TABLE vps_templates ADD COLUMN provisioning_model TEXT DEFAULT 'STANDARD'`,
		`ALTER TABLE vps_templates ADD COLUMN nic_count INTEGER DEFAULT 1`,
		`ALTER TABLE vps_instances ADD COLUMN provisioning_model TEXT DEFAULT 'STANDARD'`,
		`ALTER TABLE vps_instances ADD COLUMN nic_count INTEGER DEFAULT 1`,
		`ALTER TABLE vps_instances ADD COLUMN additional_ips_json TEXT DEFAULT '[]'`,
		`ALTER TABLE static_ips ADD COLUMN nic_index INTEGER DEFAULT 0`,
		`ALTER TABLE static_ips ADD COLUMN slot_group TEXT DEFAULT ''`,
	}
	for _, m := range migrations {
		if _, err := conn.Exec(m); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				conn.Close()
				return fmt.Errorf("store: apply migration %q: %w", m, err)
			}
		}
	}

	// 一次性数据迁移（v0.1.24）：老预设模板是 debian-12，KumoMTA 已不支持，升到 Ubuntu 22.04。
	// 只改"还是 debian-12 的预设"，用户自定义过的不动。
	if _, err := conn.Exec(`UPDATE vps_templates
		SET image_family='ubuntu-2204-lts', image_project='ubuntu-os-cloud'
		WHERE is_preset=1 AND image_family='debian-12'`); err != nil {
		// 数据迁移失败不阻止启动，仅告警
		fmt.Fprintf(os.Stderr, "[store] 升级预设模板镜像失败（非致命）: %v\n", err)
	}

	db = conn
	return nil
}

// DB returns the underlying *sql.DB handle. Returns nil if Init was not called.
func DB() *sql.DB {
	mu.Lock()
	defer mu.Unlock()
	return db
}

// Close closes the database handle. Safe to call multiple times.
func Close() error {
	mu.Lock()
	defer mu.Unlock()
	if db == nil {
		return nil
	}
	err := db.Close()
	db = nil
	return err
}
