import { createContext, useCallback, useContext, useState, ReactNode } from 'react'
import './primitives.css'

// A minimal toast system replacing the app's dismissable error banner and the
// several places errors were swallowed. Errors surface here as a live region so
// they are announced to assistive tech.

type ToastKind = 'error' | 'success'
interface Toast {
  id: number
  kind: ToastKind
  message: string
}

interface ToastApi {
  error: (message: string) => void
  success: (message: string) => void
}

const ToastContext = createContext<ToastApi | null>(null)

let nextId = 1

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])

  const dismiss = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id))
  }, [])

  const push = useCallback((kind: ToastKind, message: string) => {
    const id = nextId++
    setToasts((prev) => [...prev, { id, kind, message }])
    // Auto-dismiss after a readable interval.
    setTimeout(() => dismiss(id), kind === 'error' ? 8000 : 4000)
  }, [dismiss])

  const api: ToastApi = {
    error: (m) => push('error', m),
    success: (m) => push('success', m),
  }

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="ui-toast-region" role="status" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={`ui-toast ui-toast-${t.kind}`}>
            <span>{t.message}</span>
            <button className="ui-toast-close" onClick={() => dismiss(t.id)} aria-label="Dismiss">×</button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast must be used within a ToastProvider')
  return ctx
}
