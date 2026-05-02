import { NavLink } from 'react-router-dom'
import { useEffect, useState, type ReactNode } from 'react'
import {
  Rocket,
  Server,
  Download,
  KeyRound,
  LayoutGrid,
  Ban,
  ScrollText,
  Cloud,
  UserCircle2,
  Inbox,
  BarChart3,
  Cable,
} from 'lucide-react'
import { GetVersion } from '../../wailsjs/go/main/App'

const navItems = [
  { to: '/batch',       icon: Rocket,      label: '批量开机' },
  { to: '/resources',   icon: Server,      label: '资源清单' },
  { to: '/server-status', icon: Cable,      label: '连接服务器' },
  { to: '/gcp-monitor', icon: BarChart3,   label: 'GCP 监控' },
  { to: '/export',      icon: Download,    label: 'SMTP 导出' },
  { to: '/extract',     icon: Inbox,       label: '邮箱提取' },
  { to: '/credentials', icon: KeyRound,    label: '凭证管理' },
  { to: '/templates',   icon: LayoutGrid,  label: '开机模板' },
  { to: '/blackseg',    icon: Ban,         label: '黑段库' },
  { to: '/personas',    icon: UserCircle2, label: 'Persona 库' },
  { to: '/log',         icon: ScrollText,  label: '实时日志' },
]

export default function Layout({ keepAlive }: { keepAlive?: ReactNode }) {
  const [version, setVersion] = useState('')

  useEffect(() => {
    GetVersion().then(v => setVersion(v || '')).catch(() => setVersion(''))
  }, [])

  return (
    <div className="flex h-screen bg-[#0f1117] text-slate-200 overflow-hidden">
      <aside className="w-52 flex flex-col bg-[#1a1d27] border-r border-slate-700/50 shrink-0">
        <div className="h-14 flex items-center gap-2.5 px-4 border-b border-slate-700/50">
          <div className="w-8 h-8 rounded-lg bg-gradient-to-br from-indigo-500 to-purple-600 flex items-center justify-center">
            <Cloud size={16} className="text-white" />
          </div>
          <span className="font-bold text-transparent bg-clip-text bg-gradient-to-r from-indigo-400 to-purple-400">
            GCP MailNode
          </span>
        </div>

        <nav className="flex-1 py-3 px-2 space-y-0.5">
          {navItems.map(({ to, icon: Icon, label }) => (
            <NavLink
              key={to}
              to={to}
              className={({ isActive }) =>
                `flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm transition-colors ` +
                (isActive
                  ? 'bg-indigo-500/20 text-indigo-300 font-medium border border-indigo-500/30'
                  : 'text-slate-400 hover:bg-slate-700/40 hover:text-slate-200')
              }
            >
              <Icon size={17} />
              {label}
            </NavLink>
          ))}
        </nav>

        <div className="p-3 text-xs text-slate-500 text-center border-t border-slate-700/50">
          {version ? `v${version}` : ''}
        </div>
      </aside>

      <main className="flex-1 overflow-auto bg-[#0f1117]">
        {keepAlive}
      </main>
    </div>
  )
}
