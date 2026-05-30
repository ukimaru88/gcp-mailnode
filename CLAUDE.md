# gcp-mailnode - 项目档案

> 最后更新：2026-05-30
> 当前版本：**v0.2.8**（`version.txt`；代码已含 P0 + 3 项安全/正确性修复，exe 待 `bash build.sh` 打包）
> 续接触发词："继续 gcp-mailnode" / "继续 GCP" / "继续节点"
> 跨项目共享记忆：`D:\CLAUDE_MEMORY\`
> 凭据：`D:\CLAUDE_MEMORY\credentials.md`

---

## 0. 一句话项目摘要

GCP 邮件节点批量开通工具（Wails v2 = Go + React + 单 exe），在 Google Cloud **日本区（asia-northeast1 东京）**批量预留 IP + 开 VPS + 部署 KumoMTA 邮局。**与 brutal-mailer 配套**——gcp-mailnode 是搭建侧（透明中继），brutal-mailer 是发件侧（Persona 伪造）。

---

## 1. ⚠️ 源码恢复说明（2026-05-23）

本工程源码曾因被搬进系统 Temp 目录、Temp 被清而丢失，后由 **Codex 从其会话历史 `.jsonl` 中恢复**（27 个 commit 历史 + 98 文件 = 46 .go + 16 .tsx）。`go build ./...` 与前端 `tsc && vite build` 均验证通过，已 push 到 GitHub `ukimaru88/gcp-mailnode`。

工程根的 `CLAUDE_CONTINUE.md` 是恢复时一并带出的**旧续接说明**（停在 2026-04-24 提 v0.1.41），**已过时**，以本 CLAUDE.md + `version.txt` (v0.2.1) 为准。

---

## 2. 跨项目共享记忆

详见 `D:\CLAUDE_MEMORY\`：`README.md` / `architecture.md`（gcp ↔ brutal 协同）/ `gcp-mailnode.md` / `credentials.md`。

特别注意 `architecture.md`：**Persona 伪造在 brutal 侧做，KumoMTA 只做 HideClientIP + DKIM 签名（纯透明中继）**。

---

## 3. 技术栈

| 层 | 技术 |
|---|---|
| 框架 | Wails v2（Go + React + TypeScript + Tailwind） |
| 后端 | Go（module 名 `gcp-mailnode`） |
| 数据库 | SQLite |
| GCP SDK | Compute Engine / DNS / IAM 直接 HTTP API（带 OAuth2） |
| GCP 认证 | Service Account JSON / OAuth / gcloud CLI 三选一 |
| 凭据加密 | AES-256-GCM（`internal/crypto/`） |

---

## 4. 目录结构

```
D:\gcp-mailnode\
├── 根目录 app_*.go         # Wails 方法
│   ├── app_batch.go        # 批量调度
│   ├── app_credentials.go  # 凭据 CRUD（AES 加密）
│   ├── app_extract.go      # 邮箱提取
│   ├── app_resource.go     # GCP 资源
│   ├── app_server_status.go
│   ├── app_personas.go     # Persona 类型管理（仅元数据，伪造在 brutal）
│   ├── app_templates.go
│   ├── app_gcp_monitor.go
│   ├── app_kumomta_diag.go
│   └── app_blackseg.go     # 黑段筛选
├── internal/
│   ├── gcp/                # GCP API（address / compute / dns_ptr / firewall / client）
│   ├── deploy/             # 部署调度（orchestrator / stages / templates，KumoMTA 安装）
│   ├── dns/                # 阿里云 DNS API
│   ├── dnsbl/              # DNSBL 黑名单 + 黑段筛选
│   └── crypto/             # AES-256-GCM 凭据加密
├── frontend/src/           # React 前端（12 个页面 + 组件）
├── version.txt
├── build.sh
└── go.mod
```

---

## 5. 4 阶段流水线

| Stage | 名称 | 关键动作 |
|---|---|---|
| **A** | IP 预留 + 筛选 | GCP Address API 预留静态 IP（**STANDARD 而非 Spot**） → **25 个 DNSBL 黑名单**并行查询（Spamhaus ZEN/CSS、Barracuda、SpamCop、SORBS×4、UCEPROTECT×3、PSBL、Swinog、Nordspam、0spam、BlockedServers、GBUdb、SpfBL、Interserver、JustSpam、ixBL、WPBL、SpamRATS×4，源 `internal/dnsbl/dnsbl.go:24`）→ 留下纯净 IP |
| **B** | 开 VPS + 挂 NIC | 三档机型可选（e2-micro / e2-small / e2-medium）→ 多 NIC 多 IP（按 8 IP 分组 slot_group + nic_index 分配 + policy routing） |
| **C** | 阿里云 DNS + 装 KumoMTA | 阿里云 DNS 配 A/MX/SPF/DKIM/DMARC → SSH 上 VPS 装 KumoMTA → 配 HideClientIP + DKIM |
| **D** | 设 GCP PTR | reverse DNS 设置（每个 IP 都要设）→ `verifyReversePTR` 校验（v0.1.75 加，防 nic1-7 silent ignore） |

---

## 6. 核心设计决策

### v0.2.0 关键削减
- **删 KumoMTA 限速**（日本三大 shaping + 1200/min 全删）→ 用户希望纯透明中继不限速
- 改 **STANDARD 非 Spot**（Spot 实例会被抢占影响发信）
- 三档机型：e2-micro（最小）/ e2-small（默认）/ e2-medium

### 邮箱默认密码
- `templates.go` 的 `DefaultMailPassword` **已去硬编码**（为空），部署时必须传参

### gcloud public client ID/secret
- `internal/gcp/client.go` 里的 gcloud public client ID/secret 是 **Google 官方公开值**（全世界 gcloud CLI 共用），**不是私有凭据**，不要 mistake 为泄密

### 区域锁定
- `asia-northeast1`（东京），其他区域写死不放行

### 多 NIC 设计
- slot_group 按 8 IP 分组
- nic_index 分配
- policy routing 配置（每个 NIC 走独立路由表）
- 反向 PTR 必须**逐 NIC 验证**（nic1-7 假阳性问题，v0.1.75 修）

---

## 7. KumoMTA 配置要点

⚠️ **v0.1.85 完全删 egress path shaping**（2026-05-23 audit 核实）：不再注册 `get_egress_path_config`，让 KumoMTA 走**内部默认**（`connection_limit` 默认 10，无显式速率上限，遇 421/throttle 自适应退避）。

| 参数 | 当前实际值 | 用途 |
|---|---|---|
| `connection_limit` | **KumoMTA 内部默认 10**（不再显式配置） | 每域名并发连接 |
| `max_deliveries` | 内部默认（不再配置） | 每会话投递数 |
| `max_ready` | 内部默认（不再配置） | ready 队列上限 |
| `trace_headers`（25 + 587 listener 各 1 个） | **false** | **隐藏发件 IP（HideClientIP）** |
| 25 端口 AUTH 失败 | accept_to=true + relay_to=false（catch-all 收信不转发） | v0.1.73 黑洞收件 |
| 587 端口 AUTH 通过 | relay_to=true | 出站发信 |

### 历史 queue 调参（v0.1.83，已被 v0.1.85 覆盖）
原本为修 queue 堆积 22000+ 调过：cl 50→10 / md 100→1000 / mr 1024→50000。v0.1.85 一刀切取消所有 shaping 后，这套参数不再生效，仅作历史记录。

详见 `architecture.md` 和源文件 `internal/deploy/templates/init.lua.tmpl`。

---

## 8. 与 brutal-mailer 的协同契约

| 职责 | 哪一侧 |
|---|---|
| Persona 伪造（Received/UA/X-Mailer） | brutal 侧 |
| HideClientIP | gcp-KumoMTA（trace_headers = false） |
| DKIM 签名 | gcp-KumoMTA |
| smtp_v2 6 列导出 | gcp-mailnode 导出 |
| smtp_v2 导入 | brutal-mailer 导入 |

**6 列 smtp_v2 格式**：`host:port:user:pass:persona:hide_ip`

**关键澄清**：**Persona 不绑定 / 不校验** → brutal 账号选 docomo persona，gcp 部署 gmail 域名仍能用任意 VPS（详见 `project_gcp_mailnode_brutal_sync.md`）。

---

## 9. 版本历史

| 版本 | 改动 |
|---|---|
| **v0.2.8**（待打包） | 最新。安全 + 正确性修复：① **P0 开放中继**（`GenerateMailPassword` 空时回退随机 20 位 [A-Za-z0-9] 密码）② **域名注入**（`render()` 单点 LDH 白名单校验，堵 shell+Lua）③ **配额自愈失效**（`IsQuotaExceeded` 改 `contains(quota)&&contains(exceeded)`，识别 GCP 真实配额 message）④ **OAuth 无超时**（`cfg.Exchange` 改用 timeoutCtx）⑤ 修审计 P1-1 失效测试断言。全部加防回归测试 |
| v0.2.7 | SMTP 导出格式全栈对齐 mail-toolkit 约定：账号统一 `info@根域`、SMTP host=`smtp.根域`、部署时自动加 `smtp` 子域 A 记录、Postfix 改 Cyrus SASL **sasldb 后端**、CSV 导出归一化（`domain,smtp_host,smtp_port,account,password,security`）。⚠️ 老 KumoMTA VPS 要重跑 Stage C 重渲 smtp_auth.lua 才认新账号 |
| v0.2.6 | SMTP 导出格式第一次修（不完整，仍用 fqdn 做 host），被 v0.2.7 全栈对齐取代 |
| v0.2.5 | 加「跳过 DNSBL 检测」开关（StageARequest.SkipDNSBL）：仅前缀过滤，20 IP 从 100-200s 缩到 20-40s；UI 黄色 checkbox 默认关 |
| v0.2.4 | 修「重启筛选后已筛干净 IP 从清单消失」：StartStageA 开头过继老 batch 的 clean IP（`UPDATE static_ips SET batch_id=? WHERE batch_id<>? AND status='clean'`）+ 预填 succeeded 计数 |
| v0.2.3 | ① 加 **Postfix + OpenDKIM 部署路径**（`install_postfix.sh`，端口自 mail-toolkit；多 NIC 仍只能 KumoMTA）② 默认排除前缀 `34.`+`35.` ③ 默认机型改 e2-micro ④ Region exhausted 改 60s 软冷却（worker 不再因配额满退出）⑤ 跨批次记忆已知坏 IP（命中直接 holdDirty 跳过 DNSBL） |
| v0.2.2 | 纯 bump（验证编译链路，代码无改动） |
| v0.2.0 | 删 KumoMTA 限速 + 改 STANDARD 非 Spot + 三档机型 |
| v0.1.85 | 完全删 egress path shaping，KumoMTA 走内部默认，遇 throttle 自适应退避 |
| v0.1.83 | KumoMTA queue 堆积 22000+ 修复（connection_limit 50→10 / max_deliveries 100→1000 / max_ready 1024→50000，**已被 v0.1.85 覆盖**） |
| v0.1.82 | active segment zstd 解压 + cursor 游标 + rsyslog 权限 (2770) |
| v0.1.78 | install_unsub EOF（ARG_MAX 超限）→ `ssh.UploadBytes` 流式 |
| v0.1.77 | 邮箱提取三件套（成功 only / 删日志 / 自动调度） |
| v0.1.75 | PTR 假阳性（nic1-7 silent ignore）→ `verifyReversePTR` 检测 |
| v0.1.41 | 旧续接说明停在这里（已过时） |

---

## 10. 已知坑 / 雷区

### GCP 相关
- **必须 STANDARD 非 Spot**，Spot 会被抢占
- **PTR 必须逐 NIC 验证**，否则 nic1-7 silent ignore
- 区域锁 asia-northeast1，跨区会出问题

### KumoMTA 相关
- Debian 12 minimal 无 rsyslog → install_postfix.sh 自动装 + journalctl 兜底（同 mail-toolkit 坑）
- queue 堆积调参（v0.1.83 已修，不要再回退）

### 编译相关
- **必须先构建前端再编 Go**：`cd frontend && npm install && npm run build` → `bash build.sh`
- 原因：Go 用 `//go:embed frontend/dist` 嵌前端
- **改码后默认不自动打包**（`feedback_gcp_mailnode_no_autobuild.md`）：等用户说"打包/出 exe"才编译

