// unsub-server: 部署到每台 KumoMTA VPS 的退订接收 HTTP 服务
//
// 端点：
//   GET  /u?t=TOKEN          → 显示退订确认页（HTML）
//   POST /u                  → Body: List-Unsubscribe=One-Click&t=TOKEN（Google/Yahoo 一键退订）
//   POST /u?t=TOKEN          → 同上（备用，部分客户端把 token 放 query）
//   GET  /u/list?since=TS&sig=HMAC → brutal-mailer 拉退订列表（HMAC 签名）
//   GET  /healthz            → 健康检查
//
// Token 格式：HMAC-SHA256(secret, email|list_id|created_at) → base64url 16 字节前缀.payload
// 实际格式：base64url(email|list_id|ts|hmac)，HMAC 12 字节前缀就够了
//
// 数据库：SQLite /var/lib/unsub/unsub.db
// 配置：环境变量 UNSUB_SECRET（HMAC 密钥）+ UNSUB_LISTEN（默认 127.0.0.1:8080）
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	db     *sql.DB
	secret []byte
)

const schema = `
CREATE TABLE IF NOT EXISTS unsubscribed (
	email      TEXT NOT NULL,
	list_id    TEXT NOT NULL DEFAULT '',
	unsub_at   INTEGER NOT NULL,
	source     TEXT NOT NULL DEFAULT 'http',
	PRIMARY KEY (email, list_id)
);
CREATE INDEX IF NOT EXISTS idx_unsub_at ON unsubscribed(unsub_at);
`

func main() {
	listen := os.Getenv("UNSUB_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	sec := os.Getenv("UNSUB_SECRET")
	if sec == "" {
		log.Fatal("UNSUB_SECRET env required")
	}
	secret = []byte(sec)

	dbPath := os.Getenv("UNSUB_DB")
	if dbPath == "" {
		dbPath = "/var/lib/unsub/unsub.db"
	}
	if err := os.MkdirAll("/var/lib/unsub", 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		log.Fatalf("init schema: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/u", handleUnsub)
	mux.HandleFunc("/u/list", handleList)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	log.Printf("unsub-server listening on %s", listen)
	srv := &http.Server{
		Addr:         listen,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// makeToken 生成退订 token：base64url(email|list_id|ts).hmac12
func makeToken(email, listID string, ts int64) string {
	payload := fmt.Sprintf("%s|%s|%d", email, listID, ts)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)[:12]
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sig)
}

// parseToken 验证 token，返回 email + list_id（失败返回错误）
func parseToken(token string) (email, listID string, err error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid token format")
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	if err1 != nil {
		return "", "", fmt.Errorf("decode payload: %w", err1)
	}
	gotSig, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	if err2 != nil {
		return "", "", fmt.Errorf("decode sig: %w", err2)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	wantSig := mac.Sum(nil)[:12]
	if !hmac.Equal(gotSig, wantSig) {
		return "", "", fmt.Errorf("hmac mismatch")
	}
	fields := strings.SplitN(string(payload), "|", 3)
	if len(fields) != 3 {
		return "", "", fmt.Errorf("payload fields")
	}
	return fields[0], fields[1], nil
}

func handleUnsub(w http.ResponseWriter, r *http.Request) {
	token := ""
	if r.Method == http.MethodPost {
		// One-Click: List-Unsubscribe=One-Click 在 body 里，token 通常在 URL query
		if err := r.ParseForm(); err == nil {
			token = r.Form.Get("t")
		}
		if token == "" {
			token = r.URL.Query().Get("t")
		}
	} else {
		token = r.URL.Query().Get("t")
	}

	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	email, listID, err := parseToken(token)
	if err != nil {
		log.Printf("parseToken: %v (token=%s)", err, token)
		http.Error(w, "invalid token", http.StatusBadRequest)
		return
	}

	// 写入退订表（幂等：UNIQUE on email+list_id）
	_, err = db.Exec(`INSERT OR IGNORE INTO unsubscribed (email, list_id, unsub_at, source) VALUES (?, ?, ?, ?)`,
		strings.ToLower(email), listID, time.Now().Unix(), "http")
	if err != nil {
		log.Printf("insert unsub: %v", err)
		// 仍返回 200——One-Click 标准要求成功响应
	}

	if r.Method == http.MethodPost {
		// One-Click：纯 200
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
		return
	}

	// GET：返回人类可读确认页
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>取消订阅成功</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>body{font-family:system-ui,-apple-system,sans-serif;max-width:480px;margin:80px auto;padding:24px;color:#333;text-align:center}h1{color:#2c5282}p{line-height:1.6}</style>
</head><body>
<h1>✓ 取消订阅成功</h1>
<p><strong>%s</strong> 已从邮件列表中移除。</p>
<p>You have been unsubscribed. We will not send you any further emails to this address.</p>
</body></html>`, htmlEscape(email))
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// handleList 给 brutal-mailer 拉退订列表
// GET /u/list?since=UNIX_TS&sig=HMAC(secret, "since="+since)
func handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sinceStr := q.Get("since")
	sig := q.Get("sig")
	if sinceStr == "" || sig == "" {
		http.Error(w, "missing params", http.StatusBadRequest)
		return
	}

	// 验证 HMAC：仅授权调用方可拉列表
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("since=" + sinceStr))
	wantSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
	if !hmac.Equal([]byte(sig), []byte(wantSig)) {
		http.Error(w, "bad sig", http.StatusForbidden)
		return
	}

	since, err := strconv.ParseInt(sinceStr, 10, 64)
	if err != nil {
		http.Error(w, "bad since", http.StatusBadRequest)
		return
	}

	rows, err := db.Query(`SELECT email, list_id, unsub_at, source FROM unsubscribed WHERE unsub_at >= ? ORDER BY unsub_at ASC LIMIT 10000`, since)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type entry struct {
		Email   string `json:"email"`
		ListID  string `json:"list_id"`
		UnsubAt int64  `json:"unsub_at"`
		Source  string `json:"source"`
	}
	var out []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.Email, &e.ListID, &e.UnsubAt, &e.Source); err != nil {
			continue
		}
		out = append(out, e)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"count":   len(out),
		"entries": out,
	})
}

// 兜底导入避免 unused import 警告
var _ = url.QueryEscape
