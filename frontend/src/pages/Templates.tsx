import { useEffect, useState } from 'react'
import { Plus, Pencil, Trash2, X, Copy } from 'lucide-react'
import { ListVPSTemplates, SaveVPSTemplate, DeleteVPSTemplate } from '../../wailsjs/go/main/App'
import { main } from '../../wailsjs/go/models'
import { useToast } from '../components/Toast'
import { useConfirm } from '../components/ConfirmDialog'

const REGION_OPTIONS = [
  { code: 'asia-northeast1', label: '日本东京' },
  { code: 'asia-northeast2', label: '日本大阪' },
  { code: 'asia-northeast3', label: '韩国首尔' },
  { code: 'asia-southeast1', label: '新加坡' },
]

const MACHINE_TYPES = [
  'e2-micro', 'e2-small', 'e2-medium', 'e2-standard-2', 'e2-standard-4',
  'n1-standard-1', 'n1-standard-2', 'n1-standard-4', 'n1-standard-8',  // n1-standard-8 Spot 折扣最高
]

const DISK_TYPES: { code: string; label: string }[] = [
  { code: 'pd-standard', label: 'pd-standard（HDD，最便宜）' },
  { code: 'pd-balanced', label: 'pd-balanced（均衡，默认）' },
  { code: 'pd-ssd',      label: 'pd-ssd（SSD，推荐发信）' },
]

interface FormState {
  id: string
  name: string
  regions: string[]
  auto_spread: boolean
  machine_type: string
  image_family: string
  image_project: string
  disk_size_gb: number
  disk_type: string
  tagsText: string
  metadata_script: string
  root_password: string
  deploy_type: string
  provisioning_model: string  // STANDARD / SPOT
  nic_count: number           // 1 / 8（Batch 2 启用）
  is_preset: boolean
}

const emptyForm = (): FormState => ({
  id: '',
  name: '',
  regions: [],
  auto_spread: true,
  machine_type: 'e2-micro',
  image_family: 'ubuntu-2204-lts',
  image_project: 'ubuntu-os-cloud',
  disk_size_gb: 20,
  disk_type: 'pd-ssd',
  tagsText: '',
  metadata_script: '',
  root_password: '',
  deploy_type: 'kumomta',
  provisioning_model: 'STANDARD',
  nic_count: 1,
  is_preset: false,
})