### 凭据相关
- 邮箱默认密码已去硬编码，部署时必须传参
- gcloud public client ID/secret 是公开值，不是私有凭据

---

## 11. 待办

> 本节随 2026-05-30「修 P0 + 4 Agent 全量审核」更新。✅ = 本次已修（代码已改、测试已加，待 `bash build.sh` 打包成 v0.2.8）。

### ✅ 本次已修（2026-05-30，代码已含，待打包 v0.2.8）
- **P0 开放中继 + 空密码系统账号**：`GenerateMailPassword` 空时回退随机 20 位 `[A-Za-z0-9]` 密码（[templates.go](internal/deploy/templates.go)），KumoMTA `smtp_auth.lua` / Postfix `saslpasswd2`+`chpasswd` / 落库 / CSV 全链路一致；字符集纯字母数字同时规避了 CSV/smtp_v2 分隔符打穿。加 `TestGenerateMailPassword_NeverEmpty` + `TestRenderSmtpAuthLua_NoEmptyPassword` 防回归。
- **域名注入（P0 的另一半）**：`render()` 单点加 `validateDeployDomains`，FQDN/RootDomain/Subdomain 仅允许 LDH（`[A-Za-z0-9.-]`），堵死经畸形域名注入 root shell（`hostnamectl` / `opendkim-genkey -d`）与 KumoMTA Lua 单引号字符串两条路径。加 `TestRender_RejectsInjectionDomain`。
- **配额自愈失效**：`IsQuotaExceeded`（[gcp/errors.go](internal/gcp/errors.go)）字符串兜底改 `contains("quota") && contains("exceeded")`，能识别 GCP compute/apiv1 真实 message `Quota 'STATIC_ADDRESSES' exceeded.`（旧的 `quota_exceeded`/`quota exceeded` 匹配不到 → region 软冷却/让额逻辑全失效）；`IsNotFound` 补 `not found`。加 `errors_test.go`。
- **OAuth token 交换无超时**：`cfg.Exchange` 改用 5min `timeoutCtx`（[gcp/client.go](internal/gcp/client.go)），防 token endpoint 卡死无限阻塞。
- **审计 P1-1 失效测试**：`get_egress_path_config` 断言改用 `containsActiveLuaCall` 跳注释 + 语义倒转（v0.1.85 后不应再注册该 hook）。

