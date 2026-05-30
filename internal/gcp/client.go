package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"

	"gcp-mailnode/internal/logger"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// AuthType 鉴权类型
type AuthType string

const (
	AuthServiceAccount AuthType = "service_account"
	AuthOAuth          AuthType = "oauth"
	AuthGcloudCLI      AuthType = "gcloud" // gcloud auth print-access-token，短命（1小时），兼容保留
	AuthADC            AuthType = "adc"    // Application Default Credentials，自动 refresh
)

// gcloud 公开 desktop client（与 gcloud CLI 使用的相同）
const (
	gcloudPublicClientID     = "32555940559.apps.googleusercontent.com"
	gcloudPublicClientSecret = "ZmssLNjJy2998hD4CTg2ejr2"
	scopeCloudPlatform       = "https://www.googleapis.com/auth/cloud-platform"
)

// Credential GCP 凭据
type Credential struct {
	ID        string
	Name      string
	AuthType  AuthType
	ProjectID string
	Blob      []byte
}

// Client 封装 GCP 访问所需的 token source 和 project ID。
// 具体 REST client (Instances/Addresses/Zones) 每次调用时临时构造，
// 用完 Close，避免长连接泄漏。
type Client struct {
	cred        Credential
	projectID   string
	tokenSource oauth2.TokenSource
}

// NewClient 根据凭据构造 Client
func NewClient(ctx context.Context, cred Credential) (*Client, error) {
	c := &Client{cred: cred, projectID: cred.ProjectID}

	switch cred.AuthType {
	case AuthServiceAccount:
		creds, err := google.CredentialsFromJSON(ctx, cred.Blob, scopeCloudPlatform)
		if err != nil {
			return nil, fmt.Errorf("解析 service account JSON 失败: %w", err)
		}
		c.tokenSource = creds.TokenSource
		if c.projectID == "" {
			c.projectID = creds.ProjectID
		}
		if c.projectID == "" {
			var raw struct {
				ProjectID string `json:"project_id"`
			}
			if err := json.Unmarshal(cred.Blob, &raw); err != nil {
				return nil, fmt.Errorf("解析 SA JSON project_id 失败: %w", err)
			}
			c.projectID = raw.ProjectID
		}

	case AuthOAuth:
		var tok oauth2.Token
		if err := json.Unmarshal(cred.Blob, &tok); err != nil {
			return nil, fmt.Errorf("解析 OAuth token JSON 失败: %w", err)
		}
		cfg := &oauth2.Config{
			ClientID:     gcloudPublicClientID,
			ClientSecret: gcloudPublicClientSecret,
			Scopes:       []string{scopeCloudPlatform},
			Endpoint:     google.Endpoint,
		}
		c.tokenSource = cfg.TokenSource(ctx, &tok)

	case AuthGcloudCLI:
		gts := &gcloudTokenSource{}
		if _, err := gts.Token(); err != nil {
			return nil, err
		}
		// ReuseTokenSource 会在 Expiry 到期后自动调 gts.Token()（exec gcloud auth print-access-token）重新取；
		// 所以长批量任务不会因 token 过期中断。
		c.tokenSource = oauth2.ReuseTokenSource(nil, gts)
		if c.projectID == "" {
			pidBytes, err := exec.Command("gcloud", "config", "get-value", "project").Output()
			if err == nil {
				c.projectID = strings.TrimSpace(string(pidBytes))
			}
		}

	case AuthADC:
		// Application Default Credentials：读取本机 `gcloud auth application-default login` 产生的凭证。
		// 凭证位置（Windows）：%APPDATA%\gcloud\application_default_credentials.json
		// SDK 会自动 refresh，不会 1 小时过期。
		creds, err := google.FindDefaultCredentials(ctx, scopeCloudPlatform)
		if err != nil {
			return nil, fmt.Errorf("读取 ADC 凭证失败（是否已跑 gcloud auth application-default login？）: %w", err)
		}
		c.tokenSource = creds.TokenSource
		if c.projectID == "" {
			c.projectID = creds.ProjectID
		}
		if c.projectID == "" {
			// ADC JSON 里本身不一定有 project_id，fallback 到 gcloud config
			pidBytes, err := exec.Command("gcloud", "config", "get-value", "project").Output()
			if err == nil {
				c.projectID = strings.TrimSpace(string(pidBytes))
			}
		}

	default:
		return nil, fmt.Errorf("未知 AuthType: %s", cred.AuthType)
	}

	logger.Info("GCP Client 初始化成功 project=%s authType=%s", c.projectID, cred.AuthType)
	return c, nil
}

