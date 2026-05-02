import { useEffect, useMemo, useState } from 'react'
import { Activity, AlertTriangle, DollarSign, Network, RefreshCw, Server, SlidersHorizontal } from 'lucide-react'
import {
  ListGCPCredentials,
  // @ts-ignore - bindings 会在 wails build 时重新生成
  GetGCPMonitorReport,
} from '../../wailsjs/go/main/App'
import { main } from '../../wailsjs/go/models'
import { useToast } from '../components/Toast'

type Pricing = {
  currency: string
  egress_per_gb: number
  vps_per_hour: number
  static_ip_per_hour: number
  use_last_hour_projection: boolean
}

const defaultPricing: Pricing = {
  currency: 'USD',
  egress_per_gb: 0.12,
  vps_per_hour: 0.35,
  static_ip_per_hour: 0.005,
  use_last_hour_projection: true,
}

const loadPricing = (): Pricing => {
  try {
    const raw = localStorage.getItem('gcp-monitor-pricing')
    if (!raw) return defaultPricing
    return { ...defaultPricing, ...JSON.parse(raw) }
  } catch {
    return defaultPricing
  }
}

const num = (v: any) => Number.isFinite(Number(v)) ? Number(v) : 0
const money = (v: any, c = 'USD') => `${c} ${num(v).toFixed(2)}`
const gb = (v: any) => {
  const n = num(v)
  if (n < 1) return `${(n * 1024).toFixed(1)} MB`
  return `${n.toFixed(2)} GB`
}
const dt = (v: any) => {
  try { return new Date(v).toLocaleString('zh-CN') } catch { return '-' }
}

