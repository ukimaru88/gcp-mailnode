import { useEffect, useState } from 'react'
import { Plus, Trash2, Pencil, Copy, X } from 'lucide-react'
// @ts-ignore - bindings 会在 wails build 时重新生成
import { ListPersonas, SavePersona, DeletePersona } from '../../wailsjs/go/main/App'
import { useToast } from '../components/Toast'
import { useConfirm } from '../components/ConfirmDialog'

interface ExtraHeader { name: string; value: string }

interface PersonaDTO {
  id: string
  name: string
  description: string
  received_template: string
  user_agent: string
  x_mailer: string
  extra_headers: ExtraHeader[]
  is_preset: boolean
  created_at?: any
}

const emptyPersona = (): PersonaDTO => ({
  id: '',
  name: '',
  description: '',
  received_template: '',
  user_agent: '',
  x_mailer: '',
  extra_headers: [],
  is_preset: false,
})

export default function Personas() {
  const { toast } = useToast()
  const confirmDlg = useConfirm()
  const [list, setList] = useState<PersonaDTO[]>([])
  const [modalOpen, setModalOpen] = useState(false)
  const [editing, setEditing] = useState<PersonaDTO>(emptyPersona())

  const refresh = async () => {
    try {
      const rows = await ListPersonas()
      setList((rows || []).map((r: any) => ({
        ...r,
        extra_headers: r.extra_headers || [],
      })))
    } catch (e: any) {
      toast('error', '加载失败: ' + (e?.message || e))
    }
  }
  useEffect(() => { refresh() }, [])

  const openNew = () => {
    setEditing(emptyPersona())
    setModalOpen(true)
  }

  const openEdit = (p: PersonaDTO) => {
    setEditing({
      ...p,
      extra_headers: [...(p.extra_headers || [])],
    })
    setModalOpen(true)
  }

  const openCopy = (p: PersonaDTO) => {
    setEditing({
      ...p,
      id: '',
      name: p.name + ' (副本)',
      is_preset: false,
      extra_headers: [...(p.extra_headers || [])],
    })
    setModalOpen(true)
  }

  const save = async () => {
    if (!editing.name.trim()) { toast('warning', '请填写名称'); return }
    try {
      const payload: any = { ...editing }
      await SavePersona(payload)
      toast('success', '已保存')
      setModalOpen(false)
      await refresh()
    } catch (e: any) {
      toast('error', '保存失败: ' + (e?.message || e))
    }
  }

  const remove = async (p: PersonaDTO) => {
    if (p.is_preset) { toast('warning', '预设不可删除'); return }
    if (!await confirmDlg({ message: `确认删除 "${p.name}"？`, danger: true })) return
    try {
      await DeletePersona(p.id)
      toast('success', '已删除')
      await refresh()
    } catch (e: any) {
      toast('error', '删除失败: ' + (e?.message || e))
    }
  }

  const addHeader = () => {
    setEditing(p => ({ ...p, extra_headers: [...(p.extra_headers || []), { name: '', value: '' }] }))
  }
  const updateHeader = (idx: number, field: 'name' | 'value', val: string) => {
    setEditing(p => ({
      ...p,
      extra_headers: (p.extra_headers || []).map((h, i) => i === idx ? { ...h, [field]: val } : h),
    }))
  }
  const removeHeader = (idx: number) => {
    setEditing(p => ({
      ...p,
      extra_headers: (p.extra_headers || []).filter((_, i) => i !== idx),
    }))
  }

  return (
    <div className="p-6 h-full overflow-auto">
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-xl font-bold text-slate-100">Persona 库</h1>
        <button onClick={openNew}
                className="bg-indigo-600 hover:bg-indigo-500 text-white rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
          <Plus size={14} /> 新建
        </button>
      </div>

      <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-slate-900/50 text-slate-400">
              <tr>
                <th className="text-left px-3 py-2 font-medium">名称</th>
                <th className="text-left px-3 py-2 font-medium">描述</th>
                <th className="text-left px-3 py-2 font-medium">预设</th>
                <th className="text-left px-3 py-2 font-medium">User-Agent</th>
                <th className="text-left px-3 py-2 font-medium">X-Mailer</th>
                <th className="text-right px-3 py-2 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.length === 0 && (
                <tr><td colSpan={6} className="text-center px-3 py-8 text-slate-500">暂无 Persona</td></tr>
              )}
              {list.map(p => (
                <tr key={p.id} className="border-t border-slate-700/40 hover:bg-slate-800/50">
                  <td className="px-3 py-2 text-slate-200">{p.name}</td>
                  <td className="px-3 py-2 text-slate-400 text-xs max-w-xs truncate" title={p.description}>{p.description || '-'}</td>
                  <td className="px-3 py-2">
                    {p.is_preset
                      ? <span className="px-1.5 py-0.5 rounded bg-sky-500/20 text-sky-300 text-xs">预设</span>
                      : <span className="px-1.5 py-0.5 rounded bg-slate-500/20 text-slate-400 text-xs">自定义</span>}
                  </td>
                  <td className="px-3 py-2 text-slate-300 text-xs font-mono max-w-xs truncate" title={p.user_agent}>{p.user_agent || '-'}</td>
                  <td className="px-3 py-2 text-slate-300 text-xs font-mono max-w-xs truncate" title={p.x_mailer}>{p.x_mailer || '-'}</td>
                  <td className="px-3 py-2 text-right">
                    <div className="inline-flex gap-1">
                      {p.is_preset ? (
                        <button onClick={() => openCopy(p)}
                                title="另存为副本"
                                className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-2 py-1 text-xs inline-flex items-center gap-1">
                          <Copy size={12} /> 另存为副本
                        </button>
                      ) : (
                        <>
                          <button onClick={() => openEdit(p)} title="编辑"
                                  className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-2 py-1 text-xs inline-flex items-center gap-1">
                            <Pencil size={12} /> 编辑
                          </button>
                          <button onClick={() => remove(p)} title="删除"
                                  className="bg-red-600 hover:bg-red-500 text-white rounded-md px-2 py-1 text-xs inline-flex items-center gap-1">
                            <Trash2 size={12} /> 删除
                          </button>
                        </>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {/* Modal */}
      {modalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4">
          <div className="bg-[#1a1d27] rounded-xl border border-slate-700/50 w-full max-w-2xl p-5 space-y-4 max-h-[90vh] overflow-auto">
            <div className="flex items-center justify-between">
              <h3 className="font-semibold text-slate-100">
                {editing.id ? '编辑 Persona' : '新建 Persona'}
              </h3>
              <button onClick={() => setModalOpen(false)} className="text-slate-500 hover:text-slate-300">
                <X size={16} />
              </button>
            </div>

            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="block text-xs text-slate-400 mb-1">名称</label>
                <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                       value={editing.name} onChange={e => setEditing({ ...editing, name: e.target.value })} />
              </div>
              <div>
                <label className="block text-xs text-slate-400 mb-1">描述</label>
                <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none"
                       value={editing.description} onChange={e => setEditing({ ...editing, description: e.target.value })} />
              </div>
            </div>

            <div>
              <label className="block text-xs text-slate-400 mb-1">
                Received 模板 <span className="text-slate-500">（占位符：{'{fqdn} {ip} {message_id} {timestamp}'}；每行一条 Received）</span>
              </label>
              <textarea
                className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-xs font-mono focus:border-indigo-500 outline-none"
                rows={6}
                value={editing.received_template}
                onChange={e => setEditing({ ...editing, received_template: e.target.value })}
                placeholder={'Received: from [{ip}] by {fqdn} ...\nReceived: from mail.example.com ...'}
              />
            </div>

            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="block text-xs text-slate-400 mb-1">User-Agent</label>
                <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none"
                       value={editing.user_agent} onChange={e => setEditing({ ...editing, user_agent: e.target.value })} />
              </div>
              <div>
                <label className="block text-xs text-slate-400 mb-1">X-Mailer</label>
                <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none"
                       value={editing.x_mailer} onChange={e => setEditing({ ...editing, x_mailer: e.target.value })} />
              </div>
            </div>

            <div>
              <div className="flex items-center justify-between mb-1">
                <label className="text-xs text-slate-400">额外 Headers</label>
                <button type="button" onClick={addHeader}
                        className="text-xs text-indigo-300 hover:text-indigo-200 inline-flex items-center gap-1">
                  <Plus size={12} /> 添加一行
                </button>
              </div>
              <div className="space-y-1.5">
                {(editing.extra_headers || []).length === 0 && (
                  <div className="text-xs text-slate-500 text-center py-3 border border-dashed border-slate-700 rounded-md">无额外 headers</div>
                )}
                {(editing.extra_headers || []).map((h, i) => (
                  <div key={i} className="flex gap-2 items-center">
                    <input placeholder="Header 名" value={h.name}
                           onChange={e => updateHeader(i, 'name', e.target.value)}
                           className="flex-1 bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1 text-xs font-mono focus:border-indigo-500 outline-none" />
                    <input placeholder="值" value={h.value}
                           onChange={e => updateHeader(i, 'value', e.target.value)}
                           className="flex-[2] bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1 text-xs font-mono focus:border-indigo-500 outline-none" />
                    <button onClick={() => removeHeader(i)}
                            className="text-red-400 hover:text-red-300 p-1"
                            title="删除">
                      <X size={13} />
                    </button>
                  </div>
                ))}
              </div>
            </div>

            <div className="flex justify-end gap-2 pt-2">
              <button onClick={() => setModalOpen(false)}
                      className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm">
                取消
              </button>
              <button onClick={save}
                      className="bg-indigo-600 hover:bg-indigo-500 text-white rounded-md px-3 py-1.5 text-sm">
                保存
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
