package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"gcp-mailnode/internal/crypto"
	"gcp-mailnode/internal/dns"
	"gcp-mailnode/internal/gcp"
	"gcp-mailnode/internal/logger"
)

type GCPCredentialDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	AuthType  string    `json:"auth_type"`
	ProjectID string    `json:"project_id"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

type AliyunCredentialDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	AccessKeyID string    `json:"access_key_id"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

func (a *App) ListGCPCredentials() ([]GCPCredentialDTO, error) {
	db, err := requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, name, auth_type, project_id, enabled, created_at FROM gcp_credentials ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GCPCredentialDTO{}
	for rows.Next() {
		var c GCPCredentialDTO
		var enabled int
		if err := rows.Scan(&c.ID, &c.Name, &c.AuthType, &c.ProjectID, &enabled, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Enabled = enabled == 1
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) AddGCPCredentialServiceAccount(name, jsonContent string) (string, error) {
	jsonContent = strings.TrimSpace(jsonContent)
	if jsonContent == "" {
		return "", fmt.Errorf("JSON 内容为空")
	}
	var meta struct {
		ClientEmail string `json:"client_email"`
		ProjectID   string `json:"project_id"`
	}
	if err := json.Unmarshal([]byte(jsonContent), &meta); err != nil {
		return "", fmt.Errorf("解析 service account JSON 失败: %w", err)
	}
	if meta.ProjectID == "" {
		return "", fmt.Errorf("service account JSON 缺少 project_id")
	}
	if strings.TrimSpace(name) == "" {
		name = meta.ClientEmail
		if name == "" {
			name = meta.ProjectID
		}
	}
	enc, err := crypto.Encrypt([]byte(jsonContent))
	if err != nil {
		return "", fmt.Errorf("加密失败: %w", err)
	}
	db, err := requireDB()
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	if _, err := db.Exec(
		`INSERT INTO gcp_credentials (id, name, auth_type, project_id, encrypted_blob, enabled) VALUES (?,?,?,?,?,1)`,
		id, name, string(gcp.AuthServiceAccount), meta.ProjectID, enc); err != nil {
		return "", err
	}
	logger.Info("已添加 GCP SA 凭证 id=%s project=%s", id, meta.ProjectID)
	return id, nil
}

func (a *App) AddGCPCredentialOAuth(name, projectID string) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	tokenJSON, _, err := gcp.OAuthAuthorize(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(name) == "" {
		name = "OAuth-" + time.Now().Format("20060102-150405")
	}
	enc, err := crypto.Encrypt(tokenJSON)
	if err != nil {
		return "", fmt.Errorf("加密失败: %w", err)
	}
	db, err := requireDB()
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	if _, err := db.Exec(
		`INSERT INTO gcp_credentials (id, name, auth_type, project_id, encrypted_blob, enabled) VALUES (?,?,?,?,?,1)`,
		id, name, string(gcp.AuthOAuth), projectID, enc); err != nil {
		return "", err
	}
	logger.Info("已添加 GCP OAuth 凭证 id=%s project=%s", id, projectID)
	return id, nil
}

func (a *App) AddGCPCredentialGcloud(name string) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cli, err := gcp.NewClient(ctx, gcp.Credential{AuthType: gcp.AuthGcloudCLI})
	if err != nil {
		return "", fmt.Errorf("gcloud 初始化失败: %w", err)
	}
	defer cli.Close()
	if err := cli.TestConnection(ctx); err != nil {
		return "", fmt.Errorf("gcloud TestConnection 失败: %w", err)
	}
	if strings.TrimSpace(name) == "" {
		name = "gcloud-" + cli.ProjectID()
	}
	db, err := requireDB()
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	if _, err := db.Exec(
		`INSERT INTO gcp_credentials (id, name, auth_type, project_id, encrypted_blob, enabled) VALUES (?,?,?,?,?,1)`,
		id, name, string(gcp.AuthGcloudCLI), cli.ProjectID(), []byte{}); err != nil {
		return "", err
	}
	logger.Info("已添加 GCP gcloud 凭证 id=%s project=%s", id, cli.ProjectID())
	return id, nil
}

func (a *App) CheckGCPADC() (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return gcp.CheckADCAvailable(ctx)
}

func (a *App) AddGCPCredentialADC(name, projectID string) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cli, err := gcp.NewClient(ctx, gcp.Credential{AuthType: gcp.AuthADC, ProjectID: strings.TrimSpace(projectID)})
	if err != nil {
		return "", fmt.Errorf("ADC 初始化失败: %w", err)
	}
	defer cli.Close()
	if err := cli.TestConnection(ctx); err != nil {
		return "", fmt.Errorf("ADC TestConnection 失败: %w", err)
	}
	resolvedProject := cli.ProjectID()
	if resolvedProject == "" {
		return "", fmt.Errorf("无法确定 project_id，请手工填写")
	}
	if strings.TrimSpace(name) == "" {
		name = "adc-" + resolvedProject
	}
	db, err := requireDB()
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	if _, err := db.Exec(
		`INSERT INTO gcp_credentials (id, name, auth_type, project_id, encrypted_blob, enabled) VALUES (?,?,?,?,?,1)`,
		id, name, string(gcp.AuthADC), resolvedProject, []byte{}); err != nil {
		return "", err
	}
	logger.Info("已添加 GCP ADC 凭证 id=%s project=%s", id, resolvedProject)
	return id, nil
}

