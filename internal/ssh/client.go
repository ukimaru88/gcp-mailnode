package ssh

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// Config SSH 连接配置
type Config struct {
	Host       string
	Port       int
	Username   string
	Password   string
	KeyPath    string // 私钥文件路径（可选，优先于密码）
	KeyContent string // 私钥内容（可选，优先于 KeyPath）
}

// Result SSH 命令执行结果
type Result struct {
	Output string
	Err    error
}

// TestConnection 测试 SSH 连接
func TestConnection(cfg Config) error {
	client, err := connect(cfg, 10*time.Second)
	if err != nil {
		return err
	}
	defer client.Close()
	return nil
}

// RunCommand 执行远程命令
func RunCommand(ctx context.Context, cfg Config, cmd string) (string, error) {
	client, err := connect(cfg, 15*time.Second)
	if err != nil {
		return "", fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建会话失败: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() {
		done <- session.Run(cmd)
	}()

	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGTERM)
		return "", ctx.Err()
	case err := <-done:
		if err != nil {
			// 同时附上 stdout（很多脚本把诊断信息打到 stdout，尤其是 systemctl / journalctl）
			stdoutTail := tailLines(stdout.String(), 150)
			stderrTail := tailLines(stderr.String(), 150)
			return stdout.String(), fmt.Errorf("命令执行失败: %w\nstderr(末150行):\n%s\nstdout(末150行):\n%s", err, stderrTail, stdoutTail)
		}
		return stdout.String(), nil
	}
}

