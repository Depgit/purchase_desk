// A short message at the bottom of the screen after an action.
import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'

const Ctx = createContext<(msg: string, bad?: boolean) => void>(() => {})
export const useToast = () => useContext(Ctx)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [msg, setMsg] = useState<{ text: string; bad: boolean } | null>(null)
  const timer = useRef<number>(0)
  const show = useCallback((text: string, bad = false) => {
    setMsg({ text, bad })
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setMsg(null), bad ? 6000 : 3500)
  }, [])
  return (
    <Ctx.Provider value={show}>
      {children}
      {msg && <div className={'toast' + (msg.bad ? ' bad' : '')} role="status">{msg.text}</div>}
    </Ctx.Provider>
  )
}