export default function GCPMonitor() {
  const { toast } = useToast()
  const [creds, setCreds] = useState<main.GCPCredentialDTO[]>([])
  const [credID, setCredID] = useState('')
  const [hours, setHours] = useState(24)
  const [pricing, setPricing] = useState<Pricing>(loadPricing)
  const [loading, setLoading] = useState(false)
  const [report, setReport] = useState<any>(null)

  useEffect(() => {
    localStorage.setItem('gcp-monitor-pricing', JSON.stringify(pricing))
  }, [pricing])

  const refreshCreds = async () => {
    try {
      const list = await ListGCPCredentials()
      const enabled = (list || []).filter(c => c.enabled)
      setCreds(enabled)
      if (!credID && enabled[0]) setCredID(enabled[0].id)
    } catch (e: any) {
      toast('error', '加载 GCP 凭证失败: ' + (e?.message || e))
    }
  }

  const refresh = async (id = credID) => {
    if (!id) return
    setLoading(true)
    try {
      const r = await GetGCPMonitorReport(id, hours, pricing as any)
      setReport(r)
      if (r?.metric_error) toast('warning', '资源统计完成，但流量指标读取失败')
      else toast('success', 'GCP 监控已刷新')
    } catch (e: any) {
      toast('error', '刷新失败: ' + (e?.message || e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { refreshCreds() }, [])
  useEffect(() => {
    if (credID) refresh(credID)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [credID])

  const currency = report?.pricing?.currency || pricing.currency || 'USD'
  const hourly = report?.hourly || []
  const instances = report?.instances || []
  const maxHourly = useMemo(() => Math.max(0.001, ...hourly.map((h: any) => num(h.sent_gb))), [hourly])

  const setPrice = (key: keyof Pricing, value: any) => {
    setPricing(p => ({ ...p, [key]: key === 'currency' ? value : num(value) }))
  }

  return (
    <div className="p-6 h-full overflow-auto space-y-5">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-bold text-slate-100">GCP 监控</h1>
          <div className="text-xs text-slate-500 mt-1">
            流量来自 Cloud Monitoring；费用按当前页面单价估算。
          </div>
        </div>
        <div className="inline-flex items-center gap-2">
          <select value={credID} onChange={e => setCredID(e.target.value)}
                  className="bg-[#1a1d27] border border-slate-700 rounded-md px-3 py-2 text-sm text-slate-200 min-w-72">
            {creds.length === 0 && <option value="">没有启用的 GCP 凭证</option>}
            {creds.map(c => <option key={c.id} value={c.id}>{c.name} · {c.project_id}</option>)}
          </select>
          <select value={hours} onChange={e => setHours(Number(e.target.value))}
                  className="bg-[#1a1d27] border border-slate-700 rounded-md px-3 py-2 text-sm text-slate-200">
            <option value={24}>近 24 小时</option>
            <option value={48}>近 48 小时</option>
            <option value={72}>近 72 小时</option>
            <option value={168}>近 7 天</option>
          </select>
          <button onClick={() => refresh()} disabled={!credID || loading}
                  className="bg-indigo-600 hover:bg-indigo-500 disabled:opacity-40 text-white rounded-md px-3 py-2 text-sm inline-flex items-center gap-1.5">
            <RefreshCw size={14} className={loading ? 'animate-spin' : ''} /> 刷新
          </button>
        </div>
      </div>

      <div className="grid grid-cols-1 2xl:grid-cols-[1fr_360px] gap-5">
        <div className="space-y-5">
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-3">
            <Metric icon={DollarSign} label="预计 24h 总费用" value={money(report?.projected_cost_24h, currency)}
                    sub={`已发生口径 ${money(report?.estimated_cost_24h, currency)}`} />
            <Metric icon={Network} label={`${hours}h 出站流量`} value={gb(report?.sent_gb)}
                    sub={`入站 ${gb(report?.received_gb)} · 合计 ${gb(report?.total_gb)}`} />
            <Metric icon={Activity} label="按最近速率推算" value={gb(report?.projected_sent_gb_24h)}
                    sub={`最近 1h 出站 ${gb(report?.last_hour_sent_gb)}`} />
            <Metric icon={Server} label="资源规模" value={`${report?.running_vps || 0}/${report?.total_vps || 0} 台`}
                    sub={`静态 IP ${report?.total_static_ips || 0} 个 · in_use ${report?.in_use_static_ips || 0}`} />
          </div>

          <div className="bg-[#1a1d27] border border-slate-700/50 rounded-lg">
            <div className="px-4 py-3 border-b border-slate-700/50 flex items-center justify-between">
              <h2 className="font-semibold text-slate-100">费用拆分</h2>
              <span className="text-xs text-slate-500">刷新时间：{report?.generated_at ? dt(report.generated_at) : '-'}</span>
            </div>
            <div className="grid grid-cols-1 md:grid-cols-4 divide-y md:divide-y-0 md:divide-x divide-slate-700/50">
              <CostBlock label="VPS 24h" value={money(report?.vps_cost_24h, currency)} sub={`${report?.running_vps || 0} 台 × ${money(pricing.vps_per_hour, currency)}/h`} />
              <CostBlock label="静态 IP 24h" value={money(report?.static_ip_cost_24h, currency)} sub={`${report?.total_static_ips || 0} 个 × ${money(pricing.static_ip_per_hour, currency)}/h`} />
              <CostBlock label={`${hours}h 出站费用`} value={money(report?.traffic_cost_24h, currency)} sub={`${gb(report?.sent_gb)} × ${money(pricing.egress_per_gb, currency)}/GB`} />
              <CostBlock label="推算 24h 出站费用" value={money(report?.projected_traffic_cost_24h, currency)} sub={`${gb(report?.projected_sent_gb_24h)} × ${money(pricing.egress_per_gb, currency)}/GB`} />
            </div>
          </div>

          {(report?.warnings?.length > 0 || report?.metric_error) && (
            <div className="bg-amber-500/10 border border-amber-500/30 rounded-lg p-3 text-sm text-amber-200 space-y-1">
              <div className="font-medium inline-flex items-center gap-1.5"><AlertTriangle size={15} /> 注意</div>
              {report?.warnings?.map((w: string, idx: number) => <div key={idx}>{w}</div>)}
              {report?.metric_error && <div className="font-mono text-xs text-amber-100/80 break-all">{report.metric_error}</div>}
            </div>
          )}

          <div className="bg-[#1a1d27] border border-slate-700/50 rounded-lg">
            <div className="px-4 py-3 border-b border-slate-700/50">
              <h2 className="font-semibold text-slate-100">每台服务器流量</h2>
            </div>
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="bg-slate-900/50 text-slate-400 text-xs">
                  <tr>
                    <th className="text-left px-4 py-2 font-medium">服务器</th>
                    <th className="text-left px-4 py-2 font-medium">IP / FQDN</th>
                    <th className="text-right px-4 py-2 font-medium">出站</th>
                    <th className="text-right px-4 py-2 font-medium">入站</th>
                    <th className="text-right px-4 py-2 font-medium">最近 1h 出站</th>
                    <th className="text-right px-4 py-2 font-medium">出站费用</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-800">
                  {instances.length === 0 && (
                    <tr><td colSpan={6} className="px-4 py-8 text-center text-slate-500">没有 VPS 数据</td></tr>
                  )}
                  {instances.map((v: any) => (
                    <tr key={v.id} className="hover:bg-slate-800/30">
                      <td className="px-4 py-2">
                        <div className="text-slate-200 font-medium">{v.name}</div>
                        <div className="text-xs text-slate-500">{v.zone} · {v.machine_type} · {v.status}</div>
                      </td>
                      <td className="px-4 py-2">
                        <div className="font-mono text-xs text-slate-300">{v.ip || '-'}</div>
                        <div className="text-xs text-slate-500">{v.fqdn || '-'}</div>
                      </td>
                      <td className="px-4 py-2 text-right text-slate-200">{gb(v.sent_gb)}</td>
                      <td className="px-4 py-2 text-right text-slate-300">{gb(v.received_gb)}</td>
                      <td className="px-4 py-2 text-right text-slate-300">{gb(v.last_hour_sent_gb)}</td>
                      <td className="px-4 py-2 text-right text-slate-200">{money(v.traffic_cost_24h, currency)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>

          <div className="bg-[#1a1d27] border border-slate-700/50 rounded-lg">
            <div className="px-4 py-3 border-b border-slate-700/50">
              <h2 className="font-semibold text-slate-100">每小时出站流量</h2>
            </div>
            <div className="p-4 space-y-2">
              {hourly.length === 0 && <div className="py-8 text-center text-sm text-slate-500">暂无 Monitoring 流量点</div>}
              {hourly.slice(-24).map((h: any) => (
                <div key={h.end_time} className="grid grid-cols-[150px_1fr_120px_110px] items-center gap-3 text-xs">
                  <div className="text-slate-500">{dt(h.end_time)}</div>
                  <div className="h-2 rounded bg-slate-800 overflow-hidden">
                    <div className="h-full bg-indigo-500" style={{ width: `${Math.max(2, num(h.sent_gb) / maxHourly * 100)}%` }} />
                  </div>
                  <div className="text-right text-slate-300">{gb(h.sent_gb)}</div>
                  <div className="text-right text-slate-500">{money(h.traffic_cost, currency)}</div>
                </div>
              ))}
            </div>
          </div>
        </div>

        <div className="bg-[#1a1d27] border border-slate-700/50 rounded-lg h-fit">
          <div className="px-4 py-3 border-b border-slate-700/50 flex items-center gap-2">
            <SlidersHorizontal size={16} className="text-indigo-300" />
            <h2 className="font-semibold text-slate-100">估算单价</h2>
          </div>
          <div className="p-4 space-y-4">
            <PriceInput label="币种" value={pricing.currency} onChange={(v: string) => setPrice('currency', v)} />
            <PriceInput label="1GB 出站流量" value={pricing.egress_per_gb} onChange={(v: string) => setPrice('egress_per_gb', v)} suffix="/GB" />
            <PriceInput label="每台 VPS" value={pricing.vps_per_hour} onChange={(v: string) => setPrice('vps_per_hour', v)} suffix="/小时" />
            <PriceInput label="每个静态 IP" value={pricing.static_ip_per_hour} onChange={(v: string) => setPrice('static_ip_per_hour', v)} suffix="/小时" />
            <label className="flex items-start gap-2 text-sm text-slate-300 cursor-pointer select-none">
              <input type="checkbox" className="mt-1 accent-indigo-500"
                     checked={pricing.use_last_hour_projection}
                     onChange={e => setPricing(p => ({ ...p, use_last_hour_projection: e.target.checked }))} />
              <span>
                用最近 1 小时流量推算未来 24 小时
                <span className="block text-xs text-slate-500 mt-0.5">关闭后按所选时间范围的平均速率推算。</span>
              </span>
            </label>
          </div>
        </div>
      </div>
    </div>
  )
}

function Metric({ icon: Icon, label, value, sub }: any) {
  return (
    <div className="bg-[#1a1d27] border border-slate-700/50 rounded-lg p-4">
      <div className="flex items-center gap-2 text-slate-400 text-xs mb-2">
        <Icon size={15} className="text-indigo-300" /> {label}
      </div>
      <div className="text-2xl font-semibold text-slate-100">{value}</div>
      <div className="text-xs text-slate-500 mt-1">{sub}</div>
    </div>
  )
}

function CostBlock({ label, value, sub }: any) {
  return (
    <div className="p-4">
      <div className="text-xs text-slate-500 mb-1">{label}</div>
      <div className="text-lg font-semibold text-slate-100">{value}</div>
      <div className="text-xs text-slate-500 mt-1">{sub}</div>
    </div>
  )
}

function PriceInput({ label, value, onChange, suffix }: any) {
  return (
    <label className="block">
      <span className="text-xs text-slate-400">{label}</span>
      <div className="mt-1 flex items-center gap-2">
        <input
          value={value}
          onChange={e => onChange(e.target.value)}
          type={label === '币种' ? 'text' : 'number'}
          step="0.001"
          min="0"
          className="w-full bg-slate-900/70 border border-slate-700 rounded-md px-3 py-2 text-sm text-slate-100"
        />
        {suffix && <span className="text-xs text-slate-500 w-14">{suffix}</span>}
      </div>
    </label>
  )
}