// tailLines 返回文本最后 n 行（简化诊断日志）
func tailLines(s string, n int) string {
	if s == "" {
		return "(空)"
	}
	lines := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

// ReadMailLog 读取远程 mail.log（增量方式）
func ReadMailLog(ctx context.Context, cfg Config, logPath string, offset int64) (string, int64, error) {
	sizeCmd := fmt.Sprintf("wc -c < %s 2>/dev/null || echo 0", logPath)
	sizeStr, err := RunCommand(ctx, cfg, sizeCmd)
	if err != nil {
		return "", 0, fmt.Errorf("获取文件大小失败: %w", err)
	}

	var fileSize int64
	fmt.Sscanf(sizeStr, "%d", &fileSize)

	if fileSize == 0 {
		return "", 0, fmt.Errorf("日志文件不存在或为空: %s", logPath)
	}

	if fileSize < offset {
		offset = 0
	}

	if fileSize == offset {
		return "", fileSize, nil
	}

	var cmd string
	if offset == 0 {
		cmd = fmt.Sprintf("cat %s", logPath)
	} else {
		cmd = fmt.Sprintf("tail -c +%d %s", offset+1, logPath)
	}

	output, err := RunCommand(ctx, cfg, cmd)
	if err != nil {
		return "", 0, fmt.Errorf("读取日志失败: %w", err)
	}

	return output, fileSize, nil
}

// ReadMailLogFull 全量读取日志
// 如果 logPath 是文件（如 /var/log/mail.log）直接读取该文件
// 如果 logPath 是 s6/daemontools 目录下的 current 文件，读取同目录所有轮转文件
func ReadMailLogFull(ctx context.Context, cfg Config, logPath string) (string, int64, error) {
	// 先检查路径是文件还是目录型日志
	isS6 := strings.Contains(logPath, "/s6/") || strings.HasSuffix(logPath, "/current")

	if !isS6 {
		// 普通日志文件（如 /var/log/mail.log），直接读取
		escaped := strings.ReplaceAll(logPath, "'", "'\\''")
		content, err := RunCommand(ctx, cfg, fmt.Sprintf("cat '%s'", escaped))
		if err != nil {
			return "", 0, fmt.Errorf("读取日志文件失败: %w", err)
		}
		size, _ := GetFileSize(ctx, cfg, logPath)
		return content, size, nil
	}

	// s6/daemontools 目录型日志：读取目录下所有轮转文件
	dir := logPath
	if idx := strings.LastIndex(logPath, "/"); idx >= 0 {
		dir = logPath[:idx]
	}
	if dir == "" {
		dir = "."
	}

	cmd := fmt.Sprintf(
		"find '%s' -maxdepth 1 -type f | sort | while read f; do cat \"$f\" 2>/dev/null; done",
		strings.ReplaceAll(dir, "'", "'\\''"),
	)
	content, err := RunCommand(ctx, cfg, cmd)
	if err != nil {
		// fallback: 直接读 current 文件
		escaped := strings.ReplaceAll(logPath, "'", "'\\''")
		content, err = RunCommand(ctx, cfg, fmt.Sprintf("cat '%s' 2>/dev/null", escaped))
		if err != nil {
			return "", 0, fmt.Errorf("全量读取日志失败: %w", err)
		}
	}

	size, _ := GetFileSize(ctx, cfg, logPath)
	return content, size, nil
}

// ReadKumoMTALogs 读取远程 KumoMTA 已归档（已 close）的日志文件。
//
// v0.1.82 重要修正（推翻 v0.1.80/v0.1.81 的"cat active 明文"误判）：
//
// 官方文档：https://docs.kumomta.com/reference/kumo/configure_local_logs/
//   - active segment 是 zstd 流式压缩文件，**roll/close 之前完全不可读**
//   - 直接 cat active 文件会读到二进制乱码，喂给 JSON parser 当然 0 条
//   - 看 active 唯一办法：远端 `/opt/kumomta/sbin/tailer --tail /var/log/kumomta`
//   - 默认 max_segment_duration 无限，max_file_size=1GB 才 roll → 低流量 VPS 几天都不 roll
//
// v0.1.82 双管齐下：
//   1. init.lua 加 max_segment_duration='5 minutes'，让 KumoMTA 每 5 分钟自动 roll 一次
//      → 软件这边最多延迟 5 分钟能看到数据
//   2. 这里只读"非 active"的已归档文件（active 是字典序最大的那个，跳过它）
//      所有归档文件无论后缀都用 zstd -dcq 解压（KumoMTA 全程 zstd 压缩，不存在明文）
//
// 返回：
//   - 解压后的明文 JSON（已归档的全部内容）
//   - cursor = 当前 active 文件名（DeleteKumoMTALogsBefore 用 < 比较，永远不会删 active）
func ReadKumoMTALogs(ctx context.Context, cfg Config, logDir string, lastFile string) (string, string, error) {
	if logDir == "" {
		logDir = "/var/log/kumomta/"
	}
	dir := strings.TrimRight(logDir, "/")
	escDir := strings.ReplaceAll(dir, "'", "'\\''")

	// 远端 zstd 可用性预检
	if out, _ := RunCommand(ctx, cfg, "command -v zstd >/dev/null 2>&1 && echo ok || echo missing"); strings.Contains(out, "missing") {
		return "", lastFile, fmt.Errorf("远端缺少 zstd：apt-get install -y zstd")
	}

	// v0.1.82：只解压"非 active"的已归档文件
	// active = ls 字典序最大的那个（KumoMTA 还在写）；前面所有文件都已 close，可以 zstd -dcq
	// 文件无后缀（KumoMTA 默认）或带 .zst（某些版本），统一用 zstd 解压
	cmd := fmt.Sprintf(
		`cd '%s' && ls -1 | sort | head -n -1 | while read f; do zstd -dcq "$f" 2>/dev/null || true; done`,
		escDir,
	)
	content, err := RunCommand(ctx, cfg, cmd)
	if err != nil {
		return "", lastFile, fmt.Errorf("读取 KumoMTA 日志目录失败: %w", err)
	}

	// cursor = active 文件名本身（DeleteKumoMTALogsBefore 永远不会删它，因为它用的是 <= cursor 比较）
	// 但等等 — DeleteKumoMTALogsBefore 用 <= 会把 cursor 自己也删了。所以 cursor 取倒数第二（最后归档的）
	// 这样 DeleteKumoMTALogsBefore 删 ≤ 倒数第二的全部归档，active 文件不动。
	cursorCmd := fmt.Sprintf(`cd '%s' && cnt=$(ls -1 | wc -l); if [ $cnt -ge 2 ]; then ls -1 | sort | tail -n 2 | head -n 1; fi`, escDir)
	cursor, _ := RunCommand(ctx, cfg, cursorCmd)
	newLastFile := strings.TrimSpace(cursor)
	if newLastFile == "" {
		newLastFile = lastFile
	}

	return content, newLastFile, nil
}

// DeleteKumoMTALogsBefore 删除 ≤ cursor 的所有 KumoMTA 日志文件（保留 cursor 之后及当前正在写的最新文件）。
// cursor 为 ReadKumoMTALogs 返回的"最大文件名的前一个"游标，上一次成功读取并解析的最后文件名。
// cursor=="" 时为安全起见不删任何文件（防止误删未提取的数据）。
//
// v0.1.77：用户场景"提取完自动删服务器数据"。一次提取的语义：
//  1. ReadKumoMTALogs 拿到 [起始, cursor] 之间的全部日志内容
//  2. 解析、写本地、确认成功
//  3. 调用此函数删除 [起始, cursor] 区间，保留 (cursor, 现在] 让下次再读
func DeleteKumoMTALogsBefore(ctx context.Context, cfg Config, logDir string, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil // 安全：无游标时不删
	}
	if logDir == "" {
		logDir = "/var/log/kumomta/"
	}
	dir := strings.TrimRight(logDir, "/")
	escDir := strings.ReplaceAll(dir, "'", "'\\''")
	escCursor := strings.ReplaceAll(cursor, "'", "'\\''")
	// 删除文件名 <= cursor 的所有日志（包括 cursor 本身），打印删除数
	cmd := fmt.Sprintf(
		`cd '%s' && deleted=0; for f in $(ls -1 | sort | awk -v last='%s' '$0<=last'); do rm -f "$f" && deleted=$((deleted+1)); done; echo $deleted`,
		escDir, escCursor,
	)
	out, err := RunCommand(ctx, cfg, cmd)
	if err != nil {
		return 0, fmt.Errorf("删除 KumoMTA 日志失败: %w", err)
	}
	var n int
	fmt.Sscanf(strings.TrimSpace(out), "%d", &n)
	return n, nil
}

