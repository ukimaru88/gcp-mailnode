import { createContext, useContext, useState, useCallback, useEffect, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

export type ConfirmOpts = string | {
  title?: string
  message: string
  confirmText?: string
  cancelText?: string
  danger?: boolean
}
export type ConfirmFn = (opts: ConfirmOpts) => Promise<boolean>

interface State {
  opts: { title?: string; message: string; confirmText?: string; cancelText?: string; danger?: boolean }
  resolve: (v: boolean) => void
}

const ConfirmCtx = createContext<ConfirmFn | null>(null)

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<State | null>(null)

  const confirm: ConfirmFn = useCallback(opts => {
    const norm = typeof opts === 'string' ? { message: opts } : opts
    return new Promise<boolean>(resolve => setState({ opts: norm, resolve }))
  }, [])

  const close = useCallback((v: boolean) => {
    setState(s => { if (s) s.resolve(v); return null })
  }, [])

  useEffect(() => {
    if (!state) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') close(false) }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [state, close])

  return (
    <ConfirmCtx.Provider value={confirm}>
      {children}
      {state && createPortal(
        <div className="fixed inset-0 z-[1500] bg-black/50 backdrop-blur-sm flex items-center justify-center">
          <div
            className="w-[400px] max-w-[92vw] bg-slate-900 border border-slate-700 rounded-lg shadow-2xl"
            onClick={e => e.stopPropagation()}
          >
            {state.opts.title && (
              <div className="px-5 py-3 border-b border-slate-700 text-sm font-semibold text-slate-200">
                {state.opts.title}
              </div>
            )}
            <div className="px-5 py-4 text-sm text-slate-300 whitespace-pre-wrap leading-relaxed">
              {state.opts.message}
            </div>
            <div className="px-5 py-3 border-t border-slate-700 flex justify-end gap-2">
              <button
                onClick={() => close(false)}
                className="px-3 py-1.5 text-sm text-slate-300 bg-slate-800 hover:bg-slate-700 border border-slate-700 rounded"
              >
                {state.opts.cancelText ?? '取消'}
              </button>
              <button
                onClick={() => close(true)}
                className={`px-3 py-1.5 text-sm text-white rounded ${
                  state.opts.danger
                    ? 'bg-red-600 hover:bg-red-500'
                    : 'bg-indigo-600 hover:bg-indigo-500'
                }`}
              >
                {state.opts.confirmText ?? '确认'}
              </button>
            </div>
          </div>
        </div>,
        document.body
      )}
    </ConfirmCtx.Provider>
  )
}

export function useConfirm(): ConfirmFn {
  const ctx = useContext(ConfirmCtx)
  if (!ctx) throw new Error('useConfirm must be used inside ConfirmProvider')
  return ctx
}
