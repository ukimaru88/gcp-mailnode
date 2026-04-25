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

// v0.1.9：固定锁定日本东京 + 大阪。Stage A 后端也硬码。
// 默认日本 2 region；用户可勾选扩展到亚洲其他区（注意：IP 会是对应区的，不再是纯日本品牌）
const ASIA_REGIONS = [
  { code: 'asia-northeast1', label: '日本东京', defaultOn: true },
  { code: 'asia-northeast2', label: '日本大阪', defaultOn: true },
  { code: 'asia-northeast3', label: '韩国首尔', defaultOn: false },
  { code: 'asia-east1',      label: '台湾彰化', defaultOn: false },
  { code: 'asia-east2',      label: '香港',     defaultOn: false },
  { code: 'asia-southeast1', label: '新加坡',   defaultOn: false },
  { code: 'asia-southeast2', label: '雅加达',   defaultOn: false },
]

// IP 前缀过滤选项
// - blacklist: 排除 IPPrefixExclude 里开头的，其余全保留（默认——GCP 日本区 34.x 声誉较差）
// - all: 不过滤，仅靠 DNSBL
type IPPrefixMode = 'blacklist' | 'all'

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

  // Step 1 表单
  const [selectedGCP, setSelectedGCP] = useState<string[]>([])
  const [tplA, setTplA] = useState('')
  const [count, setCount] = useState(1)
  const [dnsblTh, setDnsblTh] = useState(1)
  // 0 = 无限循环直到达到目标或所有 region 配额耗尽；>0 = 总尝试上限 = N * 此值
  const [maxRetry, setMaxRetry] = useState(0)
  const [batchProgress, setBatchProgress] = useState<{ total: number; succeeded: number; failed: number; status: string } | null>(null)
  const batchStatus = batchProgress?.status || ''
  const [progressStartAt, setProgressStartAt] = useState<number>(0)
  // 当前正在执行的 task ID：Stage A 用 batchID；Stage C/D 返回独立 taskID
  const [currentTaskID, setCurrentTaskID] = useState<string>('')
  const [ipPrefixMode, setIpPrefixMode] = useState<IPPrefixMode>('blacklist')
  const [excludePrefixText, setExcludePrefixText] = useState('34.')
  const [selectedRegions, setSelectedRegions] = useState<string[]>(
    ASIA_REGIONS.filter(r => r.defaultOn).map(r => r.code)
  )
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
  const [domainIPText, setDomainIPText] = useState('')
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

  const toggleGCP = (id: string) => {
    setSelectedGCP(prev => prev.includes(id) ? prev.filter(x => x !== id) : [...prev, id])
  }
  const toggleCleanIP = (id: string) => {
    setSelectedCleanIPs(prev => {
      const n = new Set(prev)
      n.has(id) ? n.delete(id) : n.add(id)
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

  // === Step 1 操作 ===
  const startStageA = async () => {
    if (selectedGCP.length === 0) { toast('warning', '请选择至少一个 GCP 账号'); return }
    if (!tplA) { toast('warning', '请选择开机模板'); return }
    if (count < 1) { toast('warning', '机器数量需 >= 1'); return }
    if (selectedRegions.length === 0) { toast('warning', '请至少选择一个 region'); return }

    // 把前端模式映射成后端两个列表
    const ipPrefixFilter: string[] = []
    let ipPrefixExclude: string[] = []
    if (ipPrefixMode === 'blacklist') {
      ipPrefixExclude = excludePrefixText
        .split(/[\s,，、]+/)
        .map(s => s.trim())
        .filter(s => s.length > 0)
        .map(s => s.endsWith('.') ? s : s + '.')
    }

    const req = {
      gcp_cred_ids: selectedGCP,
      template_id: tplA,
      count,
      regions: selectedRegions,
      dnsbl_threshold: dnsblTh,
      max_retry_per_slot: maxRetry,
      ip_prefix_filter: ipPrefixFilter,
      ip_prefix_exclude: ipPrefixExclude,
    } as any

    try {
      setLogsA([])
      setCleanIPs([])
      setAllBatchIPs([])
      setSelectedCleanIPs(new Set())
      setBatchProgress({ total: count, succeeded: 0, failed: 0, status: 'stage-a-running' })
      setProgressStartAt(Date.now())
      const id = await StartStageA(req)
      setBatchID(id)
      setCurrentTaskID(id) // Stage A 用 batchID 作为 taskID
      toast('success', '阶段 A 已启动: ' + id.slice(0, 8))
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
    text.split(/\r?\n/).map(l => l.trim()).filter(l => l.length > 0 && !l.startsWith('#'))

  // 按顺序把域名和本批次 VPS IP 配对
  const buildDomainIPMap = (): { map: Record<string, string>; pairs: Array<{ domain: string; ip: string }>; extraDomains: string[]; extraVPS: Array<{ ip: string; name: string }> } => {
    const domains = parseDomains(domainIPText)
    const readyVPS = batchVPS.filter(v => v.ip && v.deploy_status === 'vps_running')
    const pairs: Array<{ domain: string; ip: string }> = []
    const map: Record<string, string> = {}
    const n = Math.min(domains.length, readyVPS.length)
    for (let i = 0; i < n; i++) {
      pairs.push({ domain: domains[i], ip: readyVPS[i].ip })
      map[domains[i]] = readyVPS[i].ip
    }
    const extraDomains = domains.slice(n)
    const extraVPS = readyVPS.slice(n).map(v => ({ ip: v.ip, name: v.name }))
    return { map, pairs, extraDomains, extraVPS }
  }

  const confirmStageC = async () => {
    const { map, pairs, extraDomains, extraVPS } = buildDomainIPMap()
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
        aliyun_cred_id: aliID,
        hide_client_ip: hideClientIP,
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

              <div>
                <label className="block text-xs text-slate-400 mb-1.5">GCP 账号（多选）</label>
                <div className="flex flex-wrap gap-2">
                  {gcps.length === 0 && <span className="text-xs text-slate-500">无可用 GCP 凭证（需启用）</span>}
                  {gcps.map(c => {
                    const on = selectedGCP.includes(c.id)
                    return (
                      <button key={c.id} type="button" onClick={() => toggleGCP(c.id)}
                              className={`px-2.5 py-1 text-xs rounded-full border transition-colors ${
                                on ? 'bg-indigo-500/20 text-indigo-300 border-indigo-500/40'
                                : 'bg-slate-900 text-slate-300 border-slate-700 hover:border-slate-500'
                              }`}>
                        {c.name}
                      </button>
                    )
                  })}
                </div>
              </div>

              <div>
                <label className="block text-xs text-slate-400 mb-1">开机模板</label>
                <select className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                        value={tplA} onChange={e => setTplA(e.target.value)}>
                  <option value="">请选择...</option>
                  {tpls.map(t => <option key={t.id} value={t.id}>{t.name}{t.is_preset ? ' [预设]' : ''}</option>)}
                </select>
              </div>

              <div className="grid grid-cols-3 gap-3">
                <div>
                  <label className="block text-xs text-slate-400 mb-1">目标干净 IP 数量</label>
                  <input type="number" min={1} max={200}
                         className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                         value={count} onChange={e => setCount(Math.max(1, Math.min(200, Number(e.target.value) || 1)))} />
                  <p className="text-[10px] text-slate-500 mt-0.5">筛到这个数量就停。下一步你再挑要开机的 IP。</p>
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
                <label className="block text-xs text-slate-400 mb-1.5">抓取 region（多选，每个 region 默认配额 8 个 IP）</label>
                <div className="grid grid-cols-2 gap-1.5">
                  {ASIA_REGIONS.map(r => {
                    const on = selectedRegions.includes(r.code)
                    return (
                      <label key={r.code} className={`inline-flex items-center gap-1.5 px-2 py-1 rounded border text-xs cursor-pointer select-none ${on ? 'bg-indigo-500/20 border-indigo-500/40 text-indigo-200' : 'bg-slate-900 border-slate-700 text-slate-400 hover:border-slate-500'}`}>
                        <input type="checkbox" className="accent-indigo-500"
                               checked={on}
                               onChange={() => {
                                 setSelectedRegions(prev => on ? prev.filter(x => x !== r.code) : [...prev, r.code])
                               }} />
                        <span className="font-mono text-[10px]">{r.code}</span>
                        <span>{r.label}</span>
                      </label>
                    )
                  })}
                </div>
                <p className="text-[10px] text-slate-500 mt-1">
                  默认只勾日本 2 个。扩到韩国/台湾/香港/新加坡会**混淆 IP 地理品牌**（收件端看到不再是纯日本邮件基础设施），仅在配额不够且你不在意 IP 国籍时开启。
                </p>
              </div>

              <div>
                <label className="block text-xs text-slate-400 mb-1.5">IP 前缀过滤</label>
                <div className="space-y-1.5">
                  {([
                    { v: 'blacklist', t: '排除指定前缀（默认排 34.）', hint: '命中黑名单就释放重申，直到筛够目标数量' },
                    { v: 'all', t: '所有前缀，仅靠 DNSBL', hint: '不看前缀，DNSBL 干净就留' },
                  ] as { v: IPPrefixMode; t: string; hint: string }[]).map(o => (
                    <label key={o.v} className="flex items-start gap-2 px-2 py-1.5 bg-slate-900 border border-slate-700 rounded-md text-sm cursor-pointer hover:border-indigo-500">
                      <input type="radio" name="ipPrefix" className="accent-indigo-500 mt-0.5"
                             checked={ipPrefixMode === o.v}
                             onChange={() => setIpPrefixMode(o.v)} />
                      <div>
                        <div className="text-slate-300">{o.t}</div>
                        <div className="text-[10px] text-slate-500">{o.hint}</div>
                      </div>
                    </label>
                  ))}
                </div>
                {ipPrefixMode === 'blacklist' && (
                  <div className="mt-2">
                    <label className="block text-xs text-slate-400 mb-1">要排除的前缀（多个用空格/逗号分隔，不带 . 会自动补）</label>
                    <input type="text"
                           className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none"
                           value={excludePrefixText}
                           onChange={e => setExcludePrefixText(e.target.value)}
                           placeholder="34. 35." />
                    <p className="text-[10px] text-slate-500 mt-0.5">例如 <span className="font-mono">34.</span> 会排除所有 34.x.x.x 开头的 IP</p>
                  </div>
                )}
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
                    <th className="text-left px-2 py-1.5 font-medium">区域</th>
                    <th className="text-left px-2 py-1.5 font-medium">DNSBL</th>
                    <th className="text-left px-2 py-1.5 font-medium">创建</th>
                    <th className="text-right px-2 py-1.5 font-medium">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {cleanIPs.length === 0 && <tr><td colSpan={6} className="text-center py-6 text-slate-500">暂无 clean IP，请点击刷新</td></tr>}
                  {cleanIPs.map(i => (
                    <tr key={i.id} className={`border-t border-slate-700/40 ${selectedCleanIPs.has(i.id) ? 'bg-indigo-500/5' : ''}`}>
                      <td className="px-2 py-1.5"><input type="checkbox"
                             checked={selectedCleanIPs.has(i.id)} onChange={() => toggleCleanIP(i.id)} /></td>
                      <td className="px-2 py-1.5 text-slate-200 font-mono">{i.ip}</td>
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
                  ))}
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
                  {tpls.map(t => <option key={t.id} value={t.id}>{t.name}{t.is_preset ? ' [预设]' : ''}</option>)}
                </select>
              </div>

              <div className="text-xs text-slate-400 bg-slate-900/50 border border-slate-700/50 rounded-md px-3 py-2 space-y-1">
                <div>🔑 SSH 登录已改用密钥认证（v0.1.7+）。软件自动生成密钥对并注入到 VPS，不需要设置 root 密码。</div>
                <div>🔥 首次开机会自动在 GCP 项目里建防火墙规则 <span className="font-mono text-indigo-300">mailnode-mail-ports</span>（入站 22/25/80/443/465/587/2525）+ <span className="font-mono text-indigo-300">mailnode-smtp-out</span>（出站 25/465/587）。VPS 打 <span className="font-mono">mail-node</span> tag 自动命中，已存在则复用。</div>
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
            等 A 记录 DNS 生效 1-5 分钟后再点。仅 <span className="font-mono">ptr_status</span> 未设置的 VPS 需要处理。
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
                        <td className="px-2 py-1.5"><span className={statusBadge(v.ptr_status || '-')}>{v.ptr_status || '-'}</span></td>
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
                const list = Array.from(types).map(t => t === 'mailcow' ? '📥 mailcow（收发一体）' : '🚀 KumoMTA（纯发信）').join(' / ')
                return <div className="mt-1 text-slate-500">将按模板类型部署：<span className="text-indigo-300">{list}</span></div>
              })()}
            </div>

            <div>
              <textarea
                className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-xs font-mono focus:border-indigo-500 outline-none"
                rows={8}
                value={domainIPText}
                onChange={e => setDomainIPText(e.target.value)}
                placeholder={'example1.com\nexample2.com\nexample3.com\n# 以 # 开头的行会被忽略'}
              />
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

            <label className="flex items-start gap-2 p-3 bg-slate-900 border border-slate-700 rounded-md cursor-pointer">
              <input type="checkbox" className="accent-indigo-500 mt-0.5"
                     checked={hideClientIP} onChange={e => setHideClientIP(e.target.checked)} />
              <div>
                <div className="text-sm text-slate-200">🛡️ 屏蔽发件端真实 IP（推荐勾选）</div>
                <div className="text-xs text-slate-500">移除 Received 链头部真实 IP，写入伪造链</div>
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
