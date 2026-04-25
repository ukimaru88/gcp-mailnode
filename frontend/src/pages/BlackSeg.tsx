import { useEffect, useMemo, useState } from 'react'
import { Plus, Trash2, Search, Upload, Target } from 'lucide-react'
import {
  AddBlackSegment,
  ListBlackSegments,
  RemoveBlackSegment,
  ImportBlackSegmentsText,
  CheckIPBlackSegment,
} from '../../wailsjs/go/main/App'
import { main } from '../../wailsjs/go/models'
import { useToast } from '../components/Toast'
import { useConfirm } from '../components/ConfirmDialog'

export default function BlackSeg() {
  const { toast } = useToast()
  const confirmDlg = useConfirm()
  const [list, setList] = useState<main.BlackSegmentDTO[]>([])
  const [cidr, setCidr] = useState('')
  const [note, setNote] = useState('')
  const [bulk, setBulk] = useState('')
  const [testIP, setTestIP] = useState('')
  const [testResult, setTestResult] = useState<{ cidr: string; note: string } | null>(null)
  const [search, setSearch] = useState('')
  const [busy, setBusy] = useState(false)

  const refresh = async () => {
    try { const r = await ListBlackSegments(); setList(r || []) }
    catch (e: any) { toast('error', '加载失败: ' + (e?.message || e)) }
  }
  useEffect(() => { refresh() }, [])

  const addOne = async () => {
    if (!cidr.trim()) { toast('warning', '请输入 CIDR'); return }
    setBusy(true)
    try {
      await AddBlackSegment(cidr.trim(), note.trim())
      toast('success', '已添加')
      setCidr(''); setNote('')
      await refresh()
    } catch (e: any) { toast('error', '添加失败: ' + (e?.message || e)) }
    finally { setBusy(false) }
  }

  const importBulk = async () => {
    if (!bulk.trim()) { toast('warning', '请粘贴 CIDR 列表'); return }
    setBusy(true)
    try {
      const r: any = await ImportBlackSegmentsText(bulk)
      const imported = r?.imported ?? 0
      const duplicates = r?.duplicates ?? 0
      const errors = (r?.parse_errors || []).length
      toast('success', `导入 ${imported}，重复 ${duplicates}，错误 ${errors}`)
      setBulk('')
      await refresh()
    } catch (e: any) { toast('error', '导入失败: ' + (e?.message || e)) }
    finally { setBusy(false) }
  }

  const checkIP = async () => {
    if (!testIP.trim()) { toast('warning', '请输入 IP'); return }
    setTestResult(null)
    try {
      const r = await CheckIPBlackSegment(testIP.trim())
      setTestResult({ cidr: r?.cidr || '', note: r?.note || '' })
    } catch (e: any) {
      toast('error', '检查失败: ' + (e?.message || e))
    }
  }

  const remove = async (id: number) => {
    if (!await confirmDlg({ message: '确认删除该黑段？', danger: true })) return
    try { await RemoveBlackSegment(id); toast('success', '已删除'); await refresh() }
    catch (e: any) { toast('error', '删除失败: ' + (e?.message || e)) }
  }

  const filtered = useMemo(() => {
    if (!search.trim()) return list
    const s = search.toLowerCase()
    return list.filter(b =>
      b.cidr.toLowerCase().includes(s) || (b.note || '').toLowerCase().includes(s)
    )
  }, [list, search])

  return (
    <div className="p-6 h-full overflow-auto">
      <h1 className="text-xl font-bold text-slate-100 mb-4">IP 黑段库</h1>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4 mb-4">
        {/* 单条添加 */}
        <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4">
          <h2 className="font-semibold text-slate-100 mb-3 text-sm flex items-center gap-2">
            <Plus size={14} className="text-indigo-400" /> 单条添加
          </h2>
          <div className="space-y-2">
            <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none" placeholder="CIDR, e.g. 1.2.3.0/24" value={cidr} onChange={e => setCidr(e.target.value)} />
            <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none" placeholder="备注（可选）" value={note} onChange={e => setNote(e.target.value)} />
            <button className="w-full bg-indigo-600 hover:bg-indigo-500 disabled:opacity-50 text-white rounded-md px-3 py-1.5 text-sm" disabled={busy} onClick={addOne}>添加</button>
          </div>
        </div>

        {/* 批量导入 */}
        <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4">
          <h2 className="font-semibold text-slate-100 mb-3 text-sm flex items-center gap-2">
            <Upload size={14} className="text-indigo-400" /> 批量导入
          </h2>
          <div className="space-y-2">
            <textarea className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-xs font-mono focus:border-indigo-500 outline-none" rows={4} placeholder="一行一个 CIDR，可附加空格 + 备注" value={bulk} onChange={e => setBulk(e.target.value)} />
            <button className="w-full bg-indigo-600 hover:bg-indigo-500 disabled:opacity-50 text-white rounded-md px-3 py-1.5 text-sm" disabled={busy} onClick={importBulk}>导入</button>
          </div>
        </div>

        {/* IP 测试 */}
        <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4">
          <h2 className="font-semibold text-slate-100 mb-3 text-sm flex items-center gap-2">
            <Target size={14} className="text-indigo-400" /> IP 测试
          </h2>
          <div className="space-y-2">
            <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none" placeholder="要检查的 IP" value={testIP} onChange={e => setTestIP(e.target.value)} />
            <button className="w-full bg-indigo-600 hover:bg-indigo-500 text-white rounded-md px-3 py-1.5 text-sm" onClick={checkIP}>检查</button>
            {testResult && (
              <div className={`text-xs rounded-md px-2 py-1.5 border ${testResult.cidr ? 'border-red-500/40 bg-red-500/10 text-red-300' : 'border-green-500/40 bg-green-500/10 text-green-300'}`}>
                {testResult.cidr
                  ? <>命中黑段 <span className="font-mono">{testResult.cidr}</span>{testResult.note && <> · {testResult.note}</>}</>
                  : '未命中任何黑段'}
              </div>
            )}
          </div>
        </div>
      </div>

      {/* 列表 */}
      <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 overflow-hidden">
        <div className="flex items-center gap-2 px-4 py-3 border-b border-slate-700/50">
          <Search size={14} className="text-slate-400" />
          <input className="flex-1 bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1 text-sm focus:border-indigo-500 outline-none" placeholder="按 CIDR 或备注搜索..." value={search} onChange={e => setSearch(e.target.value)} />
          <span className="text-xs text-slate-500">{filtered.length} / {list.length}</span>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-slate-900/50 text-slate-400">
              <tr>
                <th className="text-left px-3 py-2 font-medium w-20">ID</th>
                <th className="text-left px-3 py-2 font-medium">CIDR</th>
                <th className="text-left px-3 py-2 font-medium">备注</th>
                <th className="text-right px-3 py-2 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {filtered.length === 0 && (
                <tr><td colSpan={4} className="text-center px-3 py-8 text-slate-500">暂无数据</td></tr>
              )}
              {filtered.map(b => (
                <tr key={b.id} className="border-t border-slate-700/40 hover:bg-slate-800/50">
                  <td className="px-3 py-2 text-slate-500 font-mono text-xs">{b.id}</td>
                  <td className="px-3 py-2 text-slate-200 font-mono">{b.cidr}</td>
                  <td className="px-3 py-2 text-slate-400">{b.note || '-'}</td>
                  <td className="px-3 py-2 text-right">
                    <button className="bg-red-600 hover:bg-red-500 text-white rounded-md px-2 py-1 text-xs inline-flex items-center gap-1" onClick={() => remove(b.id)}>
                      <Trash2 size={12} /> 删除
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}
