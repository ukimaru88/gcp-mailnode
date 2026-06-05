import { useEffect, useRef, useState } from 'react'
import { Play, RefreshCw, Check, ChevronRight, Rocket, Server, Globe, Settings2, X, Copy } from 'lucide-react'
import {
  ListGCPCredentials,
  ListAliyunCredentials,
  ListVPSTemplates,
  ListStaticIPs,
  ListVPS,
  GetBatchProgress,
  // @ts-ignore - bindings 会在 wails build 时重新生成
  StartStageA,
  // @ts-ignore
  StartStageB,
  // @ts-ignore
  StartStageC,
  // @ts-ignore
  BatchSetPTR,
  // @ts-ignore
  PruneBatchIPs,
  // @ts-ignore
  ClearDNSBLCache,
} from '../../wailsjs/go/main/App'
// @ts-ignore - bindings 会在 wails build 时重新生成
import { main } from '../../wailsjs/go/models'
import { useToast } from '../components/Toast'
import { useConfirm } from '../components/ConfirmDialog'

declare global {
  interface Window {
    runtime?: {
      EventsOn: (name: string, cb: (data: any) => void) => () => void
      EventsOff: (name: string) => void
    }
  }
}

// v0.2.15：region 锁回单区域东京 asia-northeast1（v0.2.14 双区域实测负优化已回退）。

interface LogLine {
  slot?: number
  level: string
  msg: string
  created_at?: string
  batch_id?: string
}

const logColor = (level: string): string => {
  const l = (level || '').toUpperCase()
  if (l === 'ERROR' || l === 'FATAL') return 'text-red-400'
  if (l === 'WARN' || l === 'WARNING') return 'text-amber-400'
  if (l === 'DEBUG') return 'text-slate-500'
  if (l === 'SUCCESS') return 'text-green-400'
  return 'text-slate-300'
}

const statusBadge = (s: string) => {
  const map: Record<string, string> = {
    running: 'bg-green-500/20 text-green-300',
    pending: 'bg-amber-500/20 text-amber-300',
    error: 'bg-red-500/20 text-red-300',
    success: 'bg-green-500/20 text-green-300',
    failed: 'bg-red-500/20 text-red-300',
    reserved: 'bg-sky-500/20 text-sky-300',
    in_use: 'bg-indigo-500/20 text-indigo-300',
    released: 'bg-slate-500/20 text-slate-400',
    clean: 'bg-green-500/20 text-green-300',
    dirty: 'bg-red-500/20 text-red-300',
    set: 'bg-green-500/20 text-green-300',
    partial: 'bg-orange-500/20 text-orange-300',
    deployed: 'bg-green-500/20 text-green-300',
    deploying: 'bg-indigo-500/20 text-indigo-300',
  }
  return `px-1.5 py-0.5 rounded text-xs ${map[s] || 'bg-slate-500/20 text-slate-400'}`
}

const fmtTime = (v: any): string => {
  if (!v) return ''
  try { return new Date(v as string).toLocaleTimeString('zh-CN') } catch { return '' }
}

const steps = [
  { n: 1, label: '预留 IP', icon: Rocket },
  { n: 2, label: '购买 VPS', icon: Server },
  { n: 3, label: '搭建邮局', icon: Settings2 },
  { n: 4, label: '批量 RDNS', icon: Globe },
] as const

function LogPanel({ logs, title }: { logs: LogLine[]; title: string }) {
  const endRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' })
  }, [logs])
  return (
    <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-3 flex flex-col" style={{ minHeight: 260 }}>
      <div className="text-xs text-slate-400 mb-2 font-medium">{title}</div>
      <div className="flex-1 bg-slate-950 rounded-md border border-slate-700/50 p-2 overflow-auto font-mono text-xs" style={{ minHeight: 220, maxHeight: 400 }}>
        {logs.length === 0 && <div className="text-slate-600 text-center py-10">等待日志...</div>}
        {logs.map((e, i) => (
          <div key={i} className={`py-0.5 ${logColor(e.level)}`}>
            {e.created_at && <span className="text-slate-600 mr-2">{fmtTime(e.created_at)}</span>}
            {typeof e.slot === 'number' && <span className="text-slate-500 mr-2">[slot {e.slot}]</span>}
            <span className="text-slate-500 mr-1">[{e.level}]</span>
            <span>{e.msg}</span>
          </div>
        ))}
        <div ref={endRef} />
      </div>
    </div>
  )
}

