# gcp-mailnode

GCP 邮件节点批量开通工具。在 Google Cloud 日本区批量预留 IP、开 VPS、
部署 KumoMTA 邮局。Wails v2 桌面软件。
> 先读 `D:\项目总览.md` 了解全局；凭据见 `D:\CLAUDE_MEMORY\credentials.md`。

## ⚠️ 源码恢复说明（2026-05-23）

本工程源码曾因被搬进系统 Temp 目录、Temp 被清而丢失，后由 Codex 从其
会话历史 `.jsonl` 中恢复（27 个 commit 历史 + 98 文件）。`go build ./...`
与前端 `tsc && vite build` 均验证通过。已 push 到 GitHub `ukimaru88/gcp-mailnode`。
工程根的 `CLAUDE_CONTINUE.md` 是恢复时一并带出的旧续接说明，内容停在
2026-04-24（提 v0.1.41），**已过时**，以 `version.txt`（v0.2.1）为准。

## 技术栈

| 项 | 值 |
|----|----|
| 框架 | Wails v2（Go + React + TypeScript + Tailwind） |
| 后端 | Go，module 名 `gcp-mailnode` |
| 数据库 | SQLite |
| 当前版本 | `version.txt` = v0.2.1 |

## 目录

- 根目录 `app_*.go` —— Wails 方法（batch/credentials/extract/resource/
  server_status/personas/templates/gcp_monitor/kumomta_diag/blackseg）
- `internal/gcp/` —— GCP API（address/compute/dns_ptr/firewall/client）
- `internal/deploy/` —— 部署调度（orchestrator/stages/templates，KumoMTA 安装）
- `internal/dns/` —— 阿里云 DNS API
- `internal/dnsbl/` —— DNSBL 黑名单 + 黑段筛选
- `internal/crypto/` —— 凭据本地加密（AES-256-GCM）
- `frontend/src/` —— React 前端（12 个页面 + 组件）

## 构建

```
cd frontend && npm install && npm run build   # 先出 frontend/dist
cd .. && bash build.sh                        # 自动 bump 版本 + wails build → releases/
```
- ⚠️ Go 用 `//go:embed frontend/dist` 嵌前端，所以**必须先构建前端**再编 Go

## 关键设计

- 4 阶段流水线：预留静态 IP + DNSBL 筛选 → 开 VPS 挂多 NIC → 阿里云 DNS +
  装 KumoMTA → 设 GCP PTR
- GCP 三种认证（Service Account JSON / OAuth / gcloud CLI）
- 区域锁定 asia-northeast1（东京）
- `internal/gcp/client.go` 里的 gcloud public client ID/secret 是 Google 官方
  公开值（全世界 gcloud CLI 共用），不是私有凭据
- 邮箱默认密码已去硬编码（`templates.go` DefaultMailPassword 为空）

## 状态

源码已恢复、可编译。v0.2.1。配套发件器是 brutal-mailer。
