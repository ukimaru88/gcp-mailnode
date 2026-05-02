import { useState } from 'react'
import { Download, Copy } from 'lucide-react'
import { ExportSMTP } from '../../wailsjs/go/main/App'
import { useToast } from '../components/Toast'

type Format = 'smtp' | 'smtp_v2' | 'smtp_v3' | 'toolkit'

export default function Export() {
  const { toast } = useToast()
  const [format, setFormat] = useState<Format>('smtp_v3')
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)

  const generate = async () => {
    setBusy(true)
    try {
      const r = await ExportSMTP(format)
      setText(r || '')
      const lines = (r || '').split('\n').filter(Boolean).length
      toast('success', `已导出 ${lines} 条记录`)
    } catch (e: any) { toast('error', '导出失败: ' + (e?.message || e)) }
    finally { setBusy(false) }
  }

  const copyAll = async () => {
    if (!text) { toast('warning', '先点击生成导出'); return }
    try {
      await navigator.clipboard.writeText(text)
      toast('success', '已复制到剪贴板')
    } catch (e: any) { toast('error', '复制失败: ' + (e?.message || e)) }
  }

  return (
    <div className="p-6 h-full overflow-auto">
      <h1 className="text-xl font-bold text-slate-100 mb-4">SMTP 账号导出</h1>

      <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-4 mb-4">
        <div className="text-sm text-slate-300 mb-2">选择格式：</div>
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-2">
          <button onClick={() => setFormat('smtp')}
                  className={`text-left px-3 py-3 rounded-md border transition-colors ${
                    format === 'smtp'
                      ? 'bg-indigo-500/20 border-indigo-500/50 text-indigo-200'
                      : 'bg-slate-900 border-slate-700 text-slate-400 hover:border-slate-600'
                  }`}>
            <div className="font-medium text-sm">SMTP 账号池</div>
            <div className="text-xs text-slate-500 mt-1 font-mono">host:port:user:password</div>
          </button>
          <button onClick={() => setFormat('smtp_v2')}
                  className={`text-left px-3 py-3 rounded-md border transition-colors ${
                    format === 'smtp_v2'
                      ? 'bg-indigo-500/20 border-indigo-500/50 text-indigo-200'
                      : 'bg-slate-900 border-slate-700 text-slate-400 hover:border-slate-600'
                  }`}>
            <div className="font-medium text-sm">SMTP v2</div>
            <div className="text-xs text-slate-500 mt-1 font-mono">host:port:user:pass:persona:hide</div>
            <div className="text-[11px] text-slate-500 mt-1">
              多 NIC 机器仍导出 1 条 SMTP 入口；mail1~mail8 / IP 出口由服务器内部轮换。
            </div>
          </button>
          <button onClick={() => setFormat('smtp_v3')}
                  className={`text-left px-3 py-3 rounded-md border transition-colors ${
                    format === 'smtp_v3'
                      ? 'bg-indigo-500/20 border-indigo-500/50 text-indigo-200'
                      : 'bg-slate-900 border-slate-700 text-slate-400 hover:border-slate-600'
                  }`}>
            <div className="font-medium text-sm">⭐ SMTP v3（含一键退订）</div>
            <div className="text-xs text-slate-500 mt-1 font-mono">...:persona:hide:unsub_url:secret</div>
            <div className="text-[11px] text-slate-500 mt-1">
              v0.1.74+ 推荐。携带退订 URL + HMAC 密钥，brutal-mailer 自动写 List-Unsubscribe 头与 DKIM 签名，符合 Google/Yahoo 2024 一键退订要求。
            </div>
          </button>
          <button onClick={() => setFormat('toolkit')}
                  className={`text-left px-3 py-3 rounded-md border transition-colors ${
                    format === 'toolkit'
                      ? 'bg-indigo-500/20 border-indigo-500/50 text-indigo-200'
                      : 'bg-slate-900 border-slate-700 text-slate-400 hover:border-slate-600'
                  }`}>
            <div className="font-medium text-sm">mail-toolkit 格式</div>
            <div className="text-xs text-slate-500 mt-1 font-mono">fqdn----ip----root----password</div>
          </button>
        </div>

        <div className="flex gap-2 mt-3">
          <button onClick={generate} disabled={busy}
                  className="bg-indigo-600 hover:bg-indigo-500 disabled:opacity-40 text-white rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
            <Download size={14} /> 生成导出
          </button>
          <button onClick={copyAll} disabled={!text}
                  className="bg-slate-700 hover:bg-slate-600 disabled:opacity-40 text-slate-200 rounded-md px-3 py-1.5 text-sm inline-flex items-center gap-1.5">
            <Copy size={14} /> 复制到剪贴板
          </button>
        </div>

        <div className="text-xs text-slate-500 mt-3">
          列出 deploy_status = success / mta_ready 的机器；导出条数按 VPS 入口统计，不按绑定 IP 数统计。
        </div>
      </div>

      <div className="bg-[#1a1d27] rounded-lg border border-slate-700/50 p-3">
        <textarea readOnly value={text}
                  placeholder="点击“生成导出”后内容显示在这里..."
                  className="bg-slate-900 border border-slate-700 text-slate-100 rounded-md px-3 py-2 text-sm w-full h-[60vh] font-mono resize-none focus:border-indigo-500 outline-none" />
      </div>
    </div>
  )
}