func (a *App) TestGCPCredential(id string) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := requireDB()
	if err != nil {
		return "", err
	}
	var name, authType, projectID string
	var encBlob []byte
	row := db.QueryRow(`SELECT name, auth_type, project_id, encrypted_blob FROM gcp_credentials WHERE id=?`, id)
	if err := row.Scan(&name, &authType, &projectID, &encBlob); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("未找到凭证")
		}
		return "", err
	}
	var blob []byte
	if len(encBlob) > 0 {
		dec, err := crypto.Decrypt(encBlob)
		if err != nil {
			return "", fmt.Errorf("解密失败: %w", err)
		}
		blob = dec
	}
	cli, err := gcp.NewClient(ctx, gcp.Credential{ID: id, Name: name, AuthType: gcp.AuthType(authType), ProjectID: projectID, Blob: blob})
	if err != nil {
		return "", err
	}
	defer cli.Close()
	if err := cli.TestConnection(ctx); err != nil {
		return "", err
	}
	return fmt.Sprintf("连接成功，project=%s", cli.ProjectID()), nil
}

func (a *App) DeleteGCPCredential(id string) error {
	db, err := requireDB()
	if err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM gcp_credentials WHERE id=?`, id)
	return err
}

func (a *App) SetGCPCredentialEnabled(id string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	db, err := requireDB()
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE gcp_credentials SET enabled=? WHERE id=?`, v, id)
	return err
}

func (a *App) ListAliyunCredentials() ([]AliyunCredentialDTO, error) {
	db, err := requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, name, access_key_id, enabled, created_at FROM aliyun_credentials ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AliyunCredentialDTO{}
	for rows.Next() {
		var c AliyunCredentialDTO
		var enabled int
		if err := rows.Scan(&c.ID, &c.Name, &c.AccessKeyID, &enabled, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Enabled = enabled == 1
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) AddAliyunCredential(name, accessKeyID, accessKeySecret string) (string, error) {
	name = strings.TrimSpace(name)
	accessKeyID = strings.TrimSpace(accessKeyID)
	accessKeySecret = strings.TrimSpace(accessKeySecret)
	if accessKeyID == "" || accessKeySecret == "" {
		return "", fmt.Errorf("AccessKey ID / Secret 不能为空")
	}
	if name == "" {
		name = accessKeyID
	}
	enc, err := crypto.Encrypt([]byte(accessKeySecret))
	if err != nil {
		return "", fmt.Errorf("加密失败: %w", err)
	}
	db, err := requireDB()
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	if _, err := db.Exec(
		`INSERT INTO aliyun_credentials (id, name, access_key_id, encrypted_secret, enabled) VALUES (?,?,?,?,1)`,
		id, name, accessKeyID, enc); err != nil {
		return "", err
	}
	logger.Info("已添加阿里云凭证 id=%s", id)
	return id, nil
}

func (a *App) TestAliyunCredential(id string) (string, error) {
	db, err := requireDB()
	if err != nil {
		return "", err
	}
	var ak string
	var encSec []byte
	row := db.QueryRow(`SELECT access_key_id, encrypted_secret FROM aliyun_credentials WHERE id=?`, id)
	if err := row.Scan(&ak, &encSec); err != nil {
		return "", err
	}
	sk, err := crypto.Decrypt(encSec)
	if err != nil {
		return "", fmt.Errorf("解密失败: %w", err)
	}
	return dns.NewAliyunDns(ak, string(sk)).TestConnection()
}

func (a *App) DeleteAliyunCredential(id string) error {
	db, err := requireDB()
	if err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM aliyun_credentials WHERE id=?`, id)
	return err
}

func (a *App) SetAliyunCredentialEnabled(id string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	db, err := requireDB()
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE aliyun_credentials SET enabled=? WHERE id=?`, v, id)
	return err
}
