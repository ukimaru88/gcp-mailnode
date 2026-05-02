import { useEffect, useState } from 'react'
import { Inbox, FolderOpen, RefreshCw, Play, Server, Clock, Trash2 } from 'lucide-react'
import {
  ListVPS,
  // @ts-ignore - bindings 会在 wails build 时重新生成
  ExtractFromVPS,
  // @ts-ignore
  ExtractFromVPSWithDelete,
  // @ts-ignore
  GetExtractOutputDir,
  // @ts-ignore
  GetExtractSchedule,
  // @ts-ignore
  SetExtractSchedule,
} from '../../wailsjs/go/main/App'
import { BrowserOpenURL } from '../../wailsjs/runtime/runtime'
import { main } from '../../wailsjs/go/models'
import { useToast } from '../components/Toast'

interface ExtractResult {
  vps_id: string
  name: string
  ip: string
  lines: number
  parsed: number
  emails: number
  error?: string
}

interface WriteResult {
  total_emails: number
  new_emails: number
  duplicate_skip: number
  files_created: string[]
}

interface ExtractSummary {
  batch_id: string
  results: ExtractResult[]
  output_dir: string
  total_emails: number
  write_result: WriteResult
}

interface ScheduleCfg {
  enabled: boolean
  interval_min: number
  delete_after: boolean
  last_run_at?: string
  last_run_status?: string
  last_run_msg?: string
  next_run_at?: string
}

