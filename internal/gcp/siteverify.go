// Google Site Verification API 集成（v0.2.21）。
//
// 用途：自动验证 GCP 项目对域名的所有权，让 SetInstancePTR 能把 PTR 设成自定义
// 域名（如 smtp.example.com）。GCP 自 2023 起强制要求 PTR 域名所有权验证，未验证
// 时返回 400 "Please verify ownership of the PTR domain"。
//
// 流程：
//  1. GetVerifyToken(domain) - POST /token 拿 google-site-verification=xxx
//  2. 调用方往 domain 根域 TXT @ 写入这个 token
//  3. 等 DNS 传播
//  4. InsertWebResource(domain) - POST /webResource，Google 查 TXT 完成验证
//  5. IsDomainVerified(domain) - GET /webResource/dns%3A%2F%2Fdomain 复查
//
// 必要条件：
//  - GCP 项目 enable Site Verification API
//    https://console.cloud.google.com/apis/library/siteverification.googleapis.com
//  - SA token source 含 cloud-platform 或 siteverification scope（NewClient 默认满足）
//  - SA 调用账户即"owner"——验证后该 GCP 项目用此 SA 能在该域名设 PTR。
package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2"
)

const siteVerifyBase = "https://www.googleapis.com/siteverification/v1"

// SiteVerifyClient 用 SA token source 调 Google Site Verification REST API。
type SiteVerifyClient struct {
	tokenSource oauth2.TokenSource
	httpClient  *http.Client
}

// NewSiteVerifyClient 从已有 gcp.Client 复用 token source 构造。
func NewSiteVerifyClient(c *Client) *SiteVerifyClient {
	return &SiteVerifyClient{
		tokenSource: c.tokenSource,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

// doRequest 发起一次带 Bearer token 的 HTTP 调用。
func (s *SiteVerifyClient) doRequest(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	tok, err := s.tokenSource.Token()
	if err != nil {
		return fmt.Errorf("拿 SA token: %w", err)
	}
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, siteVerifyBase+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("siteverification HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	if out != nil && len(respBody) > 0 {
		return json.Unmarshal(respBody, out)
	}
	return nil
}

// GetVerifyToken 拿到 DNS_TXT 验证方法的 token，返回完整 `google-site-verification=xxx` 字符串。
// 调用方应把它作为 TXT 记录值写到域名根。
func (s *SiteVerifyClient) GetVerifyToken(ctx context.Context, domain string) (string, error) {
	reqBody := map[string]interface{}{
		"verificationMethod": "DNS_TXT",
		"site": map[string]string{
			"type":       "INET_DOMAIN",
			"identifier": domain,
		},
	}
	var out struct {
		Token  string `json:"token"`
		Method string `json:"method"`
	}
	if err := s.doRequest(ctx, "POST", "/token", reqBody, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("GetVerifyToken: 返回空 token")
	}
	return out.Token, nil
}

// InsertWebResource 完成验证：Google 服务端查 TXT 记录，匹配则把域名加入项目的 verified
// resources。要求 DNS TXT 已传播。返回 nil 表示验证成功。
func (s *SiteVerifyClient) InsertWebResource(ctx context.Context, domain string) error {
	reqBody := map[string]interface{}{
		"site": map[string]string{
			"type":       "INET_DOMAIN",
			"identifier": domain,
		},
	}
	return s.doRequest(ctx, "POST", "/webResource?verificationMethod=DNS_TXT", reqBody, nil)
}

// IsDomainVerified 查询 SA 所在项目是否已验证 domain 的所有权。
// 已验证 → (true, nil)；未验证 → (false, nil)；HTTP 异常 → (false, err)。
func (s *SiteVerifyClient) IsDomainVerified(ctx context.Context, domain string) (bool, error) {
	// resource id 格式：dns://example.com，URL 转义后 GET。
	resourceID := url.PathEscape("dns://" + domain)
	tok, err := s.tokenSource.Token()
	if err != nil {
		return false, fmt.Errorf("拿 SA token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", siteVerifyBase+"/webResource/"+resourceID, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return false, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	body, _ := io.ReadAll(resp.Body)
	return false, fmt.Errorf("IsDomainVerified HTTP %d: %s", resp.StatusCode, string(body))
}
