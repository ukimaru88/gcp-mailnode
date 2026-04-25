import { useEffect, useState } from 'react'
import { Plus, TestTube2, Trash2, X, KeyRound, Cloud } from 'lucide-react'
import {
  AddGCPCredentialADC,
  AddGCPCredentialGcloud,
  AddGCPCredentialOAuth,
  AddGCPCredentialServiceAccount,
  CheckGCPADC,
  ListGCPCredentials,
  TestGCPCredential,
  DeleteGCPCredential,
  SetGCPCredentialEnabled,
  AddAliyunCredential,
  ListAliyunCredentials,
  TestAliyunCredential,
  DeleteAliyunCredential,
  SetAliyunCredentialEnabled,
} from '../../wailsjs/go/main/App'
import { main } from '../../wailsjs/go/models'
import { useToast } from '../components/Toast'
import { useConfirm } from '../components/ConfirmDialog'

type ModalKind = null | 'adc' | 'sa' | 'oauth' | 'gcloud' | 'aliyun'

const authTypeLabel = (t: string): string => {
  switch (t) {
    case 'service_account': return '服务账号'
    case 'oauth': return 'OAuth'
    case 'gcloud': return 'gcloud CLI'
    case 'adc': return 'ADC（推荐）'
    default: return t
  }
}

const fmtDate = (v: any): string => {
  if (!v) return '-'
  try { return new Date(v as string).toLocaleString('zh-CN') } catch { return '-' }
}