export default function Extract() {
  const { toast } = useToast()
  const [vpsList, setVpsList] = useState<main.VPSInstanceDTO[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [running, setRunning] = useState(false)
  const [summary, setSummary] = useState<ExtractSummary | null>(null)
  const [outputDir, setOutputDir] = useState('')

  // v0.1.77：手动提取删服务器日志开关 + 自动调度配置
  const [deleteAfter, setDeleteAfter] = useState(true)
  const [schedule, setSchedule] = useState<ScheduleCfg>({ enabled: false, interval_min: 15, delete_after: true })
  const [scheduleSaving, setScheduleSaving] = useState(false)

  const isExtractableVPS = (v: main.VPSInstanceDTO) =>
    v.deploy_type === 'kumomta' &&
    v.status !== 'deleted' &&
    !!v.ip &&
    ['success', 'mta_ready', 'ptr_ready'].includes(v.deploy_status || '')

  const importFromResources = async (selectAll = false, notify = false) => {
    try {
      const v = await ListVPS()
      const usable = (v || []).filter(isExtractableVPS)
      setVpsList(usable)
      if (selectAll) setSelected(new Set(usable.map(x => x.id)))
      else setSelected(prev => new Set([...Array.from(prev)].filter(id => usable.some(x => x.id === id))))
      if (notify) toast('success', `已从资源清单导入 ${usable.length} 台可提取 VPS`)
    } catch (e: any) { toast('error', '加载 VPS 失败: ' + (e?.message || e)) }
  }
  const refresh = () => importFromResources(false, false)
  const refreshSchedule = async () => {
    try {
      const s = await GetExtractSchedule() as ScheduleCfg
      if (s) setSchedule({
        enabled: !!s.enabled,
        interval_min: s.interval_min || 15,
        delete_after: s.delete_after !== false,
        last_run_at: s.last_run_at,
        last_run_status: s.last_run_status,
        last_run_msg: s.last_run_msg,
        next_run_at: s.next_run_at,
      })
    } catch {}
  }
  useEffect(() => {
    refresh()
    GetExtractOutputDir().then((d: string) => setOutputDir(d || '')).catch(() => {})
    refreshSchedule()
    // 每 30 秒刷新一次调度状态（看到自动提取的进度）
    const t = setInterval(refreshSchedule, 30000)
    return () => clearInterval(t)
  }, [])

  const saveSchedule = async (next: ScheduleCfg) => {
    setScheduleSaving(true)
    try {
      await SetExtractSchedule(next as any)
      setSchedule(next)
      toast('success', `自动提取已${next.enabled ? '启用' : '停用'}（间隔 ${next.interval_min} 分钟）`)
    } catch (e: any) {
      toast('error', '保存调度配置失败: ' + (e?.message || e))
    } finally {
      setScheduleSaving(false)
    }
  }

  const toggle = (id: string) => {
    setSelected(prev => {
      const n = new Set(prev)
      n.has(id) ? n.delete(id) : n.add(id)
      return n
    })
  }
  const toggleAll = () => {
    if (selected.size === vpsList.length) setSelected(new Set())
    else setSelected(new Set(vpsList.map(v => v.id)))
  }

  const run = async () => {
    if (selected.size === 0) { toast('warning', '请先勾选 VPS'); return }
    setRunning(true)
    setSummary(null)
    try {
      const r = await ExtractFromVPSWithDelete(Array.from(selected), deleteAfter) as ExtractSummary
      setSummary(r)
      const tip = deleteAfter ? '（已删除服务器日志）' : ''
      toast('success', `提取完成：跨 VPS 唯一邮箱 ${r.total_emails}${tip}`)
    } catch (e: any) {
      toast('error', '提取失败: ' + (e?.message || e))
    } finally {
      setRunning(false)
    }
  }

  const fmtTime = (iso?: string) => {
    if (!iso) return '-'
    try { return new Date(iso).toLocaleString('zh-CN', { hour12: false }) } catch { return iso }
  }

  const openFolder = async () => {
    try {
      const dir = outputDir || (await GetExtractOutputDir() as string)
      if (!dir) { toast('error', '输出目录未就绪'); return }
      // Windows file:/// 路径需要把反斜杠保留，但浏览器/Wails 通常容忍
      BrowserOpenURL('file:///' + dir.replace(/\\/g, '/'))
    } catch (e: any) {
      toast('error', '打开失败: ' + (e?.message || e))
    }
  }

  return (
    <div className="p-6 h-full overflow-auto">
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-2">
          <Inbox size={20} className="text-indigo-400" />
          <h1 className="text-xl font-bold text-slate-100">邮箱提取</h1>
          <span className="text-xs text-slate-500 ml-2">仅导出"成功投递（Delivery 250 OK）"的收件人，Bounce/Deferred 不输出</span>
        </div>
        <div className="inline-flex gap-2 items-center">
          <label className="inline-flex items-center gap-1.5 text-xs text-slate-300 cursor-pointer select-none px-2 py-1.5 bg-slate-800/60 rounded-md border border-slate-700/40"
                 title="提取成功后调用 SSH 删除 ≤ 已读 cursor 的所有日志文件（包括成功+失败），保留正在写入的最新文件">
            <input type="checkbox" className="accent-rose-500" checked={deleteAfter} onChange={e => setDeleteAfter(e.target.checked)} />
            <Trash2 size={12} className={deleteAfter ? 'text-rose-400' : 'text-slate-500'} />
            提取后删服务器日志
          </label>
          <button onClick={refresh}
                  className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
            <RefreshCw size={14} /> 刷新
          </button>
          <button onClick={() => importFromResources(true, true)}
                  className="bg-emerald-600 hover:bg-emerald-500 text-white rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
            <Server size={14} /> 从资源清单导入
          </button>
          <button onClick={openFolder}
                  title={outputDir || '输出目录'}
                  className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
            <FolderOpen size={14} /> 打开输出目录
          </button>
          <button onClick={run} disabled={running || selected.size === 0}
                  className="bg-indigo-600 hover:bg-indigo-500 disabled:opacity-40 disabled:cursor-not-allowed text-white rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
            <Play size={14} /> {running ? '提取中...' : `开始提取 (${selected.size})`}
          </button>
        </div>
      </div>

      {/* 输出路径提示 */}
      {outputDir && (
        <div className="mb-3 text-xs text-slate-500">
          输出目录：<code className="font-mono text-slate-400">{outputDir}</code>
        </div>
      )}

      {/* v0.1.77：自动提取调度面板 */}
      <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-3 mb-4">
        <div className="flex items-center gap-3 flex-wrap">
          <div className="flex items-center gap-1.5">
            <Clock size={14} className={schedule.enabled ? 'text-emerald-400' : 'text-slate-500'} />
            <span className="text-sm text-slate-200 font-medium">自动提取</span>
          </div>
          <label className="inline-flex items-center gap-1.5 text-xs text-slate-300 cursor-pointer select-none">
            <input type="checkbox" className="accent-emerald-500" disabled={scheduleSaving}
                   checked={schedule.enabled}
                   onChange={e => saveSchedule({ ...schedule, enabled: e.target.checked })} />
            <span>{schedule.enabled ? '已启用' : '已停用'}</span>
          </label>
          <div className="flex items-center gap-1.5 text-xs text-slate-300">
            <span>间隔</span>
            <input type="number" min={1} max={1440} disabled={scheduleSaving}
                   className="w-16 bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1 text-xs focus:border-indigo-500 outline-none"
                   value={schedule.interval_min}
                   onChange={e => setSchedule({ ...schedule, interval_min: Math.max(1, Math.min(1440, Number(e.target.value) || 15)) })}
                   onBlur={() => saveSchedule(schedule)} />
            <span>分钟</span>
          </div>
          <label className="inline-flex items-center gap-1.5 text-xs text-slate-300 cursor-pointer select-none">
            <input type="checkbox" className="accent-rose-500" disabled={scheduleSaving}
                   checked={schedule.delete_after}
                   onChange={e => saveSchedule({ ...schedule, delete_after: e.target.checked })} />
            <Trash2 size={12} className={schedule.delete_after ? 'text-rose-400' : 'text-slate-500'} />
            <span>同时删服务器日志</span>
          </label>
          <div className="flex-1" />
          <div className="text-[10px] text-slate-500 grid grid-cols-2 gap-x-3 gap-y-0.5 min-w-[260px]">
            <span>上次：{fmtTime(schedule.last_run_at)}</span>
            <span>下次：{fmtTime(schedule.next_run_at)}</span>
            <span className="col-span-2 truncate" title={schedule.last_run_msg}>
              {schedule.last_run_status === 'ok' ? '✅' : schedule.last_run_status === 'error' ? '❌' : '·'} {schedule.last_run_msg || '未运行'}
            </span>
          </div>
        </div>
        <div className="text-[10px] text-slate-500 mt-1.5">
          启用后每 N 分钟自动从所有 KumoMTA VPS（success / mta_ready / ptr_ready）拉取日志、按域名分类追加到本地 txt。建议 ≥ 5 分钟避免频繁 SSH。
        </div>
      </div>

      {/* VPS 表 */}
      <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 overflow-hidden mb-4">
        <table className="w-full text-sm">
          <thead className="bg-slate-900/50 text-slate-400">
            <tr>
              <th className="text-left px-3 py-2 w-10">
                <input type="checkbox" className="accent-indigo-500"
                       checked={selected.size > 0 && selected.size === vpsList.length}
                       onChange={toggleAll} />
              </th>
              <th className="text-left px-3 py-2 font-medium">名称</th>
              <th className="text-left px-3 py-2 font-medium">IP</th>
              <th className="text-left px-3 py-2 font-medium">域名</th>
              <th className="text-left px-3 py-2 font-medium">Region</th>
            </tr>
          </thead>
          <tbody>
            {vpsList.length === 0 && (
              <tr><td colSpan={5} className="text-center px-3 py-6 text-slate-500">
                暂无可提取 VPS（需 KumoMTA 类型，且状态为 success / mta_ready / ptr_ready）
              </td></tr>
            )}
            {vpsList.map(v => (
              <tr key={v.id} className="border-t border-slate-700/40 hover:bg-slate-800/50 cursor-pointer"
                  onClick={() => toggle(v.id)}>
                <td className="px-3 py-2">
                  <input type="checkbox" className="accent-indigo-500"
                         checked={selected.has(v.id)} onChange={() => toggle(v.id)}
                         onClick={e => e.stopPropagation()} />
                </td>
                <td className="px-3 py-2 text-slate-200">{v.name}</td>
                <td className="px-3 py-2 text-slate-300 font-mono text-xs">{v.ip}</td>
                <td className="px-3 py-2 text-slate-400 text-xs">{v.fqdn || v.domain || '-'}</td>
                <td className="px-3 py-2 text-slate-500 text-xs">{v.region}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* 进度/结果汇总 */}
      {summary && (
        <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4 space-y-3">
          <div className="flex items-center justify-between">
            <h2 className="font-semibold text-slate-100">提取结果</h2>
            <span className="text-xs text-slate-500 font-mono">batch={summary.batch_id}</span>
          </div>

          <div className="grid grid-cols-3 gap-3 text-center">
            <div className="bg-slate-900/40 rounded-md py-3">
              <div className="text-2xl font-bold text-indigo-400">{summary.total_emails}</div>
              <div className="text-xs text-slate-500 mt-1">跨 VPS 唯一邮箱</div>
            </div>
            <div className="bg-slate-900/40 rounded-md py-3">
              <div className="text-2xl font-bold text-emerald-400">{summary.write_result?.new_emails ?? 0}</div>
              <div className="text-xs text-slate-500 mt-1">新增（去重后）</div>
            </div>
            <div className="bg-slate-900/40 rounded-md py-3">
              <div className="text-2xl font-bold text-slate-400">{summary.write_result?.duplicate_skip ?? 0}</div>
              <div className="text-xs text-slate-500 mt-1">已存在跳过</div>
            </div>
          </div>

          {/* 每台 VPS 明细 */}
          <div className="overflow-x-auto rounded-md border border-slate-700/50">
            <table className="w-full text-sm">
              <thead className="bg-slate-900/50 text-slate-400">
                <tr>
                  <th className="text-left px-3 py-2 font-medium">VPS</th>
                  <th className="text-right px-3 py-2 font-medium">日志行</th>
                  <th className="text-right px-3 py-2 font-medium">解析事件</th>
                  <th className="text-right px-3 py-2 font-medium">本机邮箱</th>
                  <th className="text-left px-3 py-2 font-medium">备注</th>
                </tr>
              </thead>
              <tbody>
                {summary.results.map(r => (
                  <tr key={r.vps_id} className="border-t border-slate-700/40">
                    <td className="px-3 py-2 text-slate-200">{r.name}</td>
                    <td className="px-3 py-2 text-right text-slate-400 font-mono">{r.lines}</td>
                    <td className="px-3 py-2 text-right text-slate-400 font-mono">{r.parsed}</td>
                    <td className="px-3 py-2 text-right text-emerald-400 font-mono">{r.emails}</td>
                    <td className="px-3 py-2 text-xs">
                      {r.error
                        ? <span className="text-red-400">{r.error}</span>
                        : <span className="text-slate-500">{r.ip}</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {/* 输出文件列表 */}
          {summary.write_result?.files_created && summary.write_result.files_created.length > 0 && (
            <div>
              <div className="text-xs text-slate-500 mb-1">生成 / 更新文件：</div>
              <div className="bg-slate-900/40 rounded-md p-2 max-h-32 overflow-y-auto">
                {summary.write_result.files_created.map((f, i) => (
                  <div key={i} className="text-xs text-slate-400 font-mono truncate">{f}</div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
