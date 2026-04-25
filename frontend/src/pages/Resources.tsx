import { useEffect, useState } from 'react'
import { RefreshCw, Trash2, Globe, Settings2, X } from 'lucide-react'
import {
  ListVPS, ListStaticIPs, ListDNSRecords, BatchDelete,
  // @ts-ignore - bindings 会在 wails build 时重新生成
  BatchSetPTR,
  // @ts-ignore
  StartMTADeploy,
  // @ts-ignore
  ListPersonas,
  // @ts-ignore
  FixMailNodeTag,
  // @ts-ignore
  CleanupOrphanResources,
} from '../../wailsjs/go/main/App'
import { main } from '../../wailsjs/go/models'
import { useToast } from '../components/Toast'
import { useConfirm } from '../components/ConfirmDialog'

type TabKind = 'vps' | 'ip' | 'dns'

const fmtDate = (v: any) => { try { return new Date(v as string).toLocaleString('zh-CN') } catch { return '-' } }

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
  }
  return `px-1.5 py-0.5 rounded text-xs ${map[s] || 'bg-slate-500/20 text-slate-400'}`
}

export default function Resources() {
  const { toast } = useToast()
  const confirmDlg = useConfirm()
  const [tab, setTab] = useState<TabKind>('vps')
  const [vpsList, setVpsList] = useState<main.VPSInstanceDTO[]>([])
  const [ipList, setIpList] = useState<main.StaticIPDTO[]>([])
  const [dnsList, setDnsList] = useState<main.DNSRecordDTO[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [showDeleted, setShowDeleted] = useState(false)

  const displayedVpsList = showDeleted ? vpsList : vpsList.filter(v => v.status !== 'deleted')
  const displayedIpList  = showDeleted ? ipList  : ipList.filter(i => i.status !== 'released')

  // Deploy Modal 状态
  const [deployModalOpen, setDeployModalOpen] = useState(false)
  const [personas, setPersonas] = useState<any[]>([])
  const [personaID, setPersonaID] = useState('')
  const [hideClientIP, setHideClientIP] = useState(true)

  const refresh = async () => {
    try {
      const [v, i, d] = await Promise.all([ListVPS(), ListStaticIPs(), ListDNSRecords()])
      setVpsList(v || []); setIpList(i || []); setDnsList(d || [])
      setSelected(new Set())
    } catch (e: any) { toast('error', '加载失败: ' + (e?.message || e)) }
  }
  useEffect(() => { refresh() }, [])

  const toggle = (id: string) => {
    setSelected(prev => {
      const n = new Set(prev)
      n.has(id) ? n.delete(id) : n.add(id)
      return n
    })
  }

  const currentList: { id: string }[] = tab === 'vps' ? displayedVpsList : tab === 'ip' ? displayedIpList : dnsList
  const toggleAll = () => {
    if (selected.size === currentList.length) setSelected(new Set())
    else setSelected(new Set(currentList.map(x => x.id)))
  }

  const batchDel = async () => {
    if (selected.size === 0) { toast('warning', '请先勾选'); return }
    if (!await confirmDlg({
      message: `确认批量删除 ${selected.size} 条记录？此操作会同步清理 GCP / 阿里云云端资源。`,
      danger: true,
    })) return
    try {
      const n = await BatchDelete(tab, Array.from(selected))
      toast('success', `成功删除 ${n} 条`)
      await refresh()
    } catch (e: any) { toast('error', '删除失败: ' + (e?.message || e)) }
  }

  const cleanupOrphans = async () => {
    if (!await confirmDlg({
      message: '清理本地孤立记录？\n\n仅清除"对应 GCP 凭证已被删除"的本地数据库记录，不调用任何云端 API。如果云端 VPS / 静态 IP / DNS 记录还在，需要你自行去 GCP / 阿里云控制台删除。',
      danger: false,
    })) return
    try {
      const r = await CleanupOrphanResources() as { vps_deleted: number; static_ips_deleted: number; dns_records_deleted: number }
      const total = r.vps_deleted + r.static_ips_deleted + r.dns_records_deleted
      if (total === 0) {
        toast('success', '没有孤立记录需要清理')
      } else {
        toast('success', `已清理 VPS=${r.vps_deleted}, 静态 IP=${r.static_ips_deleted}, DNS=${r.dns_records_deleted}`)
      }
      await refresh()
    } catch (e: any) { toast('error', '清理失败: ' + (e?.message || e)) }
  }

  const batchPTR = async () => {
    if (selected.size === 0) { toast('warning', '请先勾选 VPS'); return }
    try {
      const taskID = await BatchSetPTR(Array.from(selected))
      toast('success', `已提交 ${selected.size} 台 VPS 的 PTR 任务${taskID ? `：${taskID}` : ''}`)
    } catch (e: any) { toast('error', 'PTR 失败: ' + (e?.message || e)) }
  }

  const fixTag = async () => {
    if (selected.size === 0) { toast('warning', '请先勾选 VPS'); return }
    try {
      const n = await FixMailNodeTag(Array.from(selected))
      toast('success', `已修复 ${n} 台 VPS 的 mail-node tag（25/587/465 等端口现在应可访问）`)
      await refresh()
    } catch (e: any) { toast('error', '修复 Tag 失败: ' + (e?.message || e)) }
  }

  const openDeployModal = async () => {
    if (selected.size === 0) { toast('warning', '请先勾选 VPS'); return }
    try {
      const list = await ListPersonas()
      setPersonas(list || [])
    } catch (e: any) {
      toast('error', '加载 Persona 失败: ' + (e?.message || e))
    }
    setDeployModalOpen(true)
  }

  const confirmDeploy = async () => {
    try {
      const taskID = await StartMTADeploy(Array.from(selected), {
        hide_client_ip: hideClientIP,
        persona_id: personaID,
      } as any)
      toast('success', `已对 ${selected.size} 台 VPS 提交部署任务${taskID ? `：${taskID}` : ''}`)
      setDeployModalOpen(false)
    } catch (e: any) {
      toast('error', '部署失败: ' + (e?.message || e))
    }
  }

  const selectedPersona = personas.find(p => p.id === personaID)

  const tabs: { k: TabKind; label: string; count: number }[] = [
    { k: 'vps', label: 'VPS 实例', count: displayedVpsList.length },
    { k: 'ip',  label: '静态 IP',  count: displayedIpList.length },
    { k: 'dns', label: 'DNS 记录', count: dnsList.length },
  ]

  return (
    <div className="p-6 h-full overflow-auto">
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-xl font-bold text-slate-100">资源清单</h1>
        <div className="inline-flex gap-2 items-center">
          <label className="inline-flex items-center gap-1.5 text-xs text-slate-400 cursor-pointer select-none mr-2">
            <input type="checkbox" className="accent-indigo-500"
                   checked={showDeleted} onChange={e => setShowDeleted(e.target.checked)} />
            显示已释放/已删除
          </label>
          <button onClick={refresh} className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
            <RefreshCw size={14} /> 刷新
          </button>
          <button onClick={cleanupOrphans}
                  title="清理本地数据库中对应 GCP 凭证已被删除的孤立记录（不调用云端 API）"
                  className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
            🧹 清理孤立记录
          </button>
          {tab === 'vps' && (
            <>
              <button onClick={fixTag} disabled={selected.size === 0}
                      title="给 VPS 补打 mail-node network tag，让 25/587/465 等端口通过 GCP 防火墙"
                      className="bg-amber-600 hover:bg-amber-500 disabled:opacity-40 disabled:cursor-not-allowed text-white rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
                🔧 修复 Tag ({selected.size})
              </button>
              <button onClick={batchPTR} disabled={selected.size === 0}
                      className="bg-indigo-600 hover:bg-indigo-500 disabled:opacity-40 disabled:cursor-not-allowed text-white rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
                <Globe size={14} /> 批量设 PTR ({selected.size})
              </button>
              <button onClick={openDeployModal} disabled={selected.size === 0}
                      className="bg-indigo-600 hover:bg-indigo-500 disabled:opacity-40 disabled:cursor-not-allowed text-white rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
                <Settings2 size={14} /> 批量部署 KumoMTA ({selected.size})
              </button>
            </>
          )}
          <button onClick={batchDel} disabled={selected.size === 0}
                  className="bg-red-600 hover:bg-red-500 disabled:opacity-40 disabled:cursor-not-allowed text-white rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
            <Trash2 size={14} /> 批量删除 ({selected.size})
          </button>
        </div>
      </div>

      <div className="flex gap-1 mb-3 border-b border-slate-700/50">
        {tabs.map(t => (
          <button key={t.k} onClick={() => { setTab(t.k); setSelected(new Set()) }}
                  className={`px-3 py-2 text-sm border-b-2 transition-colors ${
                    tab === t.k ? 'border-indigo-500 text-indigo-300' : 'border-transparent text-slate-400 hover:text-slate-200'
                  }`}>
            {t.label} <span className="text-slate-500">({t.count})</span>
          </button>
        ))}
      </div>

      <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-slate-900/50 text-slate-400">
              {tab === 'vps' && (
                <tr>
                  <th className="px-3 py-2"><input type="checkbox" checked={selected.size === displayedVpsList.length && displayedVpsList.length > 0} onChange={toggleAll} /></th>
                  <th className="text-left px-3 py-2 font-medium">名称</th>
                  <th className="text-left px-3 py-2 font-medium">区/可用区</th>
                  <th className="text-left px-3 py-2 font-medium">机型</th>
                  <th className="text-left px-3 py-2 font-medium">状态</th>
                  <th className="text-left px-3 py-2 font-medium">IP</th>
                  <th className="text-left px-3 py-2 font-medium">FQDN</th>
                  <th className="text-left px-3 py-2 font-medium">部署状态</th>
                  <th className="text-left px-3 py-2 font-medium">PTR</th>
                  <th className="text-left px-3 py-2 font-medium">错误</th>
                  <th className="text-left px-3 py-2 font-medium">创建</th>
                </tr>
              )}
              {tab === 'ip' && (
                <tr>
                  <th className="px-3 py-2"><input type="checkbox" checked={selected.size === displayedIpList.length && displayedIpList.length > 0} onChange={toggleAll} /></th>
                  <th className="text-left px-3 py-2 font-medium">IP</th>
                  <th className="text-left px-3 py-2 font-medium">区域</th>
                  <th className="text-left px-3 py-2 font-medium">状态</th>
                  <th className="text-left px-3 py-2 font-medium">DNSBL</th>
                  <th className="text-left px-3 py-2 font-medium">命中列表</th>
                  <th className="text-left px-3 py-2 font-medium">绑定实例</th>
                  <th className="text-left px-3 py-2 font-medium">创建</th>
                </tr>
              )}
              {tab === 'dns' && (
                <tr>
                  <th className="px-3 py-2"><input type="checkbox" checked={selected.size === dnsList.length && dnsList.length > 0} onChange={toggleAll} /></th>
                  <th className="text-left px-3 py-2 font-medium">域名</th>
                  <th className="text-left px-3 py-2 font-medium">主机记录</th>
                  <th className="text-left px-3 py-2 font-medium">类型</th>
                  <th className="text-left px-3 py-2 font-medium">值</th>
                  <th className="text-left px-3 py-2 font-medium">阿里云 RecordID</th>
                  <th className="text-left px-3 py-2 font-medium">关联实例</th>
                  <th className="text-left px-3 py-2 font-medium">创建</th>
                </tr>
              )}
            </thead>
            <tbody>
              {tab === 'vps' && (
                <>
                  {displayedVpsList.length === 0 && <tr><td colSpan={11} className="text-center px-3 py-8 text-slate-500">暂无 VPS</td></tr>}
                  {displayedVpsList.map(v => (
                    <tr key={v.id} className="border-t border-slate-700/40 hover:bg-slate-800/50">
                      <td className="px-3 py-2"><input type="checkbox" checked={selected.has(v.id)} onChange={() => toggle(v.id)} /></td>
                      <td className="px-3 py-2 text-slate-200">{v.name}</td>
                      <td className="px-3 py-2 text-slate-400 text-xs">{v.region}<br/><span className="text-slate-600">{v.zone}</span></td>
                      <td className="px-3 py-2 text-slate-300 font-mono text-xs">{v.machine_type}</td>
                      <td className="px-3 py-2"><span className={statusBadge(v.status)}>{v.status}</span></td>
                      <td className="px-3 py-2 text-slate-300 font-mono text-xs">{v.ip || '-'}</td>
                      <td className="px-3 py-2 text-slate-300 text-xs">{v.fqdn || '-'}</td>
                      <td className="px-3 py-2"><span className={statusBadge(v.deploy_status)}>{v.deploy_status}</span></td>
                      <td className="px-3 py-2"><span className={statusBadge(v.ptr_status || '-')}>{v.ptr_status || '-'}</span></td>
                      <td className="px-3 py-2 text-red-300 text-xs max-w-xs truncate" title={v.deploy_error}>{v.deploy_error || '-'}</td>
                      <td className="px-3 py-2 text-slate-500 text-xs">{fmtDate(v.created_at)}</td>
                    </tr>
                  ))}
                </>
              )}
              {tab === 'ip' && (
                <>
                  {displayedIpList.length === 0 && <tr><td colSpan={8} className="text-center px-3 py-8 text-slate-500">暂无静态 IP</td></tr>}
                  {displayedIpList.map(i => (
                    <tr key={i.id} className="border-t border-slate-700/40 hover:bg-slate-800/50">
                      <td className="px-3 py-2"><input type="checkbox" checked={selected.has(i.id)} onChange={() => toggle(i.id)} /></td>
                      <td className="px-3 py-2 text-slate-200 font-mono">{i.ip}</td>
                      <td className="px-3 py-2 text-slate-300 text-xs">{i.region}</td>
                      <td className="px-3 py-2"><span className={statusBadge(i.status)}>{i.status}</span></td>
                      <td className="px-3 py-2">
                        <span className={i.dnsbl_result === 'clean' ? 'text-green-400' : i.dnsbl_result === 'dirty' ? 'text-red-400' : 'text-slate-500'}>
                          {i.dnsbl_result || '-'}
                        </span>
                      </td>
                      <td className="px-3 py-2 text-slate-400 text-xs max-w-xs truncate" title={i.dnsbl_hit_lists}>{i.dnsbl_hit_lists || '-'}</td>
                      <td className="px-3 py-2 text-slate-400 text-xs font-mono">{i.bound_instance_id ? i.bound_instance_id.slice(0, 8) : '-'}</td>
                      <td className="px-3 py-2 text-slate-500 text-xs">{fmtDate(i.created_at)}</td>
                    </tr>
                  ))}
                </>
              )}
              {tab === 'dns' && (
                <>
                  {dnsList.length === 0 && <tr><td colSpan={8} className="text-center px-3 py-8 text-slate-500">暂无 DNS 记录</td></tr>}
                  {dnsList.map(d => (
                    <tr key={d.id} className="border-t border-slate-700/40 hover:bg-slate-800/50">
                      <td className="px-3 py-2"><input type="checkbox" checked={selected.has(d.id)} onChange={() => toggle(d.id)} /></td>
                      <td className="px-3 py-2 text-slate-200 text-xs">{d.domain}</td>
                      <td className="px-3 py-2 text-slate-300 font-mono text-xs">{d.rr}</td>
                      <td className="px-3 py-2"><span className="px-1.5 py-0.5 rounded bg-slate-700 text-slate-300 text-xs">{d.record_type}</span></td>
                      <td className="px-3 py-2 text-slate-400 text-xs max-w-md truncate" title={d.value}>{d.value}</td>
                      <td className="px-3 py-2 text-slate-500 text-xs font-mono">{d.aliyun_record_id || '-'}</td>
                      <td className="px-3 py-2 text-slate-500 text-xs font-mono">{d.related_instance_id ? d.related_instance_id.slice(0, 8) : '-'}</td>
                      <td className="px-3 py-2 text-slate-500 text-xs">{fmtDate(d.created_at)}</td>
                    </tr>
                  ))}
                </>
              )}
            </tbody>
          </table>
        </div>
      </div>

      {/* Deploy Modal */}
      {deployModalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4">
          <div className="bg-[#1a1d27] rounded-xl border border-slate-700/50 w-full max-w-lg p-5 space-y-4">
            <div className="flex items-center justify-between">
              <h3 className="font-semibold text-slate-100">部署 KumoMTA 配置</h3>
              <button onClick={() => setDeployModalOpen(false)} className="text-slate-500 hover:text-slate-300">
                <X size={16} />
              </button>
            </div>

            <label className="flex items-start gap-2 p-3 bg-slate-900 border border-slate-700 rounded-md cursor-pointer">
              <input type="checkbox" className="accent-indigo-500 mt-0.5"
                     checked={hideClientIP} onChange={e => setHideClientIP(e.target.checked)} />
              <div>
                <div className="text-sm text-slate-200">🛡️ 屏蔽发件端真实 IP</div>
                <div className="text-xs text-slate-500">移除 Received 链头部真实 IP，写入伪造链</div>
              </div>
            </label>

            <div>
              <label className="block text-xs text-slate-400 mb-1">Persona（伪造身份）</label>
              <select className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                      value={personaID} onChange={e => setPersonaID(e.target.value)}>
                <option value="">-- 不伪造 --</option>
                {personas.map(p => (
                  <option key={p.id} value={p.id}>
                    {p.name}{p.is_preset ? ' [预设]' : ''}
                  </option>
                ))}
              </select>
            </div>

            {selectedPersona && (
              <div className="bg-slate-950 border border-slate-700/50 rounded-md p-3 space-y-2">
                <div>
                  <div className="text-xs text-slate-500 mb-0.5">Received 模板</div>
                  <pre className="text-xs text-slate-300 font-mono whitespace-pre-wrap break-all">{selectedPersona.received_template || '(空)'}</pre>
                </div>
                <div>
                  <div className="text-xs text-slate-500 mb-0.5">User-Agent</div>
                  <div className="text-xs text-slate-300 font-mono break-all">{selectedPersona.user_agent || '(空)'}</div>
                </div>
                <div>
                  <div className="text-xs text-slate-500 mb-0.5">X-Mailer</div>
                  <div className="text-xs text-slate-300 font-mono break-all">{selectedPersona.x_mailer || '(空)'}</div>
                </div>
              </div>
            )}

            <div className="flex justify-end gap-2 pt-2">
              <button onClick={() => setDeployModalOpen(false)}
                      className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm">
                取消
              </button>
              <button onClick={confirmDeploy}
                      className="bg-indigo-600 hover:bg-indigo-500 text-white rounded-md px-3 py-1.5 text-sm">
                开始部署
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
