import { useEffect, useState } from 'react'
import { Inbox, FolderOpen, RefreshCw, Play } from 'lucide-react'
import {
  ListVPS,
  // @ts-ignore - bindings 会在 wails build 时重新生成
  ExtractFromVPS,
  // @ts-ignore
  GetExtractOutputDir,
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

export default function Extract() {
  const { toast } = useToast()
  const [vpsList, setVpsList] = useState<main.VPSInstanceDTO[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [running, setRunning] = useState(false)
  const [summary, setSummary] = useState<ExtractSummary | null>(null)
  const [outputDir, setOutputDir] = useState('')

  const refresh = async () => {
    try {
      const v = await ListVPS()
      // 只列出 KumoMTA 部署成功的（可提取的）
      setVpsList((v || []).filter(x => x.deploy_type === 'kumomta' && x.deploy_status === 'success' && x.status !== 'deleted' && x.ip))
    } catch (e: any) { toast('error', '加载 VPS 失败: ' + (e?.message || e)) }
  }
  useEffect(() => {
    refresh()
    GetExtractOutputDir().then((d: string) => setOutputDir(d || '')).catch(() => {})
  }, [])

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
      const r = await ExtractFromVPS(Array.from(selected)) as ExtractSummary
      setSummary(r)
      toast('success', `提取完成：跨 VPS 唯一邮箱 ${r.total_emails}`)
    } catch (e: any) {
      toast('error', '提取失败: ' + (e?.message || e))
    } finally {
      setRunning(false)
    }
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
          <span className="text-xs text-slate-500 ml-2">从 KumoMTA VPS 拉取日志，按域名分类输出 txt</span>
        </div>
        <div className="inline-flex gap-2 items-center">
          <button onClick={refresh}
                  className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
            <RefreshCw size={14} /> 刷新
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
                暂无可提取 VPS（需 KumoMTA 类型且已部署成功）
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
