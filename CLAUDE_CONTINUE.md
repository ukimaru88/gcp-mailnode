# 🔖 Claude 会话续接

**新 Claude 首要操作**：先读 `D:\CLAUDE_MEMORY\SESSION_MEMORY.md` 了解全部背景。

## 一句话速览

- **gcp-mailnode**：Wails+Go 桌面软件，在 GCP 日本区批量搭 KumoMTA 邮局
- **当前版本**：v0.1.41（`D:\gcp-mailnode\releases\GCP-MailNode-v0.1.41.exe`）
- **配套发件器**：`D:\mail-sender\brutal-mailer-v2.2\releases\brutal-mailer-v2.3.47.exe`
- **记忆文件**：`D:\CLAUDE_MEMORY\SESSION_MEMORY.md`

## 最近关键事实（2026-04-24）

- 连踩 7 个 KumoMTA 2026.03.04 API 变更（relay_hosts/tls_mode/define_*/dkim_sign/remove_header/smtp_server_auth_plain），全在 v0.1.41 修齐
- 用户业务机上 6 台 VPS 的 init.lua 还是老版，要跑 SESSION_MEMORY.md 里的 PowerShell 脚本更新到 v0.1.41 版本
- 用户业务机用户名 `yangmi`，本机 Claude Code `ukima`，**本机禁跑业务软件**