export default function Batch() {
  const { toast } = useToast()
  const confirmDlg = useConfirm()

  const [step, setStep] = useState<1 | 2 | 3 | 4>(1)
  const [batchID, setBatchID] = useState('')

  // 共用基础数据
  const [gcps, setGcps] = useState<main.GCPCredentialDTO[]>([])
  const [alis, setAlis] = useState<main.AliyunCredentialDTO[]>([])
  const [tpls, setTpls] = useState<main.VPSTemplateDTO[]>([])

  // Step 2 / 3 / 4 数据
  const [cleanIPs, setCleanIPs] = useState<main.StaticIPDTO[]>([])
  const [allBatchIPs, setAllBatchIPs] = useState<main.StaticIPDTO[]>([])
  const [batchVPS, setBatchVPS] = useState<main.VPSInstanceDTO[]>([])

  // 各步独立日志
  const [logsA, setLogsA] = useState<LogLine[]>([])
  const [logsB, setLogsB] = useState<LogLine[]>([])
  const [logsC, setLogsC] = useState<LogLine[]>([])
  const [logsD, setLogsD] = useState<LogLine[]>([])

  // Step 1 表单（v0.1.57 简化：region 锁东京、GCP 账号自动取首个、IP 前缀过滤删除；输入是「服务器数」）
  const [tplA, setTplA] = useState('')
  const [vpsCount, setVpsCount] = useState(1)
  const [dnsblTh, setDnsblTh] = useState(1)
  // v0.1.72：IP 前缀黑名单恢复 UI 配置；默认排除 34. / 35.（两段都是 GCP 标识段，声誉差）
  const [ipPrefixExclude, setIpPrefixExclude] = useState<string>('34.\n35.')
  // 0 = 无限循环直到达到目标或所有 region 配额耗尽；>0 = 总尝试上限 = N * 此值
  const [maxRetry, setMaxRetry] = useState(0)
  // v0.2.4：跳过 DNSBL 检测开关，默认关闭（保持严格审查）
  const [skipDNSBL, setSkipDNSBL] = useState(false)
  // v0.2.16：region 单选。东京当前池 100% 是 34./35.，首尔 18% 是 8.230.x（实测）
  // v0.2.24：多区域可选（亚洲 7 个 GCP 区域），首尔默认勾选（实测 18% 非 34./35.）
  const [selectedRegions, setSelectedRegions] = useState<string[]>(['asia-northeast3'])
  const [batchProgress, setBatchProgress] = useState<{ total: number; succeeded: number; failed: number; status: string } | null>(null)
  const batchStatus = batchProgress?.status || ''
  const [progressStartAt, setProgressStartAt] = useState<number>(0)
  // 当前正在执行的 task ID：Stage A 用 batchID；Stage C/D 返回独立 taskID
  const [currentTaskID, setCurrentTaskID] = useState<string>('')
  // Step 1 → 2 过渡：用户勾选要保留的 clean IP，其余自动释放
  const [selectedCleanIPs, setSelectedCleanIPs] = useState<Set<string>>(new Set())
  const [pruning, setPruning] = useState(false)

  // Step 2 表单（只剩模板 + root 密码）
  const [tplB, setTplB] = useState('')
  const [rootPwd, setRootPwd] = useState('')

  // Step 3 / 4 选择
  const [selectedVPS, setSelectedVPS] = useState<Set<string>>(new Set())

  // Step 3 搭建邮局 Modal
  const [deployModalOpen, setDeployModalOpen] = useState(false)
  const [hideClientIP, setHideClientIP] = useState(true)
  // v0.2.9：第三步当场选搭建方式，''=跟随各 VPS 模板，否则统一覆盖
  const [deployTypeSel, setDeployTypeSel] = useState<'' | 'kumomta' | 'postfix'>('')
  // v0.2.19：邮箱账号 local-part（默认 info），仅 [a-z0-9._-]
  const [mailUser, setMailUser] = useState<string>('info')
  const [domainIPText, setDomainIPText] = useState('')
  // v0.2.25：每个根域分几台 VPS（用户场景 30 台 / 10 域 = 3）
  const [vpsPerDomain, setVpsPerDomain] = useState<number>(1)
  // v0.2.26：用户手动改过 vpsPerDomain 后停止自动填（避免覆盖用户意图）
  const [vpsPerDomainTouched, setVpsPerDomainTouched] = useState(false)
  // 子域命名模式：{N} 占位会被 1..vpsPerDomain 替换。@/空 = 直接用根域（仅 vpsPerDomain=1 时）
  const [subdomainPattern, setSubdomainPattern] = useState<string>('mail{N}')

  // v0.2.26：自动算 vpsPerDomain = ceil(readyVPS / 根域数)，让用户输入根域后软件自动
  // 把 N 台 VPS 平均分到所有根域。用户主动改过 input 则停止自动填。
  const autoFillVpsPerDomain = (text: string) => {
    if (vpsPerDomainTouched) return
    const roots = text.split(/\r?\n/).map(l => l.trim()).filter(l => l.length > 0 && !l.startsWith('#') && l.includes('.') && !l.startsWith('.') && !l.endsWith('.'))
    const vpsCount = batchVPS.filter(v => v.ip && v.deploy_status === 'vps_running').length
    if (roots.length > 0 && vpsCount > 0) {
      const auto = Math.max(1, Math.ceil(vpsCount / roots.length))
      setVpsPerDomain(auto)
    }
  }
  const [aliID, setAliID] = useState('')

  const appendLog = (setter: React.Dispatch<React.SetStateAction<LogLine[]>>) => (data: any) => {
    if (!data) return
    setter(prev => [...prev.slice(-499), {
      slot: data.slot,
      level: data.level || 'INFO',
      msg: data.msg || JSON.stringify(data),
      created_at: data.created_at,
      batch_id: data.batch_id,
    }])
  }

  // 订阅 4 个 stage 日志
  useEffect(() => {
    const rt = window.runtime
    if (!rt) return
    const offA = rt.EventsOn('stage-a:log', appendLog(setLogsA))
    const offB = rt.EventsOn('stage-b:log', appendLog(setLogsB))
    const offC = rt.EventsOn('stage-c:log', appendLog(setLogsC))
    const offD = rt.EventsOn('stage-d:log', appendLog(setLogsD))
    return () => { offA?.(); offB?.(); offC?.(); offD?.() }
  }, [])

  const refreshBasics = async () => {
    try {
      const [g, a, t] = await Promise.all([
        ListGCPCredentials(),
        ListAliyunCredentials(),
        ListVPSTemplates(),
      ])
      setGcps((g || []).filter(x => x.enabled))
      setAlis((a || []).filter(x => x.enabled))
      setTpls(t || [])
    } catch (e: any) {
      toast('error', '加载失败: ' + (e?.message || e))
    }
  }
  useEffect(() => { refreshBasics() }, [])

  const refreshCleanIPs = async (silent = false) => {
    if (!batchID) { if (!silent) toast('warning', '尚未启动批次'); return }
    try {
      const all = await ListStaticIPs()
      const batchAll = (all || []).filter((x: any) => x.batch_id === batchID)
      const cleans = batchAll.filter((x: any) => x.status === 'clean')
      setAllBatchIPs(batchAll)
      setCleanIPs(cleans)
      if (!silent) {
        const reserved = batchAll.filter((x: any) => x.status === 'reserved').length
        const released = batchAll.filter((x: any) => x.status === 'released').length
        toast('success', `批次状态: ${cleans.length} clean / ${reserved} 检测中 / ${released} 已释放`)
      }
    } catch (e: any) {
      if (!silent) toast('error', '刷新失败: ' + (e?.message || e))
    }
  }

  // 各阶段轮询：Stage A 刷 clean IP 清单；Stage B/C/D 刷 batch VPS 列表 + 进度条
  useEffect(() => {
    if (!batchID) return
    if (step !== 1 && step !== 2 && step !== 3 && step !== 4) return
    const tick = async () => {
      if (step === 1) refreshCleanIPs(true)
      if (step === 2 || step === 3 || step === 4) refreshBatchVPS(true)
      // Stage A 用 batchID，Stage C/D 用 currentTaskID；fallback 用 batchID
      const queryID = currentTaskID || batchID
      try {
        const p: any = await GetBatchProgress(queryID)
        if (p?.status) {
          setBatchProgress({
            total: p.total || 0,
            succeeded: p.succeeded || 0,
            failed: p.failed || 0,
            status: p.status,
          })
        }
      } catch {}
    }
    tick()
    const timer = setInterval(tick, 3000)
    return () => clearInterval(timer)
  }, [batchID, step, currentTaskID])

  const refreshBatchVPS = async (silent = false) => {
    if (!batchID) { if (!silent) toast('warning', '尚未启动批次'); return }
    try {
      const all = await ListVPS()
      const filtered = (all || []).filter((x: any) => x.batch_id === batchID)
      setBatchVPS(filtered)
      if (!silent) {
        setSelectedVPS(new Set())
        toast('success', `已刷新，批次中 ${filtered.length} 台 VPS`)
      }
    } catch (e: any) {
      if (!silent) toast('error', '刷新失败: ' + (e?.message || e))
    }
  }

  // v0.1.57：多 NIC 时按 slot_group 整组切换；单 NIC 时 slot_group=ip.id（一组一 IP）行为退化
  const toggleCleanIP = (id: string) => {
    const target = cleanIPs.find(x => x.id === id)
    if (!target) return
    const sg = (target as any).slot_group || target.id
    const groupIDs = cleanIPs.filter(x => (((x as any).slot_group) || x.id) === sg).map(x => x.id)
    setSelectedCleanIPs(prev => {
      const n = new Set(prev)
      const allOn = groupIDs.every(g => n.has(g))
      if (allOn) groupIDs.forEach(g => n.delete(g))
      else groupIDs.forEach(g => n.add(g))
      return n
    })
  }
  const toggleAllCleanIPs = () => {
    if (selectedCleanIPs.size === cleanIPs.length) setSelectedCleanIPs(new Set())
    else setSelectedCleanIPs(new Set(cleanIPs.map(x => x.id)))
  }
  const toggleVPS = (id: string) => {
    setSelectedVPS(prev => {
      const n = new Set(prev)
      n.has(id) ? n.delete(id) : n.add(id)
      return n
    })
  }
  const toggleAllVPS = () => {
    if (selectedVPS.size === batchVPS.length) setSelectedVPS(new Set())
    else setSelectedVPS(new Set(batchVPS.map(v => v.id)))
  }

  // 复制工具
  const copyText = async (text: string, label = '已复制') => {
    try {
      await navigator.clipboard.writeText(text)
      toast('success', label)
    } catch (e: any) {
      toast('error', '复制失败: ' + (e?.message || e))
    }
  }

  // v0.1.57：从所选模板读 nic_count；多 NIC 模板（如 8）触发 1 台 VPS = nic_count 个 IP 的逻辑
  const tplNICCount = (() => {
    const t = tpls.find(x => x.id === tplA)
    const n = (t as any)?.nic_count
    return n && n > 0 ? n : 1
  })()
  const totalIPCount = vpsCount * tplNICCount

  // === Step 1 操作 ===
  // v0.1.57：region 锁东京、GCP 账号取首个、IP 前缀过滤删除；输入是「服务器数量」自动 ×NIC 算 IP
  const startStageA = async () => {
    if (gcps.length === 0) { toast('warning', '请先到「凭证」页添加 GCP 账号'); return }
    if (!tplA) { toast('warning', '请选择开机模板'); return }
    if (vpsCount < 1) { toast('warning', '服务器数量需 >= 1'); return }

    const totalIPs = vpsCount * tplNICCount
    const req = {
      gcp_cred_ids: [gcps[0].id],
      template_id: tplA,
      count: totalIPs,
      regions: selectedRegions.length > 0 ? selectedRegions : ['asia-northeast1'],
      dnsbl_threshold: dnsblTh,
      max_retry_per_slot: maxRetry,
      ip_prefix_filter: [],
      ip_prefix_exclude: ipPrefixExclude.split('\n').map(s => s.trim()).filter(s => s.length > 0),
      nic_count: tplNICCount,
      skip_dnsbl: skipDNSBL,
    } as any

    try {
      setLogsA([])
      setCleanIPs([])
      setAllBatchIPs([])
      setSelectedCleanIPs(new Set())
      setBatchProgress({ total: totalIPs, succeeded: 0, failed: 0, status: 'stage-a-running' })
      setProgressStartAt(Date.now())
      const id = await StartStageA(req)
      setBatchID(id)
      setCurrentTaskID(id)
      toast('success', `阶段 A 已启动（${vpsCount} 台 × ${tplNICCount} NIC = ${totalIPs} IP）: ` + id.slice(0, 8))
    } catch (e: any) {
      toast('error', '启动失败: ' + (e?.message || e))
    }
  }

  // Step 1 → 2：未勾选的 IP 释放回 GCP，保留勾选的带进 Step 2
  const pruneAndNext = async () => {
    if (!batchID) { toast('warning', '无批次'); return }
    if (selectedCleanIPs.size === 0) {
      toast('warning', '请至少勾选 1 个 IP 带进下一步')
      return
    }
    const unpicked = cleanIPs.length - selectedCleanIPs.size
    if (unpicked > 0 && !await confirmDlg({
      message: `将保留勾选的 ${selectedCleanIPs.size} 个 IP，释放其余 ${unpicked} 个未勾选的 IP 回 GCP。继续？`,
      danger: true,
    })) return
    try {
      setPruning(true)
      const keepIDs = Array.from(selectedCleanIPs)
      const released = await PruneBatchIPs(batchID, keepIDs)
      toast('success', `已释放 ${released} 个未勾选的 IP`)
      await refreshCleanIPs()
      setStep(2)
    } catch (e: any) {
      toast('error', '释放失败: ' + (e?.message || e))
    } finally {
      setPruning(false)
    }
  }

  // === Step 2 操作 ===
  const startStageB = async () => {
    if (!batchID) { toast('warning', '未找到批次 ID'); return }
    if (!tplB) { toast('warning', '请选择模板'); return }

    // v0.1.7+：SSH 用密钥登录，root 密码占位（后端保留字段但不用于登录）
    const req = {
      template_id: tplB,
      root_password: 'n/a-key-auth',
    } as any

    try {
      setLogsB([])
      setProgressStartAt(Date.now())
      setCurrentTaskID(batchID) // Stage B 沿用 batchID
      setBatchProgress(prev => prev ? { ...prev, succeeded: 0, failed: 0, status: 'stage-b-running' } : { total: batchVPS.length || cleanIPs.length || 1, succeeded: 0, failed: 0, status: 'stage-b-running' })
      await StartStageB(batchID, req)
      toast('success', '阶段 B 已启动')
    } catch (e: any) {
      toast('error', '启动失败: ' + (e?.message || e))
    }
  }

  // === Step 3 操作（搭建邮局）===
  const openStageCModal = async () => {
    setDeployModalOpen(true)
  }

  // 解析纯域名列表（按行 trim，忽略空行和 # 注释）
  const parseDomains = (text: string): string[] =>
    // v0.2.11：除空行/注释外，拒绝以 . 开头/结尾、不含 . 的伪域名（避免传给后端拼出 ".com" 这种坏 FQDN，
    // 之前有过一次 myhostname=.com 导致 postfix master 起不来的事故）
    text.split(/\r?\n/)
      .map(l => l.trim())
      .filter(l => l.length > 0 && !l.startsWith('#'))
      .filter(l => l.includes('.') && !l.startsWith('.') && !l.endsWith('.'))

  // v0.2.25：根域 × VPS 数 → 展开 FQDN（mail1.根域、mail2.根域…）配对 VPS IP。
  //
  // 展开模式（subdomainPattern）：
  //   - "@"        每个 VPS 一个根域名（旧行为，vpsPerDomain 必须为 1）
  //   - "mail{N}"  N 从 1 开始递增，展开 mail1.根域 / mail2.根域 / ...
  //
  // 例：根域 ['a.com', 'b.com']，vpsPerDomain=3，pattern="mail{N}"
  //   FQDN 序列 = [mail1.a.com, mail2.a.com, mail3.a.com, mail1.b.com, mail2.b.com, mail3.b.com]
  //   按 readyVPS 顺序配对。
  const buildDomainIPMap = (): { map: Record<string, string>; rootMap: Record<string, string>; pairs: Array<{ domain: string; ip: string }>; extraDomains: string[]; extraVPS: Array<{ ip: string; name: string }> } => {
    const roots = parseDomains(domainIPText)
    const readyVPS = batchVPS.filter(v => v.ip && v.deploy_status === 'vps_running')
    // 展开 FQDN
    const fqdns: string[] = []
    const fqdnRoot: Record<string, string> = {}
    const per = Math.max(1, vpsPerDomain)
    for (const root of roots) {
      for (let i = 1; i <= per; i++) {
        let fqdn: string
        if (per === 1 && subdomainPattern.trim() === '@') {
          fqdn = root // 旧行为：单 VPS 用根域
        } else {
          const sub = subdomainPattern.replace(/\{N\}/g, String(i)).trim()
          fqdn = sub === '' || sub === '@' ? root : `${sub}.${root}`
        }
        fqdns.push(fqdn)
        fqdnRoot[fqdn] = root
      }
    }
    const pairs: Array<{ domain: string; ip: string }> = []
    const map: Record<string, string> = {}
    const n = Math.min(fqdns.length, readyVPS.length)
    for (let i = 0; i < n; i++) {
      pairs.push({ domain: fqdns[i], ip: readyVPS[i].ip })
      map[fqdns[i]] = readyVPS[i].ip
    }
    const extraDomains = fqdns.slice(n)
    const extraVPS = readyVPS.slice(n).map(v => ({ ip: v.ip, name: v.name }))
    return { map, rootMap: fqdnRoot, pairs, extraDomains, extraVPS }
  }

  const confirmStageC = async () => {
    const { map, rootMap, pairs, extraDomains, extraVPS } = buildDomainIPMap()
    if (pairs.length === 0) {
      toast('warning', '请至少填写 1 个域名，且本批次至少有 1 台 vps_running 的 VPS')
      return
    }
    if (!aliID) { toast('warning', '请选择阿里云账号'); return }
    if (extraDomains.length > 0) {
      if (!await confirmDlg(`有 ${extraDomains.length} 个域名没有对应 VPS（只会建前 ${pairs.length} 对），继续？\n未分配的域名：${extraDomains.join(', ')}`)) return
    } else if (extraVPS.length > 0) {
      if (!await confirmDlg(`有 ${extraVPS.length} 台 VPS 没有对应域名（不会建 DNS 记录），继续？`)) return
    }

    try {
      setLogsC([])
      setProgressStartAt(Date.now())
      setBatchProgress({ total: pairs.length, succeeded: 0, failed: 0, status: 'stage-c-running' })
      const taskID = await StartStageC({
        domain_ip_map: map,
        root_domain_map: rootMap,
        aliyun_cred_id: aliID,
        hide_client_ip: hideClientIP,
        deploy_type: deployTypeSel,
        mail_user: mailUser,
      } as any)
      if (taskID) setCurrentTaskID(taskID)
      toast('success', `阶段 C 已启动${taskID ? `：${taskID}` : ''}`)
      setDeployModalOpen(false)
    } catch (e: any) {
      toast('error', '启动失败: ' + (e?.message || e))
    }
  }

  // === Step 4 操作（批量 RDNS）===
  const runPTR = async () => {
    if (selectedVPS.size === 0) { toast('warning', '请勾选至少一台 VPS'); return }
    try {
      setLogsD([])
      setProgressStartAt(Date.now())
      setBatchProgress({ total: selectedVPS.size, succeeded: 0, failed: 0, status: 'stage-d-running' })
      const taskID = await BatchSetPTR(Array.from(selectedVPS))
      if (taskID) setCurrentTaskID(taskID) // 进度条改跟 PTR task
      toast('success', `已提交批量 PTR 请求${taskID ? `：${taskID}` : ''}`)
    } catch (e: any) {
      toast('error', '提交失败: ' + (e?.message || e))
    }
  }

  return (
    <div className="p-6 h-full overflow-auto">
      <h1 className="text-xl font-bold text-slate-100 mb-4">批量开机（4 阶段流水线）</h1>

      {/* Stepper */}
      <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4 mb-4">
        <div className="flex items-center">
          {steps.map((s, idx) => {
            const Icon = s.icon
            const isActive = step === s.n
            const isDone = step > s.n
            return (
              <div key={s.n} className="flex items-center flex-1 last:flex-none">
                <button
                  onClick={() => { if (pruning) return; if (isDone || isActive) setStep(s.n as any) }}
                  className={`flex items-center gap-2 px-3 py-2 rounded-lg transition-colors ${
                    isActive ? 'bg-indigo-500/20 text-indigo-300 border border-indigo-500/40'
                    : isDone ? 'bg-green-500/10 text-green-400 border border-green-500/30 hover:bg-green-500/20'
                    : 'bg-slate-900 text-slate-500 border border-slate-700'
                  }`}
                >
                  <div className={`w-6 h-6 rounded-full flex items-center justify-center text-xs font-bold ${
                    isActive ? 'bg-indigo-500 text-white'
                    : isDone ? 'bg-green-500 text-white'
                    : 'bg-slate-700 text-slate-400'
                  }`}>
                    {isDone ? <Check size={14} /> : s.n}
                  </div>
                  <Icon size={14} />
                  <span className="text-sm font-medium">{s.label}</span>
                </button>
                {idx < steps.length - 1 && (
                  <div className={`flex-1 h-0.5 mx-2 ${step > s.n ? 'bg-green-500/40' : 'bg-slate-700'}`} />
                )}
              </div>
            )
          })}
        </div>
        {batchID && (
          <div className="mt-3 text-xs text-slate-400 space-y-2">
            <div className="flex items-center gap-3">
              <span>当前批次：<span className="font-mono text-indigo-300">#{batchID.slice(0, 8)}</span></span>
              {batchStatus && (
                <span className={`px-2 py-0.5 rounded-full font-mono text-[10px] ${
                  batchStatus.endsWith('running') ? 'bg-indigo-500/20 text-indigo-300 animate-pulse'
                  : batchStatus.endsWith('done')   ? 'bg-green-500/20 text-green-300'
                  : batchStatus === 'failed'       ? 'bg-red-500/20 text-red-300'
                  : batchStatus === 'cancelled'    ? 'bg-slate-500/20 text-slate-400'
                  : 'bg-slate-500/20 text-slate-400'
                }`}>
                  {batchStatus === 'stage-a-running' ? '⏳ 筛选 IP 中...' :
                   batchStatus === 'stage-a-done'    ? '✅ 筛选完成' :
                   batchStatus === 'stage-b-running' ? '⏳ 购买 VPS 中...' :
                   batchStatus === 'stage-b-done'    ? '✅ VPS 就绪' :
                   batchStatus === 'stage-c-running' ? '⏳ 搭建邮局中...' :
                   batchStatus === 'stage-c-done'    ? '✅ 邮局就绪' :
                   batchStatus === 'failed'          ? '❌ 失败' :
                   batchStatus === 'cancelled'       ? '⏹ 已取消' :
                   batchStatus}
                </span>
              )}
            </div>
            {/* 进度条 + ETA */}
            {batchProgress && batchProgress.total > 0 && batchStatus.endsWith('running') && (() => {
              const done = batchProgress.succeeded + batchProgress.failed
              const pct = Math.min(100, Math.round((done * 100) / batchProgress.total))
              let eta = '--'
              if (progressStartAt > 0 && done > 0 && done < batchProgress.total) {
                const elapsed = (Date.now() - progressStartAt) / 1000
                const rate = done / elapsed
                const remaining = Math.ceil((batchProgress.total - done) / rate)
                eta = remaining < 60 ? `${remaining}s` : `${Math.ceil(remaining / 60)}min`
              }
              return (
                <div>
                  <div className="w-full h-1.5 bg-slate-800 rounded overflow-hidden">
                    <div className="h-full bg-indigo-500 transition-all duration-500" style={{ width: `${pct}%` }} />
                  </div>
                  <div className="mt-1 flex items-center gap-3 text-[10px]">
                    <span>✅ {batchProgress.succeeded}/{batchProgress.total}</span>
                    {batchProgress.failed > 0 && <span className="text-red-400">❌ {batchProgress.failed}</span>}
                    <span className="text-slate-500">{pct}%</span>
                    <span className="text-slate-500">预计还需 {eta}</span>
                  </div>
                </div>
              )
            })()}
          </div>
        )}
      </div>

      {/* Step 1 */}
      {step === 1 && (
        <div className="space-y-4">
          <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
            <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4 space-y-4">
              <h2 className="font-semibold text-slate-100">Step 1 · 预留 IP（仅开机 + DNSBL 检测）</h2>

              <div className="text-xs text-slate-400 bg-slate-900/40 border border-slate-700/40 rounded-md px-3 py-2 leading-relaxed">
                <div>使用账号：<span className="text-indigo-300">{gcps[0]?.name || '（无可用 GCP 凭证）'}</span></div>
                <div>
                  <div className="mb-1">区域（亚洲 7 选 N，多选并发筛各自池）：</div>
                  <div className="grid grid-cols-2 md:grid-cols-3 gap-1.5">
                    {([
                      { v: 'asia-northeast1', label: '🇯🇵 日本东京', tip: '池 ≈100% 是 34./35.' },
                      { v: 'asia-northeast2', label: '🇯🇵 日本大阪', tip: '池 100% 是 34.97' },
                      { v: 'asia-northeast3', label: '🇰🇷 韩国首尔 ⭐', tip: '18% 是 8.230.x' },
                      { v: 'asia-east1', label: '🇹🇼 台湾彰化', tip: '未实测' },
                      { v: 'asia-east2', label: '🇭🇰 香港', tip: '未实测' },
                      { v: 'asia-southeast1', label: '🇸🇬 新加坡', tip: '未实测' },
                      { v: 'asia-southeast2', label: '🇮🇩 印尼雅加达', tip: '未实测' },
                    ] as const).map(r => {
                      const checked = selectedRegions.includes(r.v)
                      return (
                        <label key={r.v}
                               className={`flex items-start gap-1.5 px-2 py-1 rounded border text-[11px] cursor-pointer transition-colors ${
                                 checked
                                   ? 'bg-indigo-500/15 border-indigo-500/50 text-indigo-200'
                                   : 'bg-slate-900 border-slate-700 text-slate-400 hover:border-slate-600'
                               }`}>
                          <input type="checkbox" className="accent-indigo-500 mt-0.5"
                                 checked={checked}
                                 onChange={e => {
                                   if (e.target.checked) setSelectedRegions([...selectedRegions, r.v])
                                   else setSelectedRegions(selectedRegions.filter(x => x !== r.v))
                                 }} />
                          <div className="flex-1">
                            <div className="font-medium">{r.label}</div>
                            <div className="text-[10px] text-slate-500">{r.v} · {r.tip}</div>
                          </div>
                        </label>
                      )
                    })}
                  </div>
                  <div className="text-[10px] text-amber-400 mt-1.5">
                    💡 每个 worker 固定绑定一个 region 持续撑满各自池（v0.2.14 平均分流的负优化已修）。已选 <span className="font-mono text-indigo-300">{selectedRegions.length || 1}</span> 个区域，并发 worker 自动 ≥ {(selectedRegions.length || 1) * 2}。
                  </div>
                </div>
                <div>IP 前缀：<span className="text-indigo-300">不过滤</span>（仅 DNSBL）</div>
              </div>

              <div>
                <label className="block text-xs text-slate-400 mb-1">开机模板</label>
                <select className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                        value={tplA} onChange={e => setTplA(e.target.value)}>
                  <option value="">请选择...</option>
                  {tpls.map(t => <option key={t.id} value={t.id}>{t.name}{t.is_preset ? ' [预设]' : ''}{(t as any).nic_count > 1 ? ` · ${(t as any).nic_count} NIC` : ''}</option>)}
                </select>
              </div>

              <div className="grid grid-cols-3 gap-3">
                <div>
                  <label className="block text-xs text-slate-400 mb-1">服务器数量</label>
                  <input type="number" min={1} max={50}
                         className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                         value={vpsCount} onChange={e => setVpsCount(Math.max(1, Math.min(50, Number(e.target.value) || 1)))} />
                  <p className="text-[10px] text-slate-500 mt-0.5">将预留 <span className="text-indigo-400 font-semibold">{vpsCount} × {tplNICCount} = {totalIPCount}</span> 个清洁 IP</p>
                  {totalIPCount > 150 && (
                    <p className="text-[10px] text-amber-400 mt-0.5">⚠ 接近企业默认 STATIC_ADDRESSES=175 配额，可去 GCP Console → IAM & Admin → Quotas 提升。30-150 台属于常规范围，无需提额。</p>
                  )}
                </div>
                <div>
                  <label className="block text-xs text-slate-400 mb-1">DNSBL 阈值（命中几个 RBL 判脏）</label>
                  <input type="number" min={1}
                         className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                         value={dnsblTh} onChange={e => setDnsblTh(Number(e.target.value) || 1)} />
                  <p className="text-[10px] text-slate-500 mt-0.5">1=最严格（默认，命中任一 RBL 即重申）；2=稍宽松</p>
                </div>
                <div>
                  <label className="block text-xs text-slate-400 mb-1">每槽最大重试</label>
                  <input type="number" min={0}
                         className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                         value={maxRetry} onChange={e => setMaxRetry(Number(e.target.value) || 0)} />
                  <p className="text-[10px] text-slate-500 mt-0.5">0=无限循环直到达到目标（推荐）；&gt;0=总尝试上限=目标×此值</p>
                </div>
              </div>

              <div>
                <label className="block text-xs text-slate-400 mb-1">IP 前缀黑名单（每行一个，匹配前缀的 IP 自动跳过）</label>
                <textarea
                  className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none font-mono"
                  rows={3}
                  placeholder="34.&#10;例如：34. / 35.230. / 35."
                  value={ipPrefixExclude}
                  onChange={e => setIpPrefixExclude(e.target.value)} />
                <p className="text-[10px] text-slate-500 mt-0.5">默认排除 <span className="text-amber-400 font-mono">34.</span> / <span className="text-amber-400 font-mono">35.</span>（GCP 段，声誉差）。清空则不过滤。</p>
              </div>

              <div className={`p-3 rounded-md border ${skipDNSBL ? 'bg-amber-500/10 border-amber-500/40' : 'bg-slate-900/50 border-slate-700'}`}>
                <label className="flex items-center gap-2 cursor-pointer">
                  <input type="checkbox" className="accent-amber-500"
                         checked={skipDNSBL} onChange={e => setSkipDNSBL(e.target.checked)} />
                  <span className="text-sm text-slate-200">跳过 DNSBL 检测（只看 IP 前缀）</span>
                </label>
                <p className="text-[10px] text-slate-400 mt-1 ml-6">
                  {skipDNSBL
                    ? '⚠ 关闭了 25 个 RBL 黑名单的全栈检查；仅前缀过滤生效。适合主流邮箱（Gmail/Outlook/Yahoo）不查的 UCEPROTECT/SORBS-DUL/SpamRATS 等噪声列表把好 IP 误判脏的场景。速度大幅提升。'
                    : '默认关闭，按 DNSBL 阈值审查所有 RBL 列表。开关后只受上面的"IP 前缀黑名单"约束。'}
                </p>
              </div>

              <div className="flex items-center justify-between pt-2">
                <div className="inline-flex gap-2">
                  <button className="bg-indigo-600 hover:bg-indigo-500 text-white rounded-md px-4 py-2 text-sm inline-flex items-center gap-2"
                          onClick={startStageA}>
                    <Play size={14} /> 🚀 开始筛选干净 IP
                  </button>
                  <button className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-2 text-xs inline-flex items-center gap-1.5"
                          title="清空 DNSBL 缓存，强制每个 IP 重新查 RBL。RBL 列表更新后用一次。"
                          onClick={async () => {
                            if (!await confirmDlg('清空 DNSBL 缓存？之后每个 IP 都会重新查 26 个 RBL，筛选会变慢。')) return
                            try {
                              const n = await ClearDNSBLCache()
                              toast('success', `已清空 ${n} 条缓存`)
                            } catch (e: any) { toast('error', '清缓存失败: ' + (e?.message || e)) }
                          }}>
                    🧹 清 DNSBL 缓存
                  </button>
                </div>
                {batchID && (
                  <span className="text-xs text-slate-400">
                    当前批次 #{batchID.slice(0, 8)}，筛完后在下方清单里勾选要进入 Step 2 的 IP
                  </span>
                )}
              </div>
            </div>

            <LogPanel logs={logsA} title="阶段 A 日志（预留 IP / DNSBL）" />
          </div>

          {/* 已筛选的干净 IP 清单 */}
          <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4 space-y-3">
            <div className="flex items-center justify-between">
              <div>
                <h2 className="font-semibold text-slate-100">已筛选的干净 IP 清单</h2>
                <p className="text-xs text-slate-500 mt-0.5">
                  勾选要保留进入 Step 2 的 IP，未勾选的在"下一步"时会自动释放回 GCP。
                  已选 <span className="text-indigo-300 font-mono">{selectedCleanIPs.size}</span> / {cleanIPs.length}
                </p>
                {allBatchIPs.length > 0 && (
                  <p className="text-[10px] text-slate-500 mt-0.5">
                    批次总状态：
                    <span className="text-green-400 mx-1">clean {cleanIPs.length}</span> /
                    <span className="text-sky-400 mx-1">检测中 {allBatchIPs.filter(i => i.status === 'reserved').length}</span> /
                    <span className="text-red-400 mx-1">已释放 {allBatchIPs.filter(i => i.status === 'released').length}</span>
                    （每 3 秒自动刷新）
                  </p>
                )}
              </div>
              <div className="inline-flex gap-2">
                <button onClick={() => refreshCleanIPs(false)}
                        className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-xs inline-flex items-center gap-1.5">
                  <RefreshCw size={13} /> 刷新
                </button>
                <button onClick={() => copyText(
                  (selectedCleanIPs.size > 0 ? cleanIPs.filter(i => selectedCleanIPs.has(i.id)) : cleanIPs).map(i => i.ip).join('\n'),
                  `已复制 ${selectedCleanIPs.size > 0 ? selectedCleanIPs.size : cleanIPs.length} 个 IP`)}
                        disabled={cleanIPs.length === 0}
                        className="bg-slate-700 hover:bg-slate-600 disabled:opacity-40 disabled:cursor-not-allowed text-slate-200 rounded-md px-3 py-1.5 text-xs inline-flex items-center gap-1.5">
                  <Copy size={13} /> 复制{selectedCleanIPs.size > 0 ? '勾选' : '全部'}
                </button>
                <button onClick={pruneAndNext} disabled={!batchID || pruning || cleanIPs.length === 0 || selectedCleanIPs.size === 0}
                        className="bg-indigo-600 hover:bg-indigo-500 disabled:opacity-40 disabled:cursor-not-allowed text-white rounded-md px-3 py-1.5 text-xs inline-flex items-center gap-1.5">
                  {pruning ? '释放中...' : <>下一步 · 释放未勾选并进入 Step 2 ({selectedCleanIPs.size}) <ChevronRight size={13} /></>}
                </button>
              </div>
            </div>
            <div className="overflow-x-auto max-h-80">
              <table className="w-full text-xs">
                <thead className="bg-slate-900/50 text-slate-400 sticky top-0">
                  <tr>
                    <th className="px-2 py-1.5"><input type="checkbox"
                           checked={selectedCleanIPs.size === cleanIPs.length && cleanIPs.length > 0}
                           onChange={toggleAllCleanIPs} /></th>
                    <th className="text-left px-2 py-1.5 font-medium">IP</th>
                    <th className="text-left px-2 py-1.5 font-medium">分组</th>
                    <th className="text-left px-2 py-1.5 font-medium">区域</th>
                    <th className="text-left px-2 py-1.5 font-medium">DNSBL</th>
                    <th className="text-left px-2 py-1.5 font-medium">创建</th>
                    <th className="text-right px-2 py-1.5 font-medium">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {cleanIPs.length === 0 && <tr><td colSpan={7} className="text-center py-6 text-slate-500">暂无 clean IP，请点击刷新</td></tr>}
                  {(() => {
                    // v0.1.57：按 slot_group 排序展示，让同一组的 IP 紧挨着；多 NIC 时勾选必须整组
                    const sorted = [...cleanIPs].sort((a, b) => {
                      const sa = (a as any).slot_group || a.id
                      const sb = (b as any).slot_group || b.id
                      if (sa !== sb) return sa < sb ? -1 : 1
                      const ia = (a as any).nic_index ?? 0
                      const ib = (b as any).nic_index ?? 0
                      return ia - ib
                    })
                    const groupSize: Record<string, number> = {}
                    for (const x of sorted) {
                      const sg = (x as any).slot_group || x.id
                      groupSize[sg] = (groupSize[sg] || 0) + 1
                    }
                    return sorted.map(i => {
                      const sg = (i as any).slot_group || i.id
                      const ni = (i as any).nic_index ?? 0
                      const sz = groupSize[sg] || 1
                      return (
                        <tr key={i.id} className={`border-t border-slate-700/40 ${selectedCleanIPs.has(i.id) ? 'bg-indigo-500/5' : ''}`}>
                          <td className="px-2 py-1.5"><input type="checkbox"
                                 checked={selectedCleanIPs.has(i.id)} onChange={() => toggleCleanIP(i.id)} /></td>
                          <td className="px-2 py-1.5 text-slate-200 font-mono">{i.ip}</td>
                          <td className="px-2 py-1.5 text-slate-400 font-mono text-[10px]">
                            {sz > 1
                              ? <span title={sg}>{sg.slice(0, 6)}·#{ni}<span className="text-slate-500"> /{sz}</span></span>
                              : <span className="text-slate-600">单</span>}
                          </td>
                          <td className="px-2 py-1.5 text-slate-300">{i.region}</td>
                          <td className="px-2 py-1.5 text-green-400">{i.dnsbl_result || 'clean'}</td>
                          <td className="px-2 py-1.5 text-slate-500">{fmtTime(i.created_at)}</td>
                          <td className="px-2 py-1.5 text-right">
                            <button onClick={() => copyText(i.ip, `已复制 ${i.ip}`)}
                                    className="text-slate-400 hover:text-indigo-300 inline-flex items-center gap-1">
                              <Copy size={12} />
                            </button>
                          </td>
                        </tr>
                      )
                    })
                  })()}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      )}

      {/* Step 2 */}
      {step === 2 && (
        <div className="space-y-4">
          <div className="bg-indigo-500/10 border border-indigo-500/30 rounded-lg p-3 text-sm text-indigo-200">
            {batchID
              ? <>批次 <span className="font-mono">#{batchID.slice(0, 8)}</span>，将用 Step 1 预留的 clean IP 购买 VPS 并挂载。</>
              : <>当前无批次，请先在 Step 1 启动。</>}
          </div>

          <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
            <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4 space-y-4">
              <h2 className="font-semibold text-slate-100">Step 2 · 购买 VPS 并挂载 IP</h2>

              <div>
                <label className="block text-xs text-slate-400 mb-1">模板（机型/磁盘）</label>
                <select className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                        value={tplB} onChange={e => setTplB(e.target.value)}>
                  <option value="">请选择...</option>
                  {tpls.map(t => <option key={t.id} value={t.id}>{t.name}{t.is_preset ? ' [预设]' : ''}{(t as any).nic_count > 1 ? ` · ${(t as any).nic_count} NIC` : ''}</option>)}
                </select>
              </div>

              <div className="text-xs text-slate-400 bg-slate-900/50 border border-slate-700/50 rounded-md px-3 py-2 space-y-1">
                <div>🔑 SSH 登录已改用密钥认证（v0.1.7+）。软件自动生成密钥对并注入到 VPS，不需要设置 root 密码。</div>
                <div>🔥 首次开机会自动在 GCP 项目里建防火墙规则 <span className="font-mono text-indigo-300">mailnode-mail-ports</span>（入站 22/25/80/443/465/587/2525）+ <span className="font-mono text-indigo-300">mailnode-smtp-out</span>（出站 25/465/587）。VPS 打 <span className="font-mono">mail-node</span> tag 自动命中，旧规则会自动校正。</div>
                <div>登录命令和私钥在「资源清单」页可复制/下载。</div>
              </div>

              <div className="flex items-center justify-between pt-2">
                <button className="bg-indigo-600 hover:bg-indigo-500 text-white rounded-md px-4 py-2 text-sm inline-flex items-center gap-2"
                        onClick={startStageB} disabled={!batchID}>
                  <Play size={14} /> 🏭 购买 VPS 并挂载
                </button>
                <button className="text-sm text-indigo-300 hover:text-indigo-200 inline-flex items-center gap-1"
                        onClick={async () => { await refreshBatchVPS(); setStep(3) }}>
                  → 进入 Step 3 <ChevronRight size={14} />
                </button>
              </div>
            </div>

            <LogPanel logs={logsB} title="阶段 B 日志（购买 VPS / 挂载 IP）" />
          </div>
        </div>
      )}

      {/* Step 3 搭建邮局 */}
      {step === 3 && (
        <div className="space-y-4">
          <div className="bg-indigo-500/10 border border-indigo-500/30 rounded-lg p-3 text-sm text-indigo-200">
            为本批次 VPS 搭建 KumoMTA 邮局，建立域名 → IP 的 A 记录映射。
          </div>

          <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
            <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4 space-y-3">
              <div className="flex items-center justify-between">
                <h2 className="font-semibold text-slate-100">批次 VPS 清单</h2>
                <button onClick={() => refreshBatchVPS(false)}
                        className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-xs inline-flex items-center gap-1.5">
                  <RefreshCw size={13} /> 刷新
                </button>
              </div>

              <div className="overflow-x-auto max-h-80">
                <table className="w-full text-xs">
                  <thead className="bg-slate-900/50 text-slate-400 sticky top-0">
                    <tr>
                      <th className="text-left px-2 py-1.5 font-medium">名称</th>
                      <th className="text-left px-2 py-1.5 font-medium">IP</th>
                      <th className="text-left px-2 py-1.5 font-medium">Zone</th>
                      <th className="text-left px-2 py-1.5 font-medium">部署</th>
                    </tr>
                  </thead>
                  <tbody>
                    {batchVPS.length === 0 && <tr><td colSpan={4} className="text-center py-6 text-slate-500">无数据，请刷新</td></tr>}
                    {batchVPS.map(v => (
                      <tr key={v.id} className="border-t border-slate-700/40">
                        <td className="px-2 py-1.5 text-slate-200">{v.name}</td>
                        <td className="px-2 py-1.5 text-slate-300 font-mono">{v.ip || '-'}</td>
                        <td className="px-2 py-1.5 text-slate-400 text-xs">{(v as any).zone || '-'}</td>
                        <td className="px-2 py-1.5"><span className={statusBadge(v.deploy_status)}>{v.deploy_status}</span></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              <div className="flex items-center justify-between pt-2">
                <button onClick={openStageCModal} disabled={!batchID || batchVPS.length === 0}
                        className="bg-indigo-600 hover:bg-indigo-500 disabled:opacity-40 disabled:cursor-not-allowed text-white rounded-md px-4 py-2 text-sm inline-flex items-center gap-2">
                  <Play size={14} /> 📝 导入域名映射开始搭建
                </button>
                <button className="text-sm text-indigo-300 hover:text-indigo-200 inline-flex items-center gap-1"
                        onClick={() => setStep(4)}>
                  → 进入 Step 4（批量 RDNS） <ChevronRight size={14} />
                </button>
              </div>
            </div>

            <LogPanel logs={logsC} title="阶段 C 日志（搭建邮局 / A 记录 / KumoMTA）" />
          </div>
        </div>
      )}

      {/* Step 4 批量 RDNS */}
      {step === 4 && (
        <div className="space-y-4">
          <div className="bg-amber-500/10 border border-amber-500/30 rounded-lg p-3 text-sm text-amber-200">
            等 A 记录 DNS 生效 1-5 分钟后再点。GCP 多 NIC 只支持 nic0 公网 PTR；mail1~mail8 的 A/EHLO 会自动配置，额外 NIC PTR 不会重复重试。
          </div>

          <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
            <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4 space-y-3">
              <div className="flex items-center justify-between">
                <h2 className="font-semibold text-slate-100">批次 VPS 清单</h2>
                <div className="inline-flex gap-2">
                  <button onClick={() => refreshBatchVPS(false)}
                          className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-xs inline-flex items-center gap-1.5">
                    <RefreshCw size={13} /> 刷新
                  </button>
                  <button onClick={runPTR} disabled={selectedVPS.size === 0}
                          className="bg-indigo-600 hover:bg-indigo-500 disabled:opacity-40 disabled:cursor-not-allowed text-white rounded-md px-3 py-1.5 text-xs">
                    🔁 批量设 PTR ({selectedVPS.size})
                  </button>
                </div>
              </div>

              <div className="overflow-x-auto max-h-96">
                <table className="w-full text-xs">
                  <thead className="bg-slate-900/50 text-slate-400 sticky top-0">
                    <tr>
                      <th className="px-2 py-1.5"><input type="checkbox"
                             checked={selectedVPS.size === batchVPS.length && batchVPS.length > 0}
                             onChange={toggleAllVPS} /></th>
                      <th className="text-left px-2 py-1.5 font-medium">名称</th>
                      <th className="text-left px-2 py-1.5 font-medium">FQDN</th>
                      <th className="text-left px-2 py-1.5 font-medium">IP</th>
                      <th className="text-left px-2 py-1.5 font-medium">部署</th>
                      <th className="text-left px-2 py-1.5 font-medium">PTR</th>
                    </tr>
                  </thead>
                  <tbody>
                    {batchVPS.length === 0 && <tr><td colSpan={6} className="text-center py-6 text-slate-500">无数据，请刷新</td></tr>}
                    {batchVPS.map(v => (
                      <tr key={v.id} className="border-t border-slate-700/40">
                        <td className="px-2 py-1.5"><input type="checkbox"
                              checked={selectedVPS.has(v.id)} onChange={() => toggleVPS(v.id)} /></td>
                        <td className="px-2 py-1.5 text-slate-200">{v.name}</td>
                        <td className="px-2 py-1.5 text-slate-300 text-xs">{v.fqdn || '-'}</td>
                        <td className="px-2 py-1.5 text-slate-300 font-mono">{v.ip || '-'}</td>
                        <td className="px-2 py-1.5"><span className={statusBadge(v.deploy_status)}>{v.deploy_status}</span></td>
                        <td className="px-2 py-1.5"><span className={statusBadge(v.ptr_status || '-')} title={v.ptr_status === 'partial' ? 'nic0 PTR 真生效，nic1~7 GCP silent ignore（87.5% 流量 PTR 残缺）' : ''}>{v.ptr_status || '-'}</span></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>

            <LogPanel logs={logsD} title="阶段 D 日志（批量 RDNS）" />
          </div>
        </div>
      )}

      {/* Stage C Modal - 导入域名映射 */}
      {deployModalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4">
          <div className="bg-[#1a1d27] rounded-xl border border-slate-700/50 w-full max-w-2xl p-5 space-y-4 max-h-[90vh] overflow-auto">
            <div className="flex items-center justify-between">
              <h3 className="font-semibold text-slate-100">📝 搭建邮局 · 导入域名映射</h3>
              <button onClick={() => setDeployModalOpen(false)} className="text-slate-500 hover:text-slate-300">
                <X size={16} />
              </button>
            </div>

            <div className="text-xs text-slate-400">
              一行一个域名（域名需已在阿里云托管）。软件会按 Step 2 <span className="font-mono text-indigo-300">vps_running</span> 状态的 VPS 顺序自动分配 IP。
              当前批次可用 VPS：<span className="text-indigo-300 font-mono">{batchVPS.filter(v => v.ip && v.deploy_status === 'vps_running').length}</span> 台
              {(() => {
                const types = new Set(batchVPS.filter(v => v.ip && v.deploy_status === 'vps_running').map(v => (v as any).deploy_type || 'kumomta'))
                if (types.size === 0) return null
                const list = Array.from(types).map(t => {
                  if (t === 'mailcow') return '📥 mailcow（收发一体）'
                  if (t === 'postfix') return '📬 Postfix + OpenDKIM（纯发信）'
                  return '🚀 KumoMTA（纯发信）'
                }).join(' / ')
                return <div className="mt-1 text-slate-500">将按模板类型部署：<span className="text-indigo-300">{list}</span></div>
              })()}
            </div>

            <div>
              <textarea
                className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-xs font-mono focus:border-indigo-500 outline-none"
                rows={8}
                value={domainIPText}
                onChange={e => { setDomainIPText(e.target.value); autoFillVpsPerDomain(e.target.value) }}
                placeholder={'example1.com\nexample2.com\nexample3.com\n# 以 # 开头的行会被忽略'}
              />
              <div className="text-[11px] text-slate-500 mt-1">
                每行一个<span className="text-slate-300">根域名</span>。下方"每域 VPS 数"&gt;1 时软件自动展开成 mail1./mail2./...
              </div>
            </div>

            {/* v0.2.25：每域 VPS 数 + 子域命名模式 */}
            <div className="grid grid-cols-2 gap-2">
              <div>
                <label className="block text-xs text-slate-400 mb-1">每域 VPS 数</label>
                <input type="number" min={1} max={50}
                       className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                       value={vpsPerDomain}
                       onChange={e => { setVpsPerDomain(Math.max(1, Math.min(50, Number(e.target.value) || 1))); setVpsPerDomainTouched(true) }} />
                <div className="text-[10px] text-slate-500 mt-0.5">
                  10 个根域 × 3 = 30 个 FQDN
                </div>
              </div>
              <div>
                <label className="block text-xs text-slate-400 mb-1">子域命名（{`{N}`} 替换为 1..N）</label>
                <input type="text"
                       className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none"
                       value={subdomainPattern}
                       disabled={vpsPerDomain <= 1}
                       placeholder="mail{N}"
                       onChange={e => setSubdomainPattern(e.target.value.toLowerCase().replace(/[^a-z0-9.\-_{}n]/g, '').slice(0, 30))} />
                <div className="text-[10px] text-slate-500 mt-0.5">
                  {vpsPerDomain <= 1
                    ? '（每域 1 台时直接用根域，不加前缀）'
                    : `示例 FQDN: ${subdomainPattern.replace(/\{N\}/g, '1')}.example.com / ${subdomainPattern.replace(/\{N\}/g, '2')}.example.com ...`}
                </div>
              </div>
            </div>

            {/* 实时 preview：域名 → IP 配对预览 */}
            {(() => {
              const { pairs, extraDomains, extraVPS } = buildDomainIPMap()
              if (pairs.length === 0 && extraDomains.length === 0) return null
              return (
                <div className="bg-slate-950 border border-slate-700/50 rounded-md p-2 text-xs space-y-1 max-h-40 overflow-auto">
                  <div className="text-slate-400">即将建立 {pairs.length} 条映射：</div>
                  {pairs.map((p, i) => (
                    <div key={i} className="font-mono text-slate-300">
                      <span className="text-indigo-300">{p.domain}</span>
                      <span className="text-slate-500"> → </span>
                      <span>{p.ip}</span>
                    </div>
                  ))}
                  {extraDomains.length > 0 && (
                    <div className="text-amber-400 pt-1 border-t border-slate-700/50 mt-1">
                      ⚠ {extraDomains.length} 个多余域名不会被分配：{extraDomains.join(', ')}
                    </div>
                  )}
                  {extraVPS.length > 0 && (
                    <div className="text-amber-400 pt-1 border-t border-slate-700/50 mt-1">
                      ⚠ {extraVPS.length} 台 VPS 没有对应域名（不会建 DNS）
                    </div>
                  )}
                </div>
              )
            })()}

            <div>
              <label className="block text-xs text-slate-400 mb-1">阿里云账号（管理 DNS）</label>
              <select className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                      value={aliID} onChange={e => setAliID(e.target.value)}>
                <option value="">请选择...</option>
                {alis.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}
              </select>
            </div>

            <div>
              <label className="block text-xs text-slate-400 mb-1">
                邮箱账号前缀 <span className="text-slate-500 font-normal">（最终 = 前缀@根域；默认 info）</span>
              </label>
              <input type="text" className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none"
                     placeholder="info"
                     value={mailUser}
                     onChange={e => setMailUser(e.target.value.toLowerCase().replace(/[^a-z0-9._-]/g, '').slice(0, 32))} />
              <div className="text-[11px] text-slate-500 mt-0.5">
                可改成 sales / hello / contact / no-reply 等。仅允许 <span className="font-mono">a-z 0-9 . - _</span>，不能以点/连字符开头结尾。
                最终账号示例：<span className="font-mono text-indigo-300">{(mailUser || 'info').replace(/^[.\-]+|[.\-]+$/g, '') || 'info'}@example.com</span>
              </div>
            </div>

            <div>
              <label className="block text-xs text-slate-400 mb-1">搭建方式</label>
              <div className="grid grid-cols-3 gap-2">
                {([['', '跟随模板'], ['kumomta', '🚀 KumoMTA'], ['postfix', '📬 Postfix']] as const).map(([val, label]) => (
                  <button key={val} type="button" onClick={() => setDeployTypeSel(val)}
                          className={`px-2 py-2 rounded-md border text-sm transition-colors ${
                            deployTypeSel === val
                              ? 'bg-indigo-500/20 border-indigo-500/50 text-indigo-200'
                              : 'bg-slate-900 border-slate-700 text-slate-400 hover:border-slate-600'
                          }`}>
                    {label}
                  </button>
                ))}
              </div>
              <div className="text-[11px] text-slate-500 mt-1">
                {deployTypeSel === ''
                  ? '按每台 VPS 所用模板的类型部署（默认）。'
                  : deployTypeSel === 'postfix'
                    ? 'Postfix + OpenDKIM 纯发信（仅单 NIC；多 NIC 自动回退 KumoMTA）。屏蔽 IP 开关仅对 KumoMTA 生效。'
                    : 'KumoMTA 纯发信 MTA（透明中继 + DKIM）。'}
              </div>
            </div>

            <label className="flex items-start gap-2 p-3 bg-slate-900 border border-slate-700 rounded-md cursor-pointer">
              <input type="checkbox" className="accent-indigo-500 mt-0.5"
                     checked={hideClientIP} onChange={e => setHideClientIP(e.target.checked)} />
              <div>
                <div className="text-sm text-slate-200">
                  🛡️ 屏蔽发件端真实 IP（{hideClientIP ? '已启用：trace_headers=false' : '关闭：透明中继暴露 IP'}）
                </div>
                <div className="text-xs text-slate-500">
                  {hideClientIP
                    ? '勾选：KumoMTA 不写客户端 IP 到 Received 头；配合 brutal-mailer Persona 伪造链使用'
                    : '取消：KumoMTA 标准模式，Received 头会暴露发件端真实 IP（适合通知类邮件不伪造身份）'}
                </div>
              </div>
            </label>

            <div className="flex justify-end gap-2 pt-2">
              <button onClick={() => setDeployModalOpen(false)}
                      className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm">
                取消
              </button>
              <button onClick={confirmStageC}
                      className="bg-indigo-600 hover:bg-indigo-500 text-white rounded-md px-3 py-1.5 text-sm">
                确认搭建
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
