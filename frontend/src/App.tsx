import { HashRouter, useLocation, Navigate, Routes, Route } from 'react-router-dom'
import { ToastProvider } from './components/Toast'
import { ConfirmProvider } from './components/ConfirmDialog'
import Layout from './components/Layout'
import Batch from './pages/Batch'
import Credentials from './pages/Credentials'
import Templates from './pages/Templates'
import BlackSeg from './pages/BlackSeg'
import Resources from './pages/Resources'
import Export from './pages/Export'
import LiveLog from './pages/LiveLog'
import Personas from './pages/Personas'
import Extract from './pages/Extract'
import GCPMonitor from './pages/GCPMonitor'
import ServerStatus from './pages/ServerStatus'

// keepAlive：所有页面一次性挂载，通过 display 切换可见性，tab 切换时组件不卸载，state 不丢。
const PAGES: { path: string; el: JSX.Element }[] = [
  { path: '/batch',       el: <Batch /> },
  { path: '/resources',   el: <Resources /> },
  { path: '/server-status', el: <ServerStatus /> },
  { path: '/gcp-monitor', el: <GCPMonitor /> },
  { path: '/export',      el: <Export /> },
  { path: '/credentials', el: <Credentials /> },
  { path: '/templates',   el: <Templates /> },
  { path: '/blackseg',    el: <BlackSeg /> },
  { path: '/personas',    el: <Personas /> },
  { path: '/log',         el: <LiveLog /> },
  { path: '/extract',     el: <Extract /> },
]

function KeepAlivePages() {
  const { pathname } = useLocation()
  // 默认页：根路径 `/` 或任何未匹配的 path 都显示 /batch，避免首次加载黑屏
  const effectivePath = pathname === '/' || !PAGES.some(p => p.path === pathname) ? '/batch' : pathname
  return (
    <>
      {PAGES.map(p => (
        <div key={p.path} style={{ display: effectivePath === p.path ? 'block' : 'none', height: '100%' }}>
          {p.el}
        </div>
      ))}
    </>
  )
}

export default function App() {
  return (
    <ToastProvider>
      <ConfirmProvider>
        <HashRouter>
          <Routes>
            <Route path="/" element={<Layout keepAlive={<KeepAlivePages />} />}>
              <Route index element={<Navigate to="/batch" replace />} />
              {/* 路径仅用于触发 Layout 的 NavLink 激活态，实际页面由 KeepAlivePages 统一渲染 */}
              {PAGES.map(p => <Route key={p.path} path={p.path.slice(1)} element={null} />)}
            </Route>
          </Routes>
        </HashRouter>
      </ConfirmProvider>
    </ToastProvider>
  )
}
