# gcp-mailnode - 项目档案

> 最后更新：2026-05-30
> 当前版本：**v0.2.25**（`version.txt`；Stage C 多 VPS 共享根域，自动展开 mail1./mail2./...）
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
- 机型：代码支持 e2-micro/small/medium 等多档手动选；**v0.2.8 起开机预设只留 e2-micro，新建模板默认也是 e2-micro**（原 v0.2.0 的 small/medium 预设已删 + 旧预设软隐藏）

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
| **v0.2.25** | 最新。**Stage C 多 VPS 共享根域**：用户场景 30 台 VPS 但只有 10 个域名，需自动分配 mail1./mail2./mail3. 子域。前端 Batch.tsx Stage C 弹窗加"每域 VPS 数"输入（默认 1）+"子域命名模式"输入（默认 `mail{N}`，`{N}`=1..N）；buildDomainIPMap 按 roots × per 展开 FQDN list（如 mail1.a.com / mail2.a.com / mail3.a.com / mail1.b.com / ...）配对 VPS IP。后端 StageCRequest 加 `RootDomainMap map[string]string`（FQDN→根域），Stage C 主循环用 `SubdomainFromFQDN(fqdn, rootDomain)` 反推子域；落库 `domain=rootDomain, fqdn=完整FQDN`；A 记录 `RR=subdomain` 而非硬编码 `@`；DKIM/SPF/MX/DMARC 由 `DNSRRsForSubdomain` 自动适配子域（早就支持）。空 RootDomainMap 时退化为 fqdn==rootDomain，完全兼容老版本逐域逐 VPS |
| v0.2.24 | **亚洲多区域并发筛 IP**：用户需求"日本+韩国+新加坡+台湾一键都筛"。后端 stages.go region 白名单扩到 7 个（asia-northeast1/2/3 + asia-east1/2 + asia-southeast1/2）；**worker 改固定绑定 region**（按 workerID 分配），不再 v0.2.14 那种 attempt 轮询——这样每个 region 池被独立 worker 持续撑满各自 175 配额，hold 翻新机制单池生效；concurrency 自动 ≥ len(regions)×2 保证每区至少 2 worker，硬上限 20；claimSlot 仍全局抢（哪区先有干净 IP 先填）。前端 Batch.tsx Step 1 改 checkbox 多选（默认勾首尔），每项显示池前缀特征 + 国旗 emoji。req.Regions 字段已存在（v0.2.14 用过被回退）现复用 |
| v0.2.23 | **修 Site Verification API URL 大小写**：v0.2.21 用 `/siteverification/v1/`（全小写）调 Google API 全部返回 HTTP 404 "URL not found"。Google REST API 路径**区分大小写**——正确为 `/siteVerification/v1/`（大 V）。一处 const 改正后，GetVerifyToken/InsertWebResource/IsDomainVerified 三个方法全部能正常调用 |
| v0.2.22 | **SpamRATS 改回参与判定**：v0.2.20 一度标 DisplayOnly 仅展示，因担心"GCP 全段一刀切误杀"；用户实测筛 IP 清单证明 SpamRATS 是**精准命中**（同 region 大部分 IP 没命中，少数命中），用户希望"命中即剔除"。把 4 个 SpamRATS RBL 的 DisplayOnly 标志去掉，命中纳入 HitLists/HitCount，verdict=dirty 直接 holdDirty 跳过该 IP。DisplayOnly 机制保留（未来某 RBL 可能用），但当前无 RBL 使用。Stage A 清单里不会再出现 [display:SpamRATS-...] 标记的 clean IP |
| v0.2.21 | **GCP 域名所有权自动验证**：用户报 Auto-PTR 失败 `Invalid value for 'publicPtrDomainName': 'zobetype.net'. Please verify ownership of the PTR domain`。根因 GCP 自 2023 起强制要求 PTR 域名所有权验证。新增 `internal/gcp/siteverify.go` 集成 Google Site Verification REST API（v1）：`GetVerifyToken` 拿 `google-site-verification=xxx` token、`InsertWebResource` 完成验证、`IsDomainVerified` 复查。stages.go 新增 `ensureDomainVerified` 流程：缓存命中 → 跳过；否则 GetVerifyToken → 阿里云 DNS AddRecord TXT @（不用 Upsert 避免覆盖 SPF）→ 等 60s 传播 → Google InsertWebResource。`sync.Map` 缓存已验证域名，`sync.Mutex` 串行化同域名并发。autoSetPTRForSupportedNICs 在调 SetInstancePTR 前自动验证。失败时友好提示（SA 缺权限/API 未启用/DNS 不在阿里云）但不阻断流程。**前提：项目需启用 Site Verification API + SA 加权限** |
| v0.2.20 | **SpamRATS RBL 加回 + DisplayOnly 设计**：用户实测要求 RBL 检测包含 SpamRATS-Dyna。实测 10 个 DoH 服务商发现 Cloudflare/Google/Quad9/AdGuard/AliDNS 等查 SpamRATS 全 RCODE=2/超时失败（SpamRATS 限速这些公网递归），**唯独 NextDNS 0.5s 返回 LISTED**。改动：① Zone 加 `DisplayOnly bool` 字段；CheckResult 加 `DisplayOnlyHits []string`，HitCount/HitLists 不计 DisplayOnly 命中，errorCount/Clean 判定也跳过；UI 仍能看到命中 ② dohLookup 路由：`.spamrats.com` 域名走 `dohEndpointsSpamRATS=[dns.nextdns.io]`，其他走 Cloudflare/Google ③ SpamRATS 4 个全标 DisplayOnly=true（云 IP 通病：所有 GCP/AWS/Azure 段一刀切列入，对 Gmail/Outlook/Yahoo 投递无影响）④ Stage A 日志带 `[仅展示命中 SpamRATS-Dyna]` 提示；DB hit_lists 字段加 `[display:...]` 前缀供前端展示 |
| v0.2.19 | **Stage C 自定义邮箱账号前缀**：之前所有部署的账号硬编码 `info@根域`，用户需要 `sales/hello/contact/no-reply` 等场景无法切换。改动：① templates.go 新增 `SanitizeMailUser`（[a-z0-9._-]，1-32 字符，不能以点/连字符首尾）+ `DeployVars.OverrideMailUser` 方法 ② render() 加 `{MAIL_USER_LOCAL}` 占位符（从 v.Username 取 @ 前缀） ③ install_mailcow.sh 把硬编码 `info` 改占位符（postfix/kumomta 已自动跟随 Username） ④ StageCRequest + DeployOpts 加 MailUser 字段，3 个 deploy 函数透传 ⑤ Batch.tsx Stage C 弹窗加输入框，实时显示账号预览，前端做字符过滤兜底。空值/非法回退 info |
| v0.2.18 | **修过时 24 IP/CPU 警告**：Batch.tsx 服务器数量提示从硬编码 `> 24 警告` 改为 `> 150` 才提醒（贴合企业默认 STATIC_ADDRESSES=175 配额）。之前 24 是 v0.1.74 之前 GCP 老默认值，企业账号实际 IP 175/CPU 1500/INSTANCES 6000，30 台 e2-micro 仅需 30 IP/7.5 CPU/30 实例，远在配额内 |
| v0.2.17 | **修 DNSBL 漏报 + 加速**：用户反馈 mxtoolbox 显示 IP 在 SpamRATS-Dyna LISTED 但软件判 clean。根因 SpamRATS 权威 DNS 限速 Cloudflare/Google DoH 递归查询，4 个 SpamRATS RBL 经 DoH 全超时（错误数 6/26 < 半数 13 → 仍判 Clean）。处理：① **删 SpamRATS 全 4 个** RBL（DoH 不可达 + 对云 IP 一刀切误杀 + 主流邮件商 Gmail/Outlook/Yahoo 不查，对实际投递无影响；PBL 类云 IP 屏蔽已被 Spamhaus ZEN 含 PBL 覆盖）② **DoH 三 endpoint 串行重试**（Cloudflare 1.1.1.1/1.0.0.1 + Google dns.google），每次独立 2.5s 超时，任一返回 NXDOMAIN/NOERROR 立即返回 ③ defaultTimeout 3s→8s 容纳三次重试。实测单 IP 0.6s 完成 22 RBL（v0.2.12 是 3s/26 RBL；v0.2.16 含 SpamRATS 拖累实际 7.5s）。给用户透明感：UI 现在显示"22 个高权重 RBL"不再误导 |
| v0.2.16 | **UI 加区域单选（东京/大阪/首尔）**。实测 IP 池分布（cmd/ippool hold 150 个）：东京 asia-northeast1 当前 100% 是 34./35.；大阪 asia-northeast2 100% 是 34.97；**韩国 asia-northeast3 约 18% 是 8.230.x**（Google 2023+ 新段，避开 34./35. 主力段的唯一可行选项）。stages.go 按 req.Regions[0] 切单 region（白名单校验只允 northeast1/2/3）；Batch.tsx Step 1 加下拉，每个区域显示池子特征 + 适用场景说明。默认仍东京（保守），切首尔时 UI 提示韩国→日本邮箱网络 ~50ms |
| v0.2.15 | **回退 v0.2.14 双区域**：实测负优化（用户 1000+ reserve 都没命中非 34./35.）。dirtyIPHolder "占满池子触发 GCP 翻新"机制必须**单池子撑到 175 配额顶**才生效；双区域让 worker 50/50 分流，东京 hold 池只撑到 ~87 个（远不到 175），翻新机制失效；同时大阪池实测 100% 是 34.97 段，双区不仅没翻倍命中率反而把东京原本能命中 104./136. 的概率砍半。回到 v0.2.13 单东京。**经验教训**：v0.1.63 dirtyIPHolder 注释明确说"用户配额越大 hold 越多脏 IP 翻新效果越好"，本质是"撑满 GCP 内部池"，加 region 是反向的（稀释 hold 而非翻倍 hold） |
| ~~v0.2.14~~ | 已回退。**解锁日本东京+大阪双区域并发筛 IP**：之前 stages.go:101 锁死 `asia-northeast1`，用户排除 34./35. 主力段时 hold 池单区域 175 配额一撞顶就 60s 冷却。改为 `["asia-northeast1", "asia-northeast2"]` 双区并发，**hold 池容量翻倍 350、两个池子翻新独立**，非 34./35.（如 104./136.）命中率显著上升。Stage B 已天然支持（IP 自带 region，自动选 zone）；多 NIC `groupCleanIPs` 按 (cred\|region) 分区保证同组同区不跨拼。UI Step 1 卡片提示"日本东京 + 大阪 双区并发"。app_templates.go 预设 Regions 同步两区域 |
| v0.2.13 | **修 render() 渲染器根因 bug**（v0.2.3 引入 Postfix 路径起就存在）：`strings.ReplaceAll(s, "{FQDN}", v.FQDN)` 会把 shell 变量引用 `${FQDN}` 中的 `{FQDN}` 子串也替换，导致 `${FQDN}` → `$madouchuanm.com` → bash 把 `$madouchuanm` 当未定义变量展开成空 → 实际值变成 ".com" → /etc/hostname=.com → myhostname=.com → postfix master fatal exit。**这就是之前 v0.2.10 实测的 `myhostname=.com` 真根因**，v0.2.11 sanity check 只拦了部署没修根因。修法：render 三阶段——① 用 sentinel `\x00GMSHELL\x00<i>` 保护所有 `${VAR}/${VAR%xx}` 引用 ② 替换 Go 占位符 ③ 恢复 shell 引用。新增 `TestRender_PreservesShellVarRefs` 防回归。顺带修同名子串 bug：`{FQDN_BARE}` 必须在 `{FQDN}` 之前替换 |
| v0.2.12 | **DNSBL DoH 加速**：之前零值 net.Resolver 走系统 stub resolver 把 25 个 RBL 并发查询串行化，单 IP ~25s；尝试 PreferGo + 直打 1.1.1.1:53 又被本地路由器封 53 端口（UDP/TCP 都不通）；最终方案 DoH (DNS over HTTPS) 走 443，绕开 53 封锁。Cloudflare + Google JSON API 轮询，HTTP/2 keep-alive 复用。实测单 IP 26 RBL 0.08s 健康检查 / 3.00s 满量查询（=defaultTimeout 上限），**用户 20 IP 并发场景预计 60s → 3-5s**（6-10× 提速）。改动单点：[dnsbl.go](internal/dnsbl/dnsbl.go) queryZone 调 dohLookup 替代 net.Resolver |
| v0.2.11 | **Postfix 真机部署事故修复**：一次实测发现 `myhostname=.com` 导致 postfix master 起不来、但脚本只报"端口没监听"无法定位（根因未确定，疑似前端域名输入或 Stage C race）。三道防线：① install_postfix.sh 渲染变量后立即 sanity check（FQDN/DOMAIN 不能为空/不能 . 开头-结尾/必须含 .），异常直接 exit 11 ② 自检失败时 dump postfix check + systemctl status + journalctl + mail.log + main.cf + /etc/hostname + /etc/hosts ③ Go 端 deployPostfixOnVPS 入口 log 入参与 BuildDeployVars 结果（domain/subdomain/v.FQDN/v.RootDomain），下次出问题能立即看到 ④ 前端 parseDomains 拒绝以 . 开头/结尾、不含 . 的非法行 |
| v0.2.10 | ① 导出新增 `toolkit_short`（`info@根域----密码`，对齐 mail-toolkit 简短导出）+ `toolkit_full`（`账号----密码----host:port----security`）两种 `----` 格式，Export 页加按钮 ② **批量部署第三步加「搭建方式」选择**（跟随模板/KumoMTA/Postfix）：`StageCRequest.DeployType` + `DeployOpts.DeployType` 当场覆盖模板默认，多 NIC 自动回退 kumomta；Batch.tsx Stage C 弹窗加三选按钮 |
| v0.2.9 | 修复审核出的全部待办：① Postfix 系统账号改 nologin、不再 chpasswd（缩攻击面）② clean IP 过继限定本账号 + succeeded clamp 到 Count ③ Stage A 取消时 reserve 后的落库/释放改 background ctx（防孤儿 IP/漏释放）④ PTR 校验窗口 30s→150s + 失 NAT 哨兵错误 `ErrPTRNATLost`（nic0/nic1+ ERROR 告警）⑤ GCP Create/Reserve 的 Wait 失败 best-effort 清孤儿 ⑥ gcloud token 过期 50min→10min ⑦ **GCP Instances/Addresses client 缓存复用**（防 100+ 台撞 ephemeral port）⑧ DNSBL 命中校验 127/8 且排除 127.255.255.0/24 软拒绝码 ⑨ CSV 导出 RFC4180 转义。curl 兜底/systemd 持久化经核实早已实现。新增 dnsbl/errors/域名注入回归测试 |
| v0.2.8 | 安全 + 正确性修复：① **P0 开放中继**（`GenerateMailPassword` 空时回退随机 20 位 [A-Za-z0-9] 密码）② **域名注入**（`render()` 单点 LDH 白名单校验，堵 shell+Lua）③ **配额自愈失效**（`IsQuotaExceeded` 改 `contains(quota)&&contains(exceeded)`，识别 GCP 真实配额 message）④ **OAuth 无超时**（`cfg.Exchange` 改用 timeoutCtx）⑤ 修审计 P1-1 失效测试断言（全部加防回归测试）⑥ **预设模板统一只留 e2-micro**：删 e2-small/e2-medium 预设、软隐藏旧预设；新建模板默认机型仍 e2-micro，前端下拉保留多档供手动选 |
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

