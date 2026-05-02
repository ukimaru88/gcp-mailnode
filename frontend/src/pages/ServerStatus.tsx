import { useEffect, useMemo, useState } from 'react'
import { AlertTriangle, CheckCircle2, Clock3, Play, RefreshCw, Server, XCircle } from 'lucide-react'
import {
  ListVPS,
  // @ts-ignore - bindings 会在 wails build 时重新生成
  GetServerStatus,
} from '../../wailsjs/go/main/App'
import { main } from '../../wailsjs/go/models'
import { useToast } from '../components/Toast'

const n = (v: any) => Number.isFinite(Number(v)) ? Number(v) : 0
const dt = (v: any) => { try { return new Date(v).toLocaleString('zh-CN') } catch { return '-' } }

export default function ServerStatus() {
  const { toast } = useToast()
  const [vpsList, setVpsList] = useState<main.VPSInstanceDTO[]>([])
  const [selectedID, setSelectedID] = useState('')
  const [logFiles, setLogFiles] = useState(8)
  const [loading, setLoading] = useState(false)
  const [status, setStatus] = useState<any>(null)

  const usable = useMemo(() =>
    vpsList.filter(v => v.deploy_type === 'kumomta' && v.status !== 'deleted' && !!v.ip),
    [vpsList]
  )

  const refreshList = async () => {
    try {
      const list = await ListVPS()
      setVpsList(list || [])
      const first = (list || []).find(v => v.deploy_type === 'kumomta' && v.status !== 'deleted' && !!v.ip)
      if (!selectedID && first) setSelectedID(first.id)
    } catch (e: any) {
      toast('error', '加载 VPS 失败: ' + (e?.message || e))
    }
  }

  const connect = async (id = selectedID) => {
    if (!id) { toast('warning', '请选择 VPS'); return }
    setLoading(true)
    try {
      const r = await GetServerStatus(id, logFiles)
      setStatus(r)
      toast(r?.service_active ? 'success' : 'warning', r?.service_active ? '服务器状态已刷新' : '已连接，但服务可能异常')
    } catch (e: any) {
      toast('error', '连接失败: ' + (e?.message || e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { refreshList() }, [])

  return (
    <div className="p-6 h-full overflow-auto space-y-5">
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-2">
          <Server size={20} className="text-indigo-400" />
          <div>
            <h1 className="text-xl font-bold text-slate-100">连接服务器</h1>
            <div className="text-xs text-slate-500 mt-1">SSH 读取 KumoMTA 当前状态、队列和最近日志统计</div>
          </div>
        </div>
        <div className="inline-flex items-center gap-2">
          <select value={selectedID} onChange={e => setSelectedID(e.target.value)}
                  className="bg-[#1a1d27] border border-slate-700 rounded-md px-3 py-2 text-sm text-slate-200 min-w-80">
            {usable.length === 0 && <option value="">没有可连接的 KumoMTA VPS</option>}
            {usable.map(v => <option key={v.id} value={v.id}>{v.name} · {v.ip} · {v.fqdn || v.domain || '-'}</option>)}
          </select>
          <select value={logFiles} onChange={e => setLogFiles(Number(e.target.value))}
                  className="bg-[#1a1d27] border border-slate-700 rounded-md px-3 py-2 text-sm text-slate-200">
            <option value={4}>最近 4 个日志文件</option>
            <option value={8}>最近 8 个日志文件</option>
            <option value={16}>最近 16 个日志文件</option>
            <option value={30}>最近 30 个日志文件</option>
          </select>
          <button onClick={refreshList}
                  className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-2 text-sm inline-flex items-center gap-1.5">
            <RefreshCw size={14} /> 刷新列表
          </button>
          <button onClick={() => connect()} disabled={loading || !selectedID}
                  className="bg-indigo-600 hover:bg-indigo-500 disabled:opacity-40 text-white rounded-md px-3 py-2 text-sm inline-flex items-center gap-1.5">
            <Play size={14} /> {loading ? '连接中...' : '连接/刷新'}
          </button>
        </div>
      </div>

      {status ? (
        <div className="space-y-5">
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-5 gap-3">
            <Metric label="服务状态" value={status.service_active ? 'active' : (status.service_state || '-')}
                    sub={`${status.service_enabled || '-'} · ${status.uptime || '-'}`}
                    good={status.service_active} />
            <Metric label="发送成功" value={status.delivered || 0}
                    sub={`提交 ${status.submitted || 0} · 扫描 ${status.log_files_scanned || 0} 个日志`} />
            <Metric label="退信" value={status.bounced || 0}
                    sub={`临时失败 ${status.deferred || 0}`} bad={n(status.bounced) > 0} />
            <Metric label="等待队列" value={status.queue_files || 0}
                    sub={`${status.queue_bytes_human || '0 B'} · meta ${status.meta_files || 0} / data ${status.data_files || 0}`}
                    warn={n(status.queue_files) > 0} />
            <Metric label="域名数量" value={status.unique_domains || 0}
                    sub={`负载 ${status.load_average || '-'} · ${status.spool_disk_used || '-'}`} />
          </div>

          <div className="grid grid-cols-1 xl:grid-cols-[1fr_420px] gap-5">
            <div className="space-y-5">
              <Panel title="服务器详情">
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3 text-sm">
                  <Info label="名称" value={status.name} />
                  <Info label="IP / FQDN" value={`${status.ip} · ${status.fqdn || '-'}`} />
                  <Info label="Zone" value={status.zone || '-'} />
                  <Info label="检查时间" value={dt(status.checked_at)} />
                  <Info label="根磁盘" value={status.root_disk_used || '-'} />
                  <Info label="队列磁盘" value={status.spool_disk_used || '-'} />
                  <Info label="最后日志" value={status.last_log_file || '-'} />
                  <Info label="监听端口" value={(status.ports || []).length ? `${(status.ports || []).length} 条` : '未发现 25/465/587'} />
                </div>
                {(status.ports || []).length > 0 && (
                  <pre className="mt-3 bg-slate-900/60 rounded-md p-3 text-xs text-slate-400 overflow-auto max-h-32">{(status.ports || []).join('\n')}</pre>
                )}
              </Panel>

              <Panel title="退信原因">
                {(status.bounce_reasons || []).length === 0 ? (
                  <Empty text="最近日志里没有退信" />
                ) : (
                  <div className="space-y-3">
                    {(status.bounce_reasons || []).map((r: any) => (
                      <div key={r.reason} className="border border-slate-700/50 rounded-md p-3 bg-slate-900/30">
                        <div className="flex items-center justify-between gap-3">
                          <div className="font-medium text-slate-200">{r.reason}</div>
                          <div className="font-mono text-red-300">{r.count}</div>
                        </div>
                        {r.sample && <div className="text-xs text-slate-500 mt-1 break-all">{r.sample}</div>}
                        {(r.top_domains || []).length > 0 && (
                          <div className="mt-2 flex flex-wrap gap-1.5">
                            {r.top_domains.map((d: any) => <Chip key={d.name} text={`${d.name} ${d.count}`} />)}
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </Panel>

              <Panel title="Top 收件域名">
                {(status.top_domains || []).length === 0 ? (
                  <Empty text="最近日志里没有域名统计" />
                ) : (
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                    {(status.top_domains || []).map((d: any) => (
                      <div key={d.name} className="flex items-center justify-between bg-slate-900/40 rounded-md px-3 py-2 text-sm">
                        <span className="text-slate-300">{d.name}</span>
                        <span className="font-mono text-slate-400">{d.count}</span>
                      </div>
                    ))}
                  </div>
                )}
              </Panel>
            </div>

            <div className="space-y-5">
              <Panel title="建议">
                <div className="space-y-2">
                  {(status.recommendations || []).map((x: string, idx: number) => (
                    <div key={idx} className="flex items-start gap-2 text-sm text-slate-300">
                      <AlertTriangle size={15} className="text-amber-300 mt-0.5 shrink-0" />
                      <span>{x}</span>
                    </div>
                  ))}
                </div>
              </Panel>

              <Panel title="最近错误">
                {(status.recent_errors || []).length === 0 ? (
                  <Empty text="最近 1 小时 journal 没有 warning/error" />
                ) : (
                  <div className="space-y-2 max-h-96 overflow-auto">
                    {(status.recent_errors || []).map((x: string, idx: number) => (
                      <div key={idx} className="text-xs text-amber-200 bg-amber-500/10 border border-amber-500/20 rounded-md p-2 break-all">{x}</div>
                    ))}
                  </div>
                )}
              </Panel>
            </div>
          </div>
        </div>
      ) : (
        <div className="bg-[#1a1d27] border border-slate-700/50 rounded-lg p-10 text-center">
          <Clock3 size={34} className="mx-auto text-slate-500 mb-3" />
          <div className="text-slate-300 font-medium">选择一台 VPS 后点击连接</div>
          <div className="text-sm text-slate-500 mt-1">会通过 SSH 读取服务、队列和最近日志，不会修改服务器配置。</div>
        </div>
      )}
    </div>
  )
}

function Metric({ label, value, sub, good, bad, warn }: any) {
  const Icon = good ? CheckCircle2 : bad ? XCircle : warn ? AlertTriangle : Clock3
  const color = good ? 'text-emerald-300' : bad ? 'text-red-300' : warn ? 'text-amber-300' : 'text-indigo-300'
  return (
    <div className="bg-[#1a1d27] border border-slate-700/50 rounded-lg p-4">
      <div className="flex items-center gap-2 text-xs text-slate-400 mb-2">
        <Icon size={15} className={color} /> {label}
      </div>
      <div className="text-2xl font-semibold text-slate-100">{value}</div>
      <div className="text-xs text-slate-500 mt-1 truncate">{sub}</div>
    </div>
  )
}

function Panel({ title, children }: any) {
  return (
    <div className="bg-[#1a1d27] border border-slate-700/50 rounded-lg">
      <div className="px-4 py-3 border-b border-slate-700/50">
        <h2 className="font-semibold text-slate-100">{title}</h2>
      </div>
      <div className="p-4">{children}</div>
    </div>
  )
}

function Info({ label, value }: any) {
  return (
    <div className="bg-slate-900/30 rounded-md px-3 py-2">
      <div className="text-xs text-slate-500">{label}</div>
      <div className="text-slate-300 mt-0.5 break-all">{value}</div>
    </div>
  )
}

function Chip({ text }: { text: string }) {
  return <span className="px-2 py-0.5 rounded bg-slate-800 text-slate-300 text-xs">{text}</span>
}

function Empty({ text }: { text: string }) {
  return <div className="text-sm text-slate-500 py-4 text-center">{text}</div>
}
