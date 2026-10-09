// Holds the signed-in user for the whole app, and answers "may this user do X?" for showing buttons.
// The server checks every right again; this only decides what to show.
import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { get, getToken, post, setToken } from '../api/client'
import type { Me } from '../api/types'
import { refreshLookups, setBillsOpen } from '../api/lookups'

interface Session {
  me: Me | null
  ready: boolean
  signIn: (login: string, password: string) => Promise<void>
  signInWithCode: (login: string, code: string) => Promise<void>
  signOut: () => void
  hasRight: (right: string) => boolean
  hasStep: (...steps: string[]) => boolean
}

const Ctx = createContext<Session>(null as unknown as Session)
export const useSession = () => useContext(Ctx)

export function SessionProvider({ children }: { children: ReactNode }) {
  const [me, setMeState] = useState<Me | null>(null)
  const setMe = useCallback((m: Me | null) => { if (m) setBillsOpen((m.phase || 5) >= 3); setMeState(m) }, [])
  const [ready, setReady] = useState(false)
  useEffect(() => {
    const out = () => setMe(null)
    window.addEventListener('pd-signed-out', out)
    if (getToken()) get<Me>('/me').then(setMe).catch(() => setMe(null)).finally(() => setReady(true))
    else setReady(true)
    // Roles, property scope and master lists change while people stay signed in. Read them again when the window
    // comes back into focus (at most once every 30 seconds), so a change made by the Admin shows without a reload.
    let last = Date.now()
    const fresh = () => {
      if (!getToken() || document.visibilityState === 'hidden' || Date.now() - last < 30000) return
      last = Date.now()
      get<Me>('/me').then(setMe).catch(() => {})
      refreshLookups()
    }
    window.addEventListener('focus', fresh)
    document.addEventListener('visibilitychange', fresh)
    return () => { window.removeEventListener('pd-signed-out', out); window.removeEventListener('focus', fresh); document.removeEventListener('visibilitychange', fresh) }
  }, [])
  const finish = (r: { token: string; user: Me }) => { setToken(r.token); setMe(r.user); refreshLookups() } // a new sign-in never sees the last user's lists
  const signIn = useCallback(async (login: string, password: string) => finish(await post('/auth/login', { login, password })), [])
  const signInWithCode = useCallback(async (login: string, code: string) => finish(await post('/auth/otp/verify', { login, code })), [])
  const signOut = useCallback(() => { setToken(''); setMe(null) }, [])
  const hasRight = useCallback((r: string) => !!me && (me.is_admin || me.roles.some((g) => g.rights.includes(r))), [me])
  const hasStep = useCallback((...s: string[]) => !!me && (me.is_admin || me.roles.some((g) => s.includes(g.step))), [me])
  return <Ctx.Provider value={{ me, ready, signIn, signInWithCode, signOut, hasRight, hasStep }}>{children}</Ctx.Provider>
}
