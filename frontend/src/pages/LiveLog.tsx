import { useEffect, useState, useRef } from 'react'

interface LogEntry {
  level: string
  msg: string
}

// Wails v2 在运行时会注入 window.runtime，含 EventsOn / EventsEmit 等
declare global {
  interface Window {
    runtime?: {
      EventsOn: (name: string, cb: (data: any) => void) => () => void
      EventsOff: (name: string) => void
    }
  }
}

export default function LiveLog() {
  const [entries, setEntries] = useState<LogEntry[]>([])
  const endRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const rt = window.runtime
    if (!rt) return
    const off = rt.EventsOn('log:entry', (data: LogEntry) => {
      setEntries(prev => [...prev.slice(-999), data])
    })
    return () => { off && off() }
  }, [])

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [entries])

  const colorFor = (level: string) => {
    if (level === 'ERROR' || level === 'FATAL') return 'text-red-400'
    if (level === 'WARN') return 'text-amber-400'
    if (level === 'DEBUG') return 'text-slate-500'
    return 'text-slate-300'
  }

  return (
    <div className="p-6 h-full flex flex-col">
      <h1 className="text-xl font-bold text-slate-100 mb-4 shrink-0">实时日志</h1>
      <div className="flex-1 bg-[#1a1d27] rounded-lg border border-slate-700/50 overflow-auto p-3">
        <pre className="log-pre">
          {entries.map((e, i) => (
            <div key={i} className={colorFor(e.level)}>
              [{e.level}] {e.msg}
            </div>
          ))}
          <div ref={endRef} />
        </pre>
      </div>
    </div>
  )
}