// GetFileSize 获取远程文件大小
func GetFileSize(ctx context.Context, cfg Config, filePath string) (int64, error) {
	sizeCmd := fmt.Sprintf("wc -c < %s 2>/dev/null || echo 0", filePath)
	sizeStr, err := RunCommand(ctx, cfg, sizeCmd)
	if err != nil {
		return 0, err
	}
	var size int64
	fmt.Sscanf(sizeStr, "%d", &size)
	return size, nil
}

// connect 建立 SSH 连接，支持密钥和密码两种认证方式
func connect(cfg Config, timeout time.Duration) (*ssh.Client, error) {
	authMethods, err := buildAuthMethods(cfg)
	if err != nil {
		return nil, err
	}

	config := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
		Config: ssh.Config{
			KeyExchanges: []string{
				"curve25519-sha256", "curve25519-sha256@libssh.org",
				"ecdh-sha2-nistp256", "ecdh-sha2-nistp384", "ecdh-sha2-nistp521",
				"diffie-hellman-group14-sha256", "diffie-hellman-group14-sha1",
				"diffie-hellman-group1-sha1",
			},
			Ciphers: []string{
				"aes128-gcm@openssh.com", "aes256-gcm@openssh.com",
				"chacha20-poly1305@openssh.com",
				"aes128-ctr", "aes192-ctr", "aes256-ctr",
				"aes128-cbc", "aes256-cbc",
			},
		},
	}

	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", addr, err)
	}
	return client, nil
}

