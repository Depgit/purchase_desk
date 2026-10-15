// The vendor's portal sign-in, on the vendor's page: give it (or reset the password), or turn it off. The one-time
// password is shown here once and emailed to the vendor with the sign-in link.
import { useState } from 'react'
import { post } from '../api/client'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { dateTime } from '../format/format'
import { Errors } from '../ui/Errors'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'

interface Access { enabled: boolean; active: boolean; email: string; must_change: boolean; last_login_at: string | null; url: string }

export function PortalAccess({ vendorId, canEdit }: { vendorId: number; canEdit: boolean }) {
  const { data, reload } = useApi<Access>(`/vendors/${vendorId}/portal`)
  const { me, hasRight } = useSession()
  const [email, setEmail] = useState<string | null>(null)
  const [given, setGiven] = useState<{ email: string; password: string; url: string; emailed: boolean } | null>(null)
  const a = useAction()
  if (!data) return null
  const may = canEdit && (me?.is_admin || hasRight('masters') || hasRight('issue_po'))
  const on = data.enabled && data.active
  return (
    <section className="card"><h2>Vendor portal</h2>
      <p>{on ? <Pill tone="ok">Can sign in</Pill> : data.enabled ? <Pill tone="off">Sign-in turned off</Pill> : <Pill tone="off">No sign-in</Pill>}</p>
      <p className="hint">On the portal ({data.url}) the vendor accepts or declines POs, gives dispatch dates, uploads invoices and sees its payments.</p>
      {on && <p className="hint">Signs in as {data.email} · {data.last_login_at ? 'last signed in ' + dateTime(data.last_login_at) : 'not signed in yet'}{data.must_change ? ' · still on the one-time password' : ''}</p>}
      {given && <div className="note ok"><b>One-time password for {given.email}: <code>{given.password}</code></b><br />
        {given.emailed ? 'It was emailed to the vendor with the sign-in link.' : 'Email is not set up, so share it with the vendor yourself.'} It is not shown again; the vendor chooses its own password at the first sign-in.</div>}
      {may && <>
        <label className="field"><span>Sign-in email</span><input type="email" value={email ?? data.email} onChange={(e) => setEmail(e.target.value)} /></label>
        <Errors list={a.errors} />
        <div className="btns">
          {on && <button className="btn ghost" disabled={a.busy} onClick={() => a.run(() => post(`/vendors/${vendorId}/portal/off`), () => { setGiven(null); reload(); return 'Sign-in turned off' })}>Turn off</button>}
          <button className="btn" disabled={a.busy} onClick={() => a.run(() => post<any>(`/vendors/${vendorId}/portal`, { email: email ?? data.email }), (r: any) => { setGiven(r); reload(); return r.message })}>
            {on ? 'Reset password' : data.enabled ? 'Turn on again' : 'Give sign-in'}</button>
        </div></>}
    </section>
  )
}
