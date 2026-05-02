// Package sshkey 管理软件自用的 SSH 密钥对（v0.1.7+）。
//
// 首次启动生成 RSA-2048 密钥对，存到 %APPDATA%\gcp-mailnode\ssh_key（私钥 PEM）
// 和 ssh_key.pub（OpenSSH 格式公钥）。之后每次启动直接读取。
//
// 所有 VPS 启动时通过 metadata startup-script 把公钥写进 /root/.ssh/authorized_keys，
// SSH 密码登录关闭；软件搭建时用私钥登录。用户可通过 UI 下载私钥或复制登录命令。
package sshkey

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	gossh "golang.org/x/crypto/ssh"
)

var (
	mu         sync.RWMutex
	privatePEM []byte // 私钥 PEM 内容
	publicSSH  string // OpenSSH 格式公钥（单行 "ssh-rsa AAAA..."）
	keyDir     string
)

// Init 加载或生成密钥对。dataDir 通常是 %APPDATA%\gcp-mailnode。
func Init(dataDir string) error {
	mu.Lock()
	defer mu.Unlock()

	if dataDir == "" {
		return fmt.Errorf("sshkey: dataDir 为空")
	}
	keyDir = dataDir
	privPath := filepath.Join(dataDir, "ssh_key")
	pubPath := filepath.Join(dataDir, "ssh_key.pub")

	// 已存在 → 加载
	if priv, err := os.ReadFile(privPath); err == nil {
		if pub, err := os.ReadFile(pubPath); err == nil {
			privatePEM = priv
			publicSSH = string(pub)
			return nil
		}
	}

	// 生成新密钥
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("sshkey: 生成 RSA 密钥失败: %w", err)
	}

	// 私钥 PEM
	privBytes := x509.MarshalPKCS1PrivateKey(key)
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privBytes,
	})

	// 公钥 OpenSSH 格式
	pub, err := gossh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return fmt.Errorf("sshkey: 构造公钥失败: %w", err)
	}
	pubLine := string(gossh.MarshalAuthorizedKey(pub))

	// 写文件
	if err := os.WriteFile(privPath, privPEM, 0600); err != nil {
		return fmt.Errorf("sshkey: 写私钥失败: %w", err)
	}
	if err := os.WriteFile(pubPath, []byte(pubLine), 0644); err != nil {
		return fmt.Errorf("sshkey: 写公钥失败: %w", err)
	}

	privatePEM = privPEM
	publicSSH = pubLine
	return nil
}

// PrivatePEM 返回私钥 PEM 内容（供 SSH 客户端用）
func PrivatePEM() []byte {
	mu.RLock()
	defer mu.RUnlock()
	return privatePEM
}

// PrivatePath 返回私钥文件在本机的完整路径（用户想在外部 ssh 工具里用）
func PrivatePath() string {
	mu.RLock()
	defer mu.RUnlock()
	if keyDir == "" {
		return ""
	}
	return filepath.Join(keyDir, "ssh_key")
}

// PublicSSH 返回单行 OpenSSH 格式公钥（写进 authorized_keys 用）
func PublicSSH() string {
	mu.RLock()
	defer mu.RUnlock()
	return publicSSH
}