type gcloudTokenSource struct {
	mu sync.Mutex
}

func (s *gcloudTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokBytes, err := exec.Command("gcloud", "auth", "print-access-token").Output()
	if err != nil {
		return nil, fmt.Errorf("调用 gcloud 获取 access token 失败: %w", err)
	}
	accessToken := strings.TrimSpace(string(tokBytes))
	if accessToken == "" {
		return nil, fmt.Errorf("gcloud 返回空 token")
	}
	return &oauth2.Token{
		AccessToken: accessToken,
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(50 * time.Minute),
	}, nil
}

// ProjectID 返回项目 ID
func (c *Client) ProjectID() string { return c.projectID }

// TokenSource 返回 token source
func (c *Client) TokenSource() oauth2.TokenSource { return c.tokenSource }

// TestConnection 通过调用 Zones.List 验证鉴权是否有效
func (c *Client) TestConnection(ctx context.Context) error {
	if c.projectID == "" {
		return fmt.Errorf("projectID 为空")
	}
	cli, err := compute.NewZonesRESTClient(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return fmt.Errorf("构造 Zones client 失败: %w", err)
	}
	defer cli.Close()

	req := &computepb.ListZonesRequest{
		Project: c.projectID,
	}
	it := cli.List(ctx, req)
	_, err = it.Next()
	if err != nil && err != iterator.Done {
		return fmt.Errorf("TestConnection 失败: %w", err)
	}
	logger.Info("GCP TestConnection 成功 project=%s", c.projectID)
	return nil
}

// Close 释放资源（当前无持有长连接）
func (c *Client) Close() error { return nil }

// CheckADCAvailable 检查本机 ADC 凭证是否可用。返回 ADC 找到的 projectID（可能为空）。
// 本方法不会访问网络，仅确认凭证文件能被解析 + 从 gcloud 读默认 project。
//
// ADC 凭证搜索顺序（Google SDK 默认行为）：
//  1. 环境变量 GOOGLE_APPLICATION_CREDENTIALS 指向的 JSON 文件
//  2. Windows: %APPDATA%\gcloud\application_default_credentials.json
//     Linux/Mac: ~/.config/gcloud/application_default_credentials.json
//  3. GCE/GKE 元数据服务器（仅在 GCP 虚拟机上）
//
// 失败时给出详细诊断：检查文件是否存在、gcloud 是否在 PATH、哪个用户跑的软件。
func CheckADCAvailable(ctx context.Context) (projectID string, err error) {
	creds, findErr := google.FindDefaultCredentials(ctx, scopeCloudPlatform)
	if findErr != nil {
		// 详细诊断
		var diag strings.Builder
		diag.WriteString("ADC 凭证不可用。\n\n")
		diag.WriteString("诊断信息：\n")

		// 1. GOOGLE_APPLICATION_CREDENTIALS 环境变量
		if envPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); envPath != "" {
			diag.WriteString(fmt.Sprintf("  • GOOGLE_APPLICATION_CREDENTIALS = %s", envPath))
			if _, statErr := os.Stat(envPath); statErr != nil {
				diag.WriteString(" ❌ 文件不存在\n")
			} else {
				diag.WriteString(" ✓\n")
			}
		} else {
			diag.WriteString("  • GOOGLE_APPLICATION_CREDENTIALS: 未设置\n")
		}

		// 2. 默认 ADC 文件位置（Windows 下是 %APPDATA%\gcloud\application_default_credentials.json）
		adcPath := defaultADCPath()
		diag.WriteString(fmt.Sprintf("  • ADC 默认路径: %s", adcPath))
		if _, statErr := os.Stat(adcPath); statErr != nil {
			diag.WriteString(" ❌ 文件不存在\n")
		} else {
			diag.WriteString(" ✓ 文件存在但 SDK 读取失败（可能格式损坏）\n")
		}

		// 3. 当前 Windows 用户名（ADC 是 per-user 的）
		if u := os.Getenv("USERNAME"); u != "" {
			diag.WriteString(fmt.Sprintf("  • 当前软件运行用户: %s\n", u))
			diag.WriteString("    （若 gcloud 是在另一个 Windows 用户下跑的，ADC 凭证不会共享）\n")
		}

		// 4. gcloud 是否在 PATH
		if _, pathErr := exec.LookPath("gcloud"); pathErr != nil {
			diag.WriteString("  • gcloud 命令: ❌ 不在 PATH（可能 gcloud CLI 未安装）\n")
		} else {
			diag.WriteString("  • gcloud 命令: ✓ 在 PATH\n")
		}

		diag.WriteString("\n解决方法：\n")
		diag.WriteString("  1. 先安装 gcloud CLI: https://cloud.google.com/sdk/docs/install\n")
		diag.WriteString("  2. PowerShell 跑: gcloud auth application-default login\n")
		diag.WriteString("  3. 浏览器授权完成后重启 gcp-mailnode\n")
		diag.WriteString("  4. 确保运行 gcp-mailnode 的 Windows 用户和跑 gcloud 的用户相同\n")
		diag.WriteString(fmt.Sprintf("\n原始错误: %v", findErr))

		return "", fmt.Errorf("%s", diag.String())
	}

	projectID = creds.ProjectID
	if projectID == "" {
		pidBytes, perr := exec.Command("gcloud", "config", "get-value", "project").Output()
		if perr == nil {
			projectID = strings.TrimSpace(string(pidBytes))
		}
	}
	return projectID, nil
}