export default function Templates() {
  const { toast } = useToast()
  const confirmDlg = useConfirm()
  const [list, setList] = useState<main.VPSTemplateDTO[]>([])
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState<FormState>(emptyForm())
  const [busy, setBusy] = useState(false)

  const refresh = async () => {
    try {
      const r = await ListVPSTemplates()
      setList(r || [])
    } catch (e: any) { toast('error', '加载失败: ' + (e?.message || e)) }
  }
  useEffect(() => { refresh() }, [])

  const openNew = () => {
    setForm(emptyForm())
    setOpen(true)
  }

  const openEdit = (t: main.VPSTemplateDTO, asCopy = false) => {
    setForm({
      id: asCopy ? '' : t.id,
      name: asCopy ? t.name + ' 副本' : t.name,
      regions: [...(t.regions || [])],
      auto_spread: t.auto_spread,
      machine_type: t.machine_type,
      image_family: t.image_family,
      image_project: t.image_project,
      disk_size_gb: t.disk_size_gb,
      disk_type: (t as any).disk_type || 'pd-balanced',
      tagsText: (t.tags || []).join(' '),
      metadata_script: t.metadata_script,
      root_password: t.root_password,
      deploy_type: (t as any).deploy_type || 'kumomta',
      provisioning_model: (t as any).provisioning_model || 'STANDARD',
      nic_count: (t as any).nic_count || 1,
      is_preset: asCopy ? false : t.is_preset,
    })
    setOpen(true)
  }

  const toggleRegion = (code: string) => {
    setForm(f => ({
      ...f,
      regions: f.regions.includes(code) ? f.regions.filter(r => r !== code) : [...f.regions, code],
    }))
  }

  const save = async () => {
    if (!form.name.trim()) { toast('warning', '请填写名称'); return }
    setBusy(true)
    try {
      const dto = main.VPSTemplateDTO.createFrom({
        id: form.id,
        name: form.name.trim(),
        regions: form.regions,
        auto_spread: form.auto_spread,
        machine_type: form.machine_type,
        image_family: form.image_family.trim() || 'ubuntu-2204-lts',
        image_project: form.image_project.trim() || 'ubuntu-os-cloud',
        disk_size_gb: form.disk_size_gb,
        disk_type: form.disk_type || 'pd-balanced',
        tags: form.tagsText.trim() ? form.tagsText.trim().split(/\s+/) : [],
        metadata_script: form.metadata_script,
        root_password: form.root_password,
        deploy_type: form.deploy_type || 'kumomta',
        provisioning_model: form.provisioning_model || 'STANDARD',
        nic_count: form.nic_count || 1,
        is_preset: false,
        created_at: null,
      } as any)
      await SaveVPSTemplate(dto)
      toast('success', '已保存')
      setOpen(false)
      await refresh()
    } catch (e: any) {
      toast('error', '保存失败: ' + (e?.message || e))
    } finally { setBusy(false) }
  }

  const del = async (t: main.VPSTemplateDTO) => {
    if (t.is_preset) { toast('warning', '预设模板不可删除'); return }
    if (!await confirmDlg({ message: `确认删除模板「${t.name}」？`, danger: true })) return
    try { await DeleteVPSTemplate(t.id); toast('success', '已删除'); await refresh() }
    catch (e: any) { toast('error', '删除失败: ' + (e?.message || e)) }
  }

  return (
    <div className="p-6 h-full overflow-auto">
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-xl font-bold text-slate-100">开机模板</h1>
        <button className="bg-indigo-600 hover:bg-indigo-500 text-white rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5" onClick={openNew}>
          <Plus size={14} /> 新建模板
        </button>
      </div>

      <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-slate-900/50 text-slate-400">
              <tr>
                <th className="text-left px-3 py-2 font-medium">名称</th>
                <th className="text-left px-3 py-2 font-medium">类型</th>
                <th className="text-left px-3 py-2 font-medium">区域</th>
                <th className="text-left px-3 py-2 font-medium">机型</th>
                <th className="text-left px-3 py-2 font-medium">镜像</th>
                <th className="text-left px-3 py-2 font-medium">磁盘</th>
                <th className="text-left px-3 py-2 font-medium">盘类型</th>
                <th className="text-left px-3 py-2 font-medium">预设</th>
                <th className="text-right px-3 py-2 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.length === 0 && (
                <tr><td colSpan={9} className="text-center px-3 py-8 text-slate-500">暂无模板</td></tr>
              )}
              {list.map(t => {
                const dt = (t as any).deploy_type || 'kumomta'
                return (
                <tr key={t.id} className="border-t border-slate-700/40 hover:bg-slate-800/50">
                  <td className="px-3 py-2 text-slate-200">{t.name}</td>
                  <td className="px-3 py-2 text-xs">
                    {dt === 'mailcow' ? <span className="px-1.5 py-0.5 rounded bg-purple-500/20 text-purple-300">📥 收发</span>
                                      : dt === 'postfix' ? <span className="px-1.5 py-0.5 rounded bg-emerald-500/20 text-emerald-300">📬 Postfix</span>
                                      : <span className="px-1.5 py-0.5 rounded bg-indigo-500/20 text-indigo-300">🚀 KumoMTA</span>}
                  </td>
                  <td className="px-3 py-2 text-slate-300 text-xs">{(t.regions || []).join(', ') || '-'}{t.auto_spread && <span className="ml-1 text-indigo-400">(自动分配)</span>}</td>
                  <td className="px-3 py-2 text-slate-300 font-mono text-xs">{t.machine_type}</td>
                  <td className="px-3 py-2 text-slate-300 text-xs">{t.image_project}/{t.image_family}</td>
                  <td className="px-3 py-2 text-slate-300">{t.disk_size_gb}G</td>
                  <td className="px-3 py-2 text-xs">
                    <span className={`font-mono ${
                      ((t as any).disk_type || '') === 'pd-ssd' ? 'text-green-400'
                      : ((t as any).disk_type || '') === 'pd-standard' ? 'text-slate-500'
                      : 'text-slate-300'
                    }`}>{(t as any).disk_type || 'pd-balanced'}</span>
                  </td>
                  <td className="px-3 py-2">
                    {t.is_preset
                      ? <span className="inline-block px-1.5 py-0.5 rounded bg-indigo-500/20 text-indigo-300 text-xs">预设</span>
                      : <span className="text-slate-500 text-xs">-</span>}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <div className="inline-flex gap-1.5">
                      {t.is_preset ? (
                        <button className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-2 py-1 text-xs inline-flex items-center gap-1" onClick={() => openEdit(t, true)}>
                          <Copy size={12} /> 另存为副本
                        </button>
                      ) : (
                        <button className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-2 py-1 text-xs inline-flex items-center gap-1" onClick={() => openEdit(t)}>
                          <Pencil size={12} /> 编辑
                        </button>
                      )}
                      <button className="bg-red-600 hover:bg-red-500 disabled:opacity-40 disabled:cursor-not-allowed text-white rounded-md px-2 py-1 text-xs inline-flex items-center gap-1" disabled={t.is_preset} onClick={() => del(t)}>
                        <Trash2 size={12} /> 删除
                      </button>
                    </div>
                  </td>
                </tr>
              )})}
            </tbody>
          </table>
        </div>
      </div>

      {open && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50" onClick={() => !busy && setOpen(false)}>
          <div className="bg-[#1a1d27] border border-slate-700 rounded-lg p-6 w-[680px] max-w-[95vw] max-h-[90vh] overflow-auto" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-4">
              <h3 className="font-semibold text-slate-100">{form.id ? '编辑模板' : '新建模板'}</h3>
              <button className="text-slate-500 hover:text-slate-300" onClick={() => !busy && setOpen(false)}>
                <X size={16} />
              </button>
            </div>

            <div className="grid grid-cols-2 gap-3">
              <div className="col-span-2">
                <label className="block text-xs text-slate-400 mb-1">名称</label>
                <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none" value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} />
              </div>

              <div className="col-span-2">
                <label className="block text-xs text-slate-400 mb-1">区域（可多选）</label>
                <div className="grid grid-cols-2 gap-2">
                  {REGION_OPTIONS.map(r => (
                    <label key={r.code} className="inline-flex items-center gap-2 px-2 py-1.5 bg-slate-900 border border-slate-700 rounded-md text-sm cursor-pointer hover:border-indigo-500">
                      <input type="checkbox" className="accent-indigo-500" checked={form.regions.includes(r.code)} onChange={() => toggleRegion(r.code)} />
                      <span className="font-mono text-xs text-slate-300">{r.code}</span>
                      <span className="text-xs text-slate-500">{r.label}</span>
                    </label>
                  ))}
                </div>
              </div>

              <div className="col-span-2">
                <label className="inline-flex items-center gap-2 cursor-pointer">
                  <input type="checkbox" className="accent-indigo-500" checked={form.auto_spread} onChange={e => setForm({ ...form, auto_spread: e.target.checked })} />
                  <span className="text-sm text-slate-300">自动分配到选中区域</span>
                </label>
              </div>

              <div>
                <label className="block text-xs text-slate-400 mb-1">机型</label>
                <select className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none" value={form.machine_type} onChange={e => setForm({ ...form, machine_type: e.target.value })}>
                  {MACHINE_TYPES.map(m => <option key={m} value={m}>{m}</option>)}
                </select>
              </div>

              <div>
                <label className="block text-xs text-slate-400 mb-1">磁盘 (GB)</label>
                <input type="number" min={10} className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none" value={form.disk_size_gb} onChange={e => setForm({ ...form, disk_size_gb: Number(e.target.value) || 10 })} />
              </div>

              <div className="col-span-2">
                <label className="block text-xs text-slate-400 mb-1">磁盘类型（KumoMTA spool 重 IO，生产建议 pd-ssd）</label>
                <select className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none" value={form.disk_type} onChange={e => setForm({ ...form, disk_type: e.target.value })}>
                  {DISK_TYPES.map(d => <option key={d.code} value={d.code}>{d.label}</option>)}
                </select>
              </div>

              <div className="col-span-2">
                <label className="block text-xs text-slate-400 mb-1">部署类型</label>
                <select className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none" value={form.deploy_type} onChange={e => setForm({ ...form, deploy_type: e.target.value })}>
                  <option value="kumomta">KumoMTA（纯发信，高并发，内存占用小）</option>
                  <option value="postfix">Postfix + OpenDKIM（纯发信，经典稳定，与 mail-toolkit 同源）</option>
                  <option value="mailcow">mailcow（收发一体，Web UI + IMAP/SMTP，邮件大师可登录）</option>
                </select>
                <p className="text-[10px] text-slate-500 mt-0.5">
                  {form.deploy_type === 'mailcow'
                    ? '建议 4GB+ 内存；禁用 ClamAV 节省 2GB；部署后通过 https://{FQDN}/ 管理，默认 admin/moohoo'
                    : form.deploy_type === 'postfix'
                    ? 'Postfix + OpenDKIM 纯发信；25/587/465/2525 全开，SASL 鉴权；单 NIC 模式'
                    : 'KumoMTA 仅发信不收信；想收回信请改选 mailcow'}
                </p>
              </div>

              <div className="col-span-2">
                <label className="block text-xs text-slate-400 mb-1">计费模式</label>
                <div className="flex gap-2">
                  <label className={`flex-1 flex items-center gap-2 px-3 py-2 border rounded-md cursor-pointer ${form.provisioning_model === 'STANDARD' ? 'bg-indigo-500/20 border-indigo-500/50 text-indigo-200' : 'bg-slate-900 border-slate-700 text-slate-400 hover:border-slate-600'}`}>
                    <input type="radio" className="accent-indigo-500" checked={form.provisioning_model === 'STANDARD'} onChange={() => setForm({ ...form, provisioning_model: 'STANDARD' })} />
                    <div>
                      <div className="text-sm">按需 (STANDARD)</div>
                      <div className="text-[10px] text-slate-500">稳定不被抢占，按官方原价</div>
                    </div>
                  </label>
                  <label className={`flex-1 flex items-center gap-2 px-3 py-2 border rounded-md cursor-pointer ${form.provisioning_model === 'SPOT' ? 'bg-amber-500/20 border-amber-500/50 text-amber-200' : 'bg-slate-900 border-slate-700 text-slate-400 hover:border-slate-600'}`}>
                    <input type="radio" className="accent-amber-500" checked={form.provisioning_model === 'SPOT'} onChange={() => setForm({ ...form, provisioning_model: 'SPOT' })} />
                    <div>
                      <div className="text-sm">⚡ Spot（73% off）</div>
                      <div className="text-[10px] text-slate-500">东京 n1 折扣最高，可被抢占（30 秒预通知 → 删除实例）</div>
                    </div>
                  </label>
                </div>
                {form.provisioning_model === 'SPOT' && (
                  <p className="text-[10px] text-amber-400/80 mt-1">
                    ⚠ Spot 抢占时实例直接 DELETE（不是 STOP）。3 天即抛业务模式适合，需要长时间稳定的不要选。
                  </p>
                )}
              </div>

              <div>
                <label className="block text-xs text-slate-400 mb-1">镜像 family</label>
                <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none" value={form.image_family} onChange={e => setForm({ ...form, image_family: e.target.value })} />
              </div>

              <div>
                <label className="block text-xs text-slate-400 mb-1">镜像 project</label>
                <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none" value={form.image_project} onChange={e => setForm({ ...form, image_project: e.target.value })} />
              </div>

              <div className="col-span-2">
                <label className="block text-xs text-slate-400 mb-1">Tags（空格分隔）</label>
                <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none" value={form.tagsText} onChange={e => setForm({ ...form, tagsText: e.target.value })} placeholder="http-server https-server smtp" />
              </div>

              <div className="col-span-2">
                <label className="block text-xs text-slate-400 mb-1">Metadata Script</label>
                <textarea className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-xs font-mono focus:border-indigo-500 outline-none" rows={6} value={form.metadata_script} onChange={e => setForm({ ...form, metadata_script: e.target.value })} placeholder="空则自动生成 root+密码 startup-script" />
              </div>

              <div className="col-span-2">
                <label className="block text-xs text-slate-400 mb-1">Root 密码</label>
                <input type="password" className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none" value={form.root_password} onChange={e => setForm({ ...form, root_password: e.target.value })} />
              </div>
            </div>

            <div className="flex justify-end gap-2 mt-5">
              <button className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm" disabled={busy} onClick={() => setOpen(false)}>取消</button>
              <button className="bg-indigo-600 hover:bg-indigo-500 disabled:opacity-50 text-white rounded-md px-3 py-1.5 text-sm" disabled={busy} onClick={save}>
                {busy ? '保存中...' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
