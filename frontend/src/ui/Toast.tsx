// A short message at the bottom of the screen after an action, or a popup to acknowledge when the action changed
// something the person must not miss (a PO that is now closed).
import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'
import { Modal } from './Modal'

const Ctx = createContext<(msg: string, bad?: boolean) => void>(() => {})
export const useToast = () => useContext(Ctx)

const PopupCtx = createContext<(title: string, text: string) => void>(() => {})
/** Shows a popup with an OK button; it stays until the person closes it, even if the page under it changes. */
export const usePopup = () => useContext(PopupCtx)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [msg, setMsg] = useState<{ text: string; bad: boolean } | null>(null)
  const [popup, setPopup] = useState<{ title: string; text: string } | null>(null)
  const timer = useRef<number>(0)
  const show = useCallback((text: string, bad = false) => {
    setMsg({ text, bad })
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setMsg(null), bad ? 6000 : 3500)
  }, [])
  const open = useCallback((title: string, text: string) => setPopup({ title, text }), [])
  const close = useCallback(() => setPopup(null), [])
  return (
    <Ctx.Provider value={show}>
      <PopupCtx.Provider value={open}>
        {children}
        {msg && <div className={'toast' + (msg.bad ? ' bad' : '')} role="status">{msg.text}</div>}
        {popup && <Modal title={popup.title} onClose={close}>
          <p style={{ whiteSpace: 'pre-line' }}>{popup.text}</p>
          <div className="btns"><button className="btn" autoFocus onClick={close}>OK</button></div>
        </Modal>}
      </PopupCtx.Provider>
    </Ctx.Provider>
  )
}
