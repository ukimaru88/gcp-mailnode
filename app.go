package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"gcp-mailnode/internal/crypto"
	"gcp-mailnode/internal/dnsbl"
	"gcp-mailnode/internal/logger"
	"gcp-mailnode/internal/sshkey"
	"gcp-mailnode/internal/store"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 主程序状态。
type App struct {
	ctx context.Context
}

func NewApp() *App {
	return &App{}
}

func dataDirectory() string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "."
	}
	p := filepath.Join(cfg, "gcp-mailnode")
	_ = os.MkdirAll(p, 0700)
	return p
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if err := logger.Init(); err != nil {
		runtime.LogError(ctx, "logger init failed: "+err.Error())
	}
	logger.SetEmitter(func(level, msg string) {
		runtime.EventsEmit(ctx, "log:entry", map[string]string{
			"level": level,
			"msg":   msg,
		})
	})

	dir := dataDirectory()

	if err := crypto.Init(); err != nil {
		logger.Error("加密模块初始化失败: %s", err.Error())
	} else {
		logger.Info("加密模块就绪")
	}

	if err := sshkey.Init(dir); err != nil {
		logger.Error("SSH 密钥初始化失败: %s", err.Error())
	} else {
		logger.Info("SSH 密钥就绪: %s", sshkey.PrivatePath())
	}

	if err := store.Init(dir); err != nil {
		logger.Error("数据库初始化失败: %s", err.Error())
	} else {
		logger.Info("数据库就绪: %s", dir)
		if err := ensurePresetTemplates(); err != nil {
			logger.Warn("初始化预设模板失败: %s", err.Error())
		}
		if err := ensurePresetPersonas(); err != nil {
			logger.Warn("初始化预设 Persona 失败: %s", err.Error())
		}
		// v0.1.16: 启动时清理超过 6 小时的 DNSBL 缓存，避免旧版软件留下的 stale "clean" 判定污染新扫描。
		if n, err := dnsbl.Purge(ctx, 6*time.Hour); err == nil && n > 0 {
			logger.Info("清理过期 DNSBL 缓存: %d 条", n)
		}
		// v0.1.77：启动自动提取调度器（按 settings.extract_schedule_config 决定是否真跑）
		a.startExtractScheduler()
	}
	logger.Info("版本: %s", Version)
}

func (a *App) shutdown(ctx context.Context) {
	_ = store.Close()
	logger.Close()
}

// GetVersion 返回版本号（前端 Layout 页可用）。
func (a *App) GetVersion() string {
	return Version
}

// OpenLogDir 在资源管理器打开日志目录。
func (a *App) OpenLogDir() error {
	return openExplorer(logger.Dir())
}

// GetSSHPrivateKey 返回软件管理的 SSH 私钥内容（PEM 格式），前端可提供下载按钮。
func (a *App) GetSSHPrivateKey() string {
	return string(sshkey.PrivatePEM())
}

// GetSSHPrivateKeyPath 返回私钥文件在本机的绝对路径（用户可自行 ssh -i 使用）。
func (a *App) GetSSHPrivateKeyPath() string {
	return sshkey.PrivatePath()
}

// GetSSHPublicKey 返回公钥（OpenSSH 格式，单行）。
func (a *App) GetSSHPublicKey() string {
	return sshkey.PublicSSH()
}

// ClearDNSBLCache 清空全部 DNSBL 缓存（不看 TTL）。用户在 RBL 列表扩充后想强制重新检测时手动触发。
func (a *App) ClearDNSBLCache() (int, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return dnsbl.Purge(ctx, 0)
}