// buildAuthMethods 构建认证方式列表
// 优先级：KeyContent > KeyPath > Password
func buildAuthMethods(cfg Config) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// 1. 内联私钥内容
	if cfg.KeyContent != "" {
		signer, err := parsePrivateKey([]byte(cfg.KeyContent))
		if err != nil {
			return nil, fmt.Errorf("解析私钥内容失败: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	// 2. 私钥文件路径
	if cfg.KeyPath != "" {
		keyBytes, err := os.ReadFile(cfg.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("读取私钥文件失败 (%s): %w", cfg.KeyPath, err)
		}
		signer, err := parsePrivateKey(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("解析私钥文件失败: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	// 3. 密码认证（兜底）
	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("未提供任何认证方式（密码或私钥）")
	}
	return methods, nil
}

// parsePrivateKey 解析 PEM 私钥（不带密码保护）
func parsePrivateKey(pemBytes []byte) (ssh.Signer, error) {
	return ssh.ParsePrivateKey(pemBytes)
}

// UploadBytes v0.1.78：把任意字节内容（含二进制）通过 SSH stdin 上传到远端文件。
// 用 `cat > path` 接收 stdin 流，避免 `echo BASE64 | base64 -d | bash` 走单一 command
// 撞 ARG_MAX (2MB) / OpenSSH command 长度上限的问题。
// 适合大二进制文件（unsub-server 9.8MB 等），实测 50MB 内稳定。
func UploadBytes(ctx context.Context, cfg Config, remotePath string, data []byte) error {
	client, err := connect(cfg, 30*time.Second)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	defer session.Close()

	// 转义远端路径里的单引号（极少见但兜底）
	escPath := strings.ReplaceAll(remotePath, "'", "'\\''")
	// 用 dd 收 stdin → 文件；mkdir -p 父目录避免路径不存在
	parent := remotePath
	if i := strings.LastIndex(parent, "/"); i > 0 {
		parent = parent[:i]
	} else {
		parent = "."
	}
	escParent := strings.ReplaceAll(parent, "'", "'\\''")
	cmd := fmt.Sprintf(`mkdir -p '%s' && cat > '%s' && chmod 644 '%s'`, escParent, escPath, escPath)

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("打开 stdin pipe 失败: %w", err)
	}
	var stderrBuf bytes.Buffer
	session.Stderr = &stderrBuf

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("启动远端 cat 失败: %w", err)
	}

	// 流式写入数据（go-ssh 的 StdinPipe 内部分块发，无需手动切）
	writeDone := make(chan error, 1)
	go func() {
		_, werr := stdin.Write(data)
		stdin.Close()
		writeDone <- werr
	}()

	// 等待写完或 ctx 取消
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		return ctx.Err()
	case werr := <-writeDone:
		if werr != nil {
			return fmt.Errorf("写入 stdin 失败: %w (stderr: %s)", werr, strings.TrimSpace(stderrBuf.String()))
		}
	}

	// 等远端 cat 结束
	if err := session.Wait(); err != nil {
		return fmt.Errorf("远端 cat 失败: %w (stderr: %s)", err, strings.TrimSpace(stderrBuf.String()))
	}
	return nil
}

// RunScript 通过 base64 管道将脚本发送到远程执行，实时回调输出
// onOutput(stream, line) 回调：stream 为 "stdout" 或 "stderr"
func RunScript(ctx context.Context, cfg Config, script string, onOutput func(stream, line string)) (string, error) {
	client, err := connect(cfg, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建会话失败: %w", err)
	}
	defer session.Close()

	// 标准化脚本
	normalized := strings.ReplaceAll(script, "\r\n", "\n")
	normalized = strings.TrimPrefix(normalized, "\xef\xbb\xbf") // strip BOM
	if !strings.HasPrefix(normalized, "#!") {
		normalized = "#!/bin/bash\n" + normalized
	}

	// base64 编码并通过管道执行
	encoded := base64Encode([]byte(normalized))
	cmd := fmt.Sprintf("echo '%s' | base64 -d | bash", encoded)

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() {
		done <- session.Run(cmd)
	}()

	// 轮询输出
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var lastStdout, lastStderr int

	for {
		select {
		case <-ctx.Done():
			session.Signal(ssh.SIGTERM)
			return "", ctx.Err()
		case err := <-done:
			// 最后一次flush
			flushOutput(&stdout, &lastStdout, "stdout", onOutput)
			flushOutput(&stderr, &lastStderr, "stderr", onOutput)
			if err != nil {
				return stdout.String(), fmt.Errorf("脚本执行失败: %w\nstderr(末150行):\n%s\nstdout(末150行):\n%s", err,
					tailLines(stderr.String(), 150), tailLines(stdout.String(), 150))
			}
			return stdout.String(), nil
		case <-ticker.C:
			flushOutput(&stdout, &lastStdout, "stdout", onOutput)
			flushOutput(&stderr, &lastStderr, "stderr", onOutput)
		}
	}
}

func flushOutput(buf *bytes.Buffer, lastPos *int, stream string, onOutput func(string, string)) {
	if onOutput == nil {
		return
	}
	data := buf.Bytes()
	if len(data) <= *lastPos {
		return
	}
	newData := string(data[*lastPos:])
	*lastPos = len(data)
	for _, line := range strings.Split(newData, "\n") {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			onOutput(stream, line)
		}
	}
}

// ReadFile 读取远程文件内容
func ReadFile(ctx context.Context, cfg Config, filePath string) (string, error) {
	cmd := fmt.Sprintf("cat '%s'", strings.ReplaceAll(filePath, "'", "'\\''"))
	return RunCommand(ctx, cfg, cmd)
}