export default function Credentials() {
  const { toast } = useToast()
  const confirmDlg = useConfirm()
  const [gcpList, setGcpList] = useState<main.GCPCredentialDTO[]>([])
  const [aliList, setAliList] = useState<main.AliyunCredentialDTO[]>([])
  const [modal, setModal] = useState<ModalKind>(null)
  const [busy, setBusy] = useState(false)

  // form state
  const [name, setName] = useState('')
  const [projectID, setProjectID] = useState('')
  const [saJSON, setSaJSON] = useState('')
  const [ak, setAk] = useState('')
  const [sk, setSk] = useState('')

  const refresh = async () => {
    try {
      const [g, a] = await Promise.all([ListGCPCredentials(), ListAliyunCredentials()])
      setGcpList(g || [])
      setAliList(a || [])
    } catch (e: any) {
      toast('error', '加载失败: ' + (e?.message || e))
    }
  }

  useEffect(() => { refresh() }, [])

  const resetForm = () => {
    setName(''); setProjectID(''); setSaJSON(''); setAk(''); setSk('')
  }

  const openModal = async (k: Exclude<ModalKind, null>) => {
    resetForm()
    if (k === 'adc') {
      try {
        const p = await CheckGCPADC()
        setProjectID(p || '')
        setModal('adc')
      } catch (e: any) {
        toast('error', 'ADC 检查失败: ' + (e?.message || e))
        return
      }
    } else {
      setModal(k)
    }
  }

  const submitGCP = async () => {
    if (!name.trim()) { toast('warning', '请输入名称'); return }
    setBusy(true)
    try {
      if (modal === 'adc') {
        if (!projectID.trim()) { toast('warning', '请输入 projectID'); setBusy(false); return }
        await AddGCPCredentialADC(name.trim(), projectID.trim())
      } else if (modal === 'sa') {
        if (!saJSON.trim()) { toast('warning', '请粘贴 Service Account JSON'); setBusy(false); return }
        await AddGCPCredentialServiceAccount(name.trim(), saJSON)
      } else if (modal === 'oauth') {
        if (!projectID.trim()) { toast('warning', '请输入 projectID'); setBusy(false); return }
        toast('success', '请在浏览器中完成授权...')
        await AddGCPCredentialOAuth(name.trim(), projectID.trim())
      } else if (modal === 'gcloud') {
        await AddGCPCredentialGcloud(name.trim())
      }
      toast('success', '已添加')
      setModal(null); resetForm()
      await refresh()
    } catch (e: any) {
      toast('error', '添加失败: ' + (e?.message || e))
    } finally { setBusy(false) }
  }

  const submitAliyun = async () => {
    if (!name.trim() || !ak.trim() || !sk.trim()) { toast('warning', '请填写完整'); return }
    setBusy(true)
    try {
      await AddAliyunCredential(name.trim(), ak.trim(), sk.trim())
      toast('success', '已添加')
      setModal(null); resetForm()
      await refresh()
    } catch (e: any) {
      toast('error', '添加失败: ' + (e?.message || e))
    } finally { setBusy(false) }
  }

  const testGCP = async (id: string) => {
    try {
      const r = await TestGCPCredential(id)
      toast('success', '测试通过: ' + r)
    } catch (e: any) {
      toast('error', '测试失败: ' + (e?.message || e))
    }
  }

  const testAli = async (id: string) => {
    try {
      const r = await TestAliyunCredential(id)
      toast('success', '测试通过: ' + r)
    } catch (e: any) {
      toast('error', '测试失败: ' + (e?.message || e))
    }
  }

  const delGCP = async (id: string) => {
    if (!await confirmDlg({ message: '确认删除该 GCP 凭证？', danger: true })) return
    try { await DeleteGCPCredential(id); toast('success', '已删除'); await refresh() }
    catch (e: any) { toast('error', '删除失败: ' + (e?.message || e)) }
  }

  const delAli = async (id: string) => {
    if (!await confirmDlg({ message: '确认删除该阿里云凭证？', danger: true })) return
    try { await DeleteAliyunCredential(id); toast('success', '已删除'); await refresh() }
    catch (e: any) { toast('error', '删除失败: ' + (e?.message || e)) }
  }

  const toggleGCP = async (id: string, v: boolean) => {
    try { await SetGCPCredentialEnabled(id, v); await refresh() }
    catch (e: any) { toast('error', '切换失败: ' + (e?.message || e)) }
  }

  const toggleAli = async (id: string, v: boolean) => {
    try { await SetAliyunCredentialEnabled(id, v); await refresh() }
    catch (e: any) { toast('error', '切换失败: ' + (e?.message || e)) }
  }

  return (
    <div className="p-6 h-full overflow-auto">
      <h1 className="text-xl font-bold text-slate-100 mb-4">凭证管理</h1>

      <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
        {/* GCP */}
        <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4">
          <div className="flex items-center gap-2 mb-3">
            <KeyRound size={16} className="text-indigo-400" />
            <h2 className="font-semibold text-slate-100">GCP 凭证</h2>
          </div>
          <div className="flex flex-wrap gap-2 mb-3">
            <button className="bg-indigo-600 hover:bg-indigo-500 text-white rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5" onClick={() => openModal('adc')}>
              <Plus size={14} /> 添加 ADC
            </button>
            <button className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5" onClick={() => openModal('sa')}>
              <Plus size={14} /> 添加 Service Account JSON
            </button>
            <button className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5" onClick={() => openModal('oauth')}>
              <Plus size={14} /> 添加 OAuth 登录
            </button>
            <button className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5" onClick={() => openModal('gcloud')}>
              <Plus size={14} /> 添加 gcloud CLI
            </button>
          </div>

          <div className="overflow-x-auto rounded-md border border-slate-700/50">
            <table className="w-full text-sm">
              <thead className="bg-slate-900/50 text-slate-400">
                <tr>
                  <th className="text-left px-3 py-2 font-medium">名称</th>
                  <th className="text-left px-3 py-2 font-medium">认证类型</th>
                  <th className="text-left px-3 py-2 font-medium">project_id</th>
                  <th className="text-left px-3 py-2 font-medium">启用</th>
                  <th className="text-left px-3 py-2 font-medium">创建时间</th>
                  <th className="text-right px-3 py-2 font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {gcpList.length === 0 && (
                  <tr><td colSpan={6} className="text-center px-3 py-6 text-slate-500">暂无凭证</td></tr>
                )}
                {gcpList.map(c => (
                  <tr key={c.id} className="border-t border-slate-700/40 hover:bg-slate-800/50">
                    <td className="px-3 py-2 text-slate-200">{c.name}</td>
                    <td className="px-3 py-2 text-slate-300">{authTypeLabel(c.auth_type)}</td>
                    <td className="px-3 py-2 text-slate-300 font-mono text-xs">{c.project_id || '-'}</td>
                    <td className="px-3 py-2">
                      <label className="inline-flex items-center cursor-pointer">
                        <input type="checkbox" className="sr-only peer" checked={c.enabled} onChange={e => toggleGCP(c.id, e.target.checked)} />
                        <div className="w-9 h-5 bg-slate-700 rounded-full peer peer-checked:bg-indigo-600 relative transition-colors">
                          <div className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${c.enabled ? 'translate-x-4' : ''}`} />
                        </div>
                      </label>
                    </td>
                    <td className="px-3 py-2 text-slate-400 text-xs">{fmtDate(c.created_at)}</td>
                    <td className="px-3 py-2 text-right">
                      <div className="inline-flex gap-1.5">
                        <button className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-2 py-1 text-xs inline-flex items-center gap-1" onClick={() => testGCP(c.id)}>
                          <TestTube2 size={12} /> 测试
                        </button>
                        <button className="bg-red-600 hover:bg-red-500 text-white rounded-md px-2 py-1 text-xs inline-flex items-center gap-1" onClick={() => delGCP(c.id)}>
                          <Trash2 size={12} /> 删除
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

        {/* Aliyun */}
        <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4">
          <div className="flex items-center gap-2 mb-3">
            <Cloud size={16} className="text-orange-400" />
            <h2 className="font-semibold text-slate-100">阿里云凭证</h2>
          </div>
          <div className="flex flex-wrap gap-2 mb-3">
            <button className="bg-indigo-600 hover:bg-indigo-500 text-white rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5" onClick={() => openModal('aliyun')}>
              <Plus size={14} /> 添加
            </button>
          </div>

          <div className="overflow-x-auto rounded-md border border-slate-700/50">
            <table className="w-full text-sm">
              <thead className="bg-slate-900/50 text-slate-400">
                <tr>
                  <th className="text-left px-3 py-2 font-medium">名称</th>
                  <th className="text-left px-3 py-2 font-medium">AccessKey ID</th>
                  <th className="text-left px-3 py-2 font-medium">启用</th>
                  <th className="text-left px-3 py-2 font-medium">创建时间</th>
                  <th className="text-right px-3 py-2 font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {aliList.length === 0 && (
                  <tr><td colSpan={5} className="text-center px-3 py-6 text-slate-500">暂无凭证</td></tr>
                )}
                {aliList.map(c => (
                  <tr key={c.id} className="border-t border-slate-700/40 hover:bg-slate-800/50">
                    <td className="px-3 py-2 text-slate-200">{c.name}</td>
                    <td className="px-3 py-2 text-slate-300 font-mono text-xs">{c.access_key_id}</td>
                    <td className="px-3 py-2">
                      <label className="inline-flex items-center cursor-pointer">
                        <input type="checkbox" className="sr-only peer" checked={c.enabled} onChange={e => toggleAli(c.id, e.target.checked)} />
                        <div className="w-9 h-5 bg-slate-700 rounded-full peer peer-checked:bg-indigo-600 relative transition-colors">
                          <div className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform ${c.enabled ? 'translate-x-4' : ''}`} />
                        </div>
                      </label>
                    </td>
                    <td className="px-3 py-2 text-slate-400 text-xs">{fmtDate(c.created_at)}</td>
                    <td className="px-3 py-2 text-right">
                      <div className="inline-flex gap-1.5">
                        <button className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-2 py-1 text-xs inline-flex items-center gap-1" onClick={() => testAli(c.id)}>
                          <TestTube2 size={12} /> 测试
                        </button>
                        <button className="bg-red-600 hover:bg-red-500 text-white rounded-md px-2 py-1 text-xs inline-flex items-center gap-1" onClick={() => delAli(c.id)}>
                          <Trash2 size={12} /> 删除
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      {/* Modal */}
      {modal && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50" onClick={() => !busy && setModal(null)}>
          <div className="bg-[#1a1d27] border border-slate-700 rounded-lg p-6 w-[480px] max-w-[90vw]" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-4">
              <h3 className="font-semibold text-slate-100">
                {modal === 'adc' && '添加 ADC 凭证'}
                {modal === 'sa' && '添加 Service Account JSON'}
                {modal === 'oauth' && '添加 OAuth 登录凭证'}
                {modal === 'gcloud' && '添加 gcloud CLI 凭证'}
                {modal === 'aliyun' && '添加阿里云凭证'}
              </h3>
              <button className="text-slate-500 hover:text-slate-300" onClick={() => !busy && setModal(null)}>
                <X size={16} />
              </button>
            </div>

            <div className="space-y-3">
              <div>
                <label className="block text-xs text-slate-400 mb-1">名称</label>
                <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none" value={name} onChange={e => setName(e.target.value)} />
              </div>

              {(modal === 'adc' || modal === 'oauth') && (
                <div>
                  <label className="block text-xs text-slate-400 mb-1">Project ID</label>
                  <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm focus:border-indigo-500 outline-none font-mono" value={projectID} onChange={e => setProjectID(e.target.value)} />
                  {modal === 'adc' && <p className="text-xs text-slate-500 mt-1">自动检测本机 ADC；若为空请手动输入</p>}
                </div>
              )}

              {modal === 'sa' && (
                <div>
                  <label className="block text-xs text-slate-400 mb-1">Service Account JSON</label>
                  <textarea className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-xs font-mono focus:border-indigo-500 outline-none" rows={10} value={saJSON} onChange={e => setSaJSON(e.target.value)} placeholder='{"type":"service_account",...}' />
                </div>
              )}

              {modal === 'oauth' && (
                <p className="text-xs text-amber-300">点击确认后将打开浏览器进行授权，完成后会自动返回。</p>
              )}

              {modal === 'aliyun' && (
                <>
                  <div>
                    <label className="block text-xs text-slate-400 mb-1">AccessKey ID</label>
                    <input className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none" value={ak} onChange={e => setAk(e.target.value)} />
                  </div>
                  <div>
                    <label className="block text-xs text-slate-400 mb-1">AccessKey Secret</label>
                    <input type="password" className="w-full bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-2 py-1.5 text-sm font-mono focus:border-indigo-500 outline-none" value={sk} onChange={e => setSk(e.target.value)} />
                  </div>
                </>
              )}
            </div>

            <div className="flex justify-end gap-2 mt-5">
              <button className="bg-slate-700 hover:bg-slate-600 text-slate-200 rounded-md px-3 py-1.5 text-sm" disabled={busy} onClick={() => setModal(null)}>取消</button>
              <button className="bg-indigo-600 hover:bg-indigo-500 disabled:opacity-50 disabled:cursor-not-allowed text-white rounded-md px-3 py-1.5 text-sm" disabled={busy} onClick={() => modal === 'aliyun' ? submitAliyun() : submitGCP()}>
                {busy ? '处理中...' : '确认'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
