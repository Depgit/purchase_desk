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
/** A vendor the signed-in email can open. */
export interface Choice { id: number; code: string; name: string }
interface Session { token?: string; vendor: Vendor; vendors: Choice[] }

const Ctx = createContext<{ vendor: Vendor; setVendor: (v: Vendor) => void } | null>(null)
export const useVendor = () => useContext(Ctx)!

// The vendor last worked as on this device, opened first at the next sign-in.
const LAST = 'pd-vendor-last'
const remember = (id: number) => { try { localStorage.setItem(LAST, String(id)) } catch { /* private mode */ } }
const lastVendor = () => { try { return Number(localStorage.getItem(LAST)) || 0 } catch { return 0 } }

export function VendorPortal() {
  const [vendor, setVendor] = useState<Vendor | null>(null)
  const [vendors, setVendors] = useState<Choice[]>([])
  const [picking, setPicking] = useState(false)
  const [ready, setReady] = useState(false)
  const [switchErr, setSwitchErr] = useState('')
  useEffect(() => {
    if (!vendorToken()) { setReady(true); return }
    vget<Session>('/me').then((r) => { setVendor(r.vendor); setVendors(r.vendors) }).catch(() => {}).finally(() => setReady(true))
    const out = () => setVendor(null)
    window.addEventListener('pd-vendor-signed-out', out)
    return () => window.removeEventListener('pd-vendor-signed-out', out)
  }, [])
  useEffect(() => { document.title = 'Vendor portal · Saltstayz' }, [])
  if (!ready) return null
  const signedIn = (r: Session) => {
    if (r.token) setVendorToken(r.token)
    setVendor(r.vendor); setVendors(r.vendors); remember(r.vendor.id)
  }
  if (!vendor) return <VendorLogin onIn={(r) => { signedIn(r); setPicking(r.vendors.length > 1) }} />
  const signOut = () => { setVendorToken(''); setVendor(null) }
  const switchTo = async (id: number) => {
    setSwitchErr('')
    try { signedIn(await vpost<Session>('/switch', { vendor_id: id })); setPicking(false) } catch (e) { setSwitchErr(e instanceof ApiError ? e.message : String(e)) }
  }
  return (
    <Ctx.Provider value={{ vendor, setVendor }}>
      <div className="vp">
        <header className="vp-top">
          <div><b>Saltstayz vendor portal</b>
            {vendors.length > 1 && !vendor.must_change
              ? <label className="vp-switch"><span className="sub">Working as</span>
                  <select value={vendor.id} onChange={(e) => switchTo(+e.target.value)} aria-label="Vendor">
                    {vendors.map((v) => <option key={v.id} value={v.id}>{v.name} · {v.code}</option>)}</select></label>
              : <span className="sub">{vendor.name} · {vendor.code}</span>}</div>
          <nav>
            <NavLink to="/vendor" end>Dashboard</NavLink>
            <NavLink to="/vendor/orders">Orders</NavLink>
            <NavLink to="/vendor/payments">Payments</NavLink>
            <NavLink to="/vendor/password">Password</NavLink>
            <button className="link" onClick={signOut}>Sign out</button>
          </nav>
        </header>
        {/* A new key per vendor reloads every page's data when the vendor is switched. */}
        <main className="vp-main" key={vendor.id}>
          {switchErr && <p className="note bad">{switchErr}</p>}
          {vendor.must_change ? <PasswordPage first /> : picking ? <PickVendor vendors={vendors} email={vendor.email} onPick={switchTo} /> : (
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

// PickVendor is shown once after signing in when the email opens several vendors.
function PickVendor({ vendors, email, onPick }: { vendors: Choice[]; email: string; onPick: (id: number) => void }) {
  return (
    <section className="card narrow"><h2>Choose a vendor</h2>
      <p className="hint">{email} opens {vendors.length} vendors. Pick the one to work as; you can switch at any time from the top of the page.</p>
      <div className="vp-list">{vendors.map((v) => (
        <button key={v.id} className="card vp-row" onClick={() => onPick(v.id)}><span><b>{v.name}</b><span className="hint">{v.code}</span></span><span aria-hidden="true">›</span></button>
      ))}</div>
    </section>
  )
}

function VendorLogin({ onIn }: { onIn: (r: Session) => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true); setErrors([])
    try {
      onIn(await vpost<Session>('/login', { email, password, vendor_id: lastVendor() }))
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
