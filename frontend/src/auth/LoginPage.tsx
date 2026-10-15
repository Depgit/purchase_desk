// Sign in with email, mobile or employee ID, using a password or a one-time code.
import { useState } from 'react'
import { ApiError, post } from '../api/client'
import { useSession } from './session'
import { Errors } from '../ui/Errors'
import { SignInDoor } from './SignInDoor'

export function LoginPage() {
  const { signIn, signInWithCode } = useSession()
  const [mode, setMode] = useState<'password' | 'code'>('password')
  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [sent, setSent] = useState(false)
  const [hint, setHint] = useState('')
  const [errors, setErrors] = useState<string[]>([])
  const [busy, setBusy] = useState(false)

  async function run(fn: () => Promise<void>) {
    setBusy(true); setErrors([])
    try { await fn() } catch (e) { setErrors(e instanceof ApiError ? e.errors : [String(e)]) } finally { setBusy(false) }
  }
  return (
    <div className="login">
      <form className="card" onSubmit={(e) => {
        e.preventDefault()
        if (mode === 'password') run(() => signIn(login, password))
        else if (!sent) run(async () => {
          const r = await post<{ dev_code?: string }>('/auth/otp/request', { login })
          setSent(true)
          setHint(r.dev_code ? `Development mode: your code is ${r.dev_code}` : 'If this login exists, a code has been sent.')
        })
        else run(() => signInWithCode(login, code))
      }}>
        <SignInDoor on="staff" />
        <div><h1>Purchase Desk</h1><p className="sub">Saltstayz staff sign-in</p></div>
        <div className="seg" role="group" aria-label="Sign-in method">
          <button type="button" className={mode === 'password' ? 'on' : ''} onClick={() => { setMode('password'); setErrors([]) }}>Password</button>
          <button type="button" className={mode === 'code' ? 'on' : ''} onClick={() => { setMode('code'); setErrors([]) }}>One-time code</button>
        </div>
        <label className="field"><span>Email, mobile number or employee ID</span>
          <input value={login} onChange={(e) => setLogin(e.target.value)} autoComplete="username" required autoFocus /></label>
        {mode === 'password' ? (
          <label className="field"><span>Password</span>
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required /></label>
        ) : sent ? (
          <label className="field"><span>Code</span>
            <input value={code} onChange={(e) => setCode(e.target.value)} inputMode="numeric" maxLength={6} autoComplete="one-time-code" required />
            <span className="hint">{hint}</span></label>
        ) : null}
        <Errors list={errors} />
        <button className="btn" disabled={busy}>{mode === 'code' && !sent ? 'Send code' : 'Sign in'}</button>
      </form>
    </div>
  )
}