// defaultADCPath 返回本机 ADC 凭证默认路径。
// Windows: %APPDATA%\gcloud\application_default_credentials.json
// Unix: $HOME/.config/gcloud/application_default_credentials.json
func defaultADCPath() string {
	if appData := os.Getenv("APPDATA"); appData != "" {
		return appData + `\gcloud\application_default_credentials.json`
	}
	if home := os.Getenv("HOME"); home != "" {
		return home + `/.config/gcloud/application_default_credentials.json`
	}
	return "<unknown>"
}

// OAuthAuthorize 执行 OAuth 授权流程：
//   - 监听 127.0.0.1 随机端口
//   - 打开浏览器到 Google 授权页
//   - 等待 /callback 拿到 code
//   - 用 code 换 token，返回 token JSON
//
// projectID 返回空串，由调用者稍后手填。
func OAuthAuthorize(ctx context.Context) (tokenJSON []byte, projectID string, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("监听本地端口失败: %w", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	redirectURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	cfg := &oauth2.Config{
		ClientID:     gcloudPublicClientID,
		ClientSecret: gcloudPublicClientSecret,
		Scopes:       []string{scopeCloudPlatform},
		Endpoint:     google.Endpoint,
		RedirectURL:  redirectURL,
	}

	state := fmt.Sprintf("state-%d", time.Now().UnixNano())
	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	logger.Info("OAuth 授权 URL: %s", authURL)

	// 打开浏览器
	if err := exec.Command("cmd", "/c", "start", "", authURL).Start(); err != nil {
		logger.Warn("自动打开浏览器失败，请手动访问授权 URL: %v", err)
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errStr := q.Get("error"); errStr != "" {
			fmt.Fprintf(w, "授权失败: %s", errStr)
			select {
			case errCh <- fmt.Errorf("OAuth 授权失败: %s", errStr):
			default:
			}
			return
		}
		if got := q.Get("state"); got != state {
			fmt.Fprint(w, "state 不匹配，可能遭受 CSRF")
			select {
			case errCh <- fmt.Errorf("state 不匹配"):
			default:
			}
			return
		}
		code := q.Get("code")
		if code == "" {
			fmt.Fprint(w, "缺少 code 参数")
			select {
			case errCh <- fmt.Errorf("缺少 code 参数"):
			default:
			}
			return
		}
		fmt.Fprint(w, "<html><body><h2>授权成功！</h2><p>可以关闭此窗口，回到 GCP MailNode。</p></body></html>")
		select {
		case codeCh <- code:
		default:
		}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(ln)
	}()
	defer srv.Shutdown(context.Background())

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var code string
	select {
	case code = <-codeCh:
	case e := <-errCh:
		return nil, "", e
	case <-timeoutCtx.Done():
		return nil, "", fmt.Errorf("OAuth 授权超时")
	}

	// 用 timeoutCtx（5 分钟上限）而非父 ctx：父 ctx 多由 UI 直接发起、常无超时，
	// token endpoint 卡住时 Exchange 会无限阻塞，授权流程挂死且无错误返回。
	tok, err := cfg.Exchange(timeoutCtx, code)
	if err != nil {
		return nil, "", fmt.Errorf("交换 token 失败: %w", err)
	}

	tokenJSON, err = json.Marshal(tok)
	if err != nil {
		return nil, "", fmt.Errorf("序列化 token 失败: %w", err)
	}

	logger.Info("OAuth 授权完成")
	return tokenJSON, "", nil
}