> 本节随 2026-05-30「修 P0 + 4 Agent 全量审核 + 修全部待办」更新。审核出的**代码问题已全部修复**（v0.2.8 + v0.2.9），剩下只有需真机环境才能推进的项。

### ✅ v0.2.8 已修（安全 4 项）
- **P0 开放中继 + 空密码系统账号**：`GenerateMailPassword` 空时回退随机 20 位 `[A-Za-z0-9]` 密码，KumoMTA/Postfix/chpasswd/CSV 全链路一致（字符集纯字母数字也规避了 CSV 分隔符打穿）。
- **域名注入（P0 另一半）**：`render()` 单点 `validateDeployDomains` LDH 白名单，堵 shell + Lua 两条路径。
- **配额自愈失效**：`IsQuotaExceeded` 改 `contains("quota")&&contains("exceeded")`，识别 GCP 真实 `Quota '...' exceeded.`；`IsNotFound` 补 `not found`。
- **OAuth 无超时**：`cfg.Exchange` 改用 `timeoutCtx`。
- 顺手修审计 P1-1 失效测试断言（`get_egress_path_config`）。

### ✅ v0.2.9 已修（审核出的全部待办）
- **Postfix 冗余系统账号**：[install_postfix.sh](internal/deploy/templates/install_postfix.sh) `info` 改 `nologin`、不再 `chpasswd`（SASL 走 sasldb 不需要可登录账号）。
- **clean IP 过继计数错位**：[stages.go](internal/deploy/stages.go) reparent 限定本账号 `gcp_cred_id` + `succeeded` clamp 到 `req.Count`。
- **Stage A 取消漏释放**：reserve 成功后落库/release/holdDirty 改 `context.Background()`（`persistCtx`），防取消时留孤儿 IP / 漏释放。
- **PTR**：`verifyReversePTR` 窗口 30s→150s（10×15s）；新增哨兵 `gcp.ErrPTRNATLost`，nic0/nic1+ 失 NAT 时 ERROR 告警提示手动重建 External NAT。
- **GCP Wait 失败清孤儿**：CreateInstance / ReserveStaticAddress 的 `op.Wait` 失败 best-effort 删 VM / 释放 IP（background ctx）。
- **GCP client 复用**：[client.go](internal/gcp/client.go) Client 缓存 Instances/Addresses REST client（懒加载 + Close 统一关），11 处调用点改用缓存，防 100+ 台撞 ephemeral port。
- **gcloud token**：过期标记 50min→10min（防用到已过期 token）。
- **DNSBL 软拒绝误判**：[dnsbl.go](internal/dnsbl/dnsbl.go) 命中校验返回 A 记录在 127/8 且排除 127.255.255.0/24（RBL 错误/超限码）。
- **CSV 导出转义**：[app_resource.go](app_resource.go) toolkit CSV 走 RFC4180 `csvField` 转义。
- **curl 兜底 / systemd 持久化**：经 grep 核实**早已实现**——setup_policy_routing.sh 自装 curl + oneshot service 开机恢复路由；deploy_config.sh `systemctl enable kumomta`；install_postfix.sh enable postfix/opendkim。属文档误列，非真待办。
- 新增回归测试：`dnsbl_test.go` / `errors_test.go` / `TestRender_RejectsInjectionDomain` / `TestGenerateMailPassword_NeverEmpty` 等，`go test ./...` 全绿。

### 🔴 剩余（需真机环境才能推进）
- **端到端实测（头号）**：v0.2.1→v0.2.9 大量改动从未真机跑过。开一台 e2-micro 跑 Postfix 全链路。易爆点：Ubuntu 22 `saslpasswd2` 依赖 `sasl2-bin`（已装）/ chroot 同步 `/var/spool/postfix/etc/sasldb2` 权限 / `smtp.根域` A 记录与 mailcow autodiscover 冲突 / auto-PTR 链路。
- **老 KumoMTA VPS 升级**：brutal 用新 CSV 账号（`info@根域`）登老 VPS 需重跑 Stage C 重渲 `smtp_auth.lua`。

> 完整审计报告（2026-05-23）汇总入口在 `D:\CLAUDE_MEMORY\P1-P5-汇总执行清单-v2.md`；项目内临时副本 `AUDIT_2026-05-23.md` 已于 2026-05-30 删除。

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