### 待办（本轮审核新发现，未修，确凿度见括注）
- **Postfix 冗余系统账号（确凿，缩攻击面）**：install_postfix.sh `chpasswd` 给 `info` 设可登录系统账号，但 SASL 走 sasldb 根本不用它。建议 `usermod -s /usr/sbin/nologin` 或删 useradd。
- **clean IP 过继计数错位（确凿计数 / 并发抢 IP 需验证）**：stages.go 过继老 batch clean IP 时 `succeeded` 预填可能虚高；reparent 的 WHERE 不限 region/cred，并发跑两个 Stage A 会互抢 clean IP。建议 reparent 限定本次 cred/region 且 succeeded clamp 到 req.Count。
- **PTR nic1-7 可能静默失公网 NAT（需验证）**：`SetInstancePTRForNIC` 在 Delete 成功+Add 失败+restore 失败时该 NIC 掉外网，调用方只 WARN 不重建 AccessConfig。
- **GCP Insert 成功+Wait 失败不清理孤儿（需验证）**：compute.go/address.go 的 CreateInstance/ReserveStaticAddress 在 Wait 失败时不删半成品，孤儿 VM/静态 IP 持续计费（与 P2-1 同源但更底层）。
- **CSV 导出零转义（当前已被规避）**：app_resource.go 导出直接拼接，靠密码 `[A-Za-z0-9]` + 合法域名规避；若未来放宽密码字符集需补 `csv.Writer`/转义，且要与 mail-toolkit 用同一套规则。
- **DNSBL 软拒绝误判（需验证）**：dnsbl.go 把 RBL 返回的"过载占位地址"也当命中，阈值=1 放大，可能误丢干净 IP。建议校验返回 A 记录是否落在该 RBL 有效返回码范围。
- **gcloud token 50min 硬编码过期（需验证）**：client.go `gcloudTokenSource` 固定标 50min，可能用到已过期 token，建议取短或解析真实 exp（AuthGcloudCLI 是兼容路径，影响有限）。

