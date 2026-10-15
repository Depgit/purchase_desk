// The vendor portal at /vendor: a vendor signs in with the email and one-time password the Purchase Team gave, sets
// its own password, then works its purchase orders and follows its payments. It shares nothing with the staff app.
import { createContext, useContext, useEffect, useState, type FormEvent } from 'react'
import { NavLink, Navigate, Route, Routes } from 'react-router-dom'
import { ApiError } from '../api/client'
import { Errors } from '../ui/Errors'
import { SignInDoor } from '../auth/SignInDoor'
import { useToast } from '../ui/Toast'
import { setVendorToken, vendorToken, vget, vpost } from './portalApi'
import { VendorOrderPage, VendorOrdersPage } from './VendorOrders'
import { VendorPaymentsPage } from './VendorPayments'
import { VendorDashboard } from './VendorDashboard'

export interface Vendor { id: number; code: string; name: string; email: string; must_change: boolean }

const Ctx = createContext<{ vendor: Vendor; setVendor: (v: Vendor) => void } | null>(null)
export const useVendor = () => useContext(Ctx)!

export function VendorPortal() {
  const [vendor, setVendor] = useState<Vendor | null>(null)
  const [ready, setReady] = useState(false)
  useEffect(() => {
    if (!vendorToken()) { setReady(true); return }
    vget<Vendor>('/me').then(setVendor).catch(() => {}).finally(() => setReady(true))
    const out = () => setVendor(null)
    window.addEventListener('pd-vendor-signed-out', out)
    return () => window.removeEventListener('pd-vendor-signed-out', out)
  }, [])
  useEffect(() => { document.title = 'Vendor portal · Saltstayz' }, [])
  if (!ready) return null
  if (!vendor) return <VendorLogin onIn={setVendor} />
  const signOut = () => { setVendorToken(''); setVendor(null) }
  return (
    <Ctx.Provider value={{ vendor, setVendor }}>
      <div className="vp">
        <header className="vp-top">
          <div><b>Saltstayz vendor portal</b><span className="sub">{vendor.name} · {vendor.code}</span></div>
          <nav>
            <NavLink to="/vendor" end>Dashboard</NavLink>
            <NavLink to="/vendor/orders">Orders</NavLink>
            <NavLink to="/vendor/payments">Payments</NavLink>
            <NavLink to="/vendor/password">Password</NavLink>
            <button className="link" onClick={signOut}>Sign out</button>
          </nav>
        </header>
        <main className="vp-main">
          {vendor.must_change ? <PasswordPage first /> : (
            <Routes>
              <Route index element={<VendorDashboard />} />
              <Route path="orders" element={<VendorOrdersPage />} />
              <Route path="orders/:id" element={<VendorOrderPage />} />
              <Route path="payments" element={<VendorPaymentsPage />} />
              <Route path="password" element={<PasswordPage />} />
              <Route path="*" element={<Navigate to="/vendor" replace />} />
            </Routes>
          )}
        </main>
      </div>
    </Ctx.Provider>
  )
}

function VendorLogin({ onIn }: { onIn: (v: Vendor) => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true); setErrors([])
    try {
      const r = await vpost<{ token: string; vendor: Vendor }>('/login', { email, password })
      setVendorToken(r.token)
      onIn(r.vendor)
    } catch (err) { setErrors(err instanceof ApiError ? err.errors : [String(err)]) } finally { setBusy(false) }
  }
  return (
    <div className="login">
      <form className="card" onSubmit={submit}>
        <SignInDoor on="vendor" />
        <div><h1>Vendor portal</h1><p className="sub">Saltstayz · purchase orders, invoices and payments</p></div>
        <label className="field"><span>Email</span>
          <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="username" required autoFocus /></label>
        <label className="field"><span>Password</span>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required /></label>
        <Errors list={errors} />
        <button className="btn" disabled={busy}>Sign in</button>
        <p className="hint">No sign-in yet, or forgot the password? Ask your contact at Saltstayz: they send a one-time password.</p>
      </form>
    </div>
  )
}

function PasswordPage({ first }: { first?: boolean }) {
  const { vendor, setVendor } = useVendor()
  const toast = useToast()
  const [cur, setCur] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const [errors, setErrors] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  async function submit(e: FormEvent) {
    e.preventDefault()
    if (next !== again) return setErrors(['The two new passwords are not the same.'])
    setBusy(true); setErrors([])
    try {
      const r = await vpost<{ message: string }>('/password', { current: cur, new: next })
      toast(r.message)
      setCur(''); setNext(''); setAgain('')
      setVendor({ ...vendor, must_change: false })
    } catch (err) { setErrors(err instanceof ApiError ? err.errors : [String(err)]) } finally { setBusy(false) }
  }
  return (
    <form className="card narrow" onSubmit={submit}>
      <h2>{first ? 'Choose your password' : 'Change password'}</h2>
      {first && <p className="note">You signed in with the one-time password from Saltstayz. Choose your own password to continue.</p>}
      <label className="field"><span>{first ? 'One-time password' : 'Current password'}</span><input type="password" value={cur} onChange={(e) => setCur(e.target.value)} autoComplete="current-password" required /></label>
      <label className="field"><span>New password</span><input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" minLength={8} required />
        <span className="hint">At least 8 characters.</span></label>
      <label className="field"><span>New password again</span><input type="password" value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" required /></label>
      <Errors list={errors} />
      <div className="btns"><button className="btn" disabled={busy}>Save password</button></div>
    </form>
  )
}