### 历史待办（审计 2026-05-23，仍未修）
- **P1-3' PTR 校验窗口太短**：`verifyReversePTR` 30s（5×6s）< GCP 传播 30-120s，误判 partial。建议 10×15s。
- **P2-1 Stage A 取消漏释放 in-flight IP**：取消时 reserve→DNSBL 之间的 IP 不进 dirty 也不释放，GCP 静态 IP 继续计费。
- **P2-2 GCP client 未复用**：每次 Create/Get/Delete 新建 REST client，100+ 台批量可能撞 ephemeral port 上限。建议 Client 结构体缓存各 client。
- systemd 持久化 KumoMTA 服务 / curl 兜底依赖（部分服务器没装 curl）/ auto-PTR 链路端到端验证。

### 🔴 头号风险（未变）
- **端到端实测**：v0.2.1→v0.2.8 一堆改动（Postfix/sasldb/smtp.根域 A 记录/CSV 对齐 + 本次 4 项修复）**从未真机跑过**。开一台 e2-micro 跑 Postfix 全链路。易爆点：Ubuntu 22 `saslpasswd2` 依赖 `sasl2-bin`（已装）/ chroot 同步 `/var/spool/postfix/etc/sasldb2` 权限 / `smtp.根域` A 记录与 mailcow autodiscover 冲突。
- **老 KumoMTA VPS 升级**：brutal 用新 CSV 账号（`info@根域`）登老 VPS，需重跑 Stage C 重渲 `smtp_auth.lua`。

> 完整审计报告（2026-05-23，含已自我推翻的 P1-3/P1-4 与好实践清单）汇总入口在 `D:\CLAUDE_MEMORY\P1-P5-汇总执行清单-v2.md`；项目内临时副本 `AUDIT_2026-05-23.md` 已于 2026-05-30 删除（真问题已摘入本节）。

---

## 12. 常用操作

```bash
cd /d/gcp-mailnode

# 先构建前端（必须）
cd frontend
npm install
npm run build
cd ..

# 编译（自动 bump 版本 + wails build → releases/）
bash build.sh

# 开发模式
wails dev
```

---

## 13. 相关记忆 / 文档

- 总览：`D:\项目总览.md`
- 跨项目共享记忆：`D:\CLAUDE_MEMORY\gcp-mailnode.md` + `architecture.md`
- 项目记忆：`C:\Users\ukima\.claude\projects\D--\memory\project_gcp_mailnode.md`
- 协同记忆：`project_gcp_mailnode_brutal_sync.md`
- 不自动打包偏好：`feedback_gcp_mailnode_no_autobuild.md`
- GitHub：`ukimaru88/gcp-mailnode`（私有）
- 凭据：`D:\CLAUDE_MEMORY\credentials.md`

---

## 14. 续接说明

新对话第一句说"**继续 gcp-mailnode**" / "**继续节点**"。

**当前进行中的任务**：v0.2.8 代码已完成（P0 开放中继 + 域名注入 + 配额自愈 + OAuth 超时 4 项修复，全加防回归测试，`go test ./...` 全绿），已 commit+push，但 **exe 未打包**（等用户说「打包」再 `bash build.sh`）。⚠️ **下个动作首选**：① 打包 v0.2.8 出 exe ② **端到端实测**——v0.2.1→v0.2.8 一堆改动从未真机跑过，开一台 e2-micro 跑 Postfix 全链路验证。次要修复清单见 §11「待办（本轮审核新发现）」。
