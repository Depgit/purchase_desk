// "My details" on the vendor portal: the vendor's details on file, and a form to ask for changes (address, contact,
// GST, PAN, MSME, bank with its proof). Saltstayz Finance approves a request before anything changes.
import { useEffect, useState } from 'react'
import { ApiError } from '../api/client'
import { dateTime } from '../format/format'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Pill } from '../ui/Pill'
import { useToast } from '../ui/Toast'
import { vget, vuploadFiles } from './portalApi'

interface Details {
  vendor: { code: string; legal_name: string; address: string; city: string; pin: string; contact_person: string; mobile: string; email: string; gst_registered: boolean
    gstin: string; pan: string; state_code: string; msme: boolean; udyam: string; bank_holder: string; bank_account_masked: string; ifsc: string; payment_terms: string; supplies: string[] }
  changes: { id: number; changes: string; status: string; asked_at: string; decided_at: string | null; note: string }[]
}
const TONE: Record<string, '' | 'ok' | 'warn' | 'bad'> = { pending: 'warn', approved: 'ok', rejected: 'bad' }
const LABEL: Record<string, string> = { pending: 'Waiting for Finance', approved: 'Approved', rejected: 'Not approved' }

export function VendorDetailsPage() {
  const toast = useToast()
  const [d, setD] = useState<Details | null>(null)
  const [f, setF] = useState<Record<string, any> | null>(null)
  const [proof, setProof] = useState<File | null>(null)
  const [cert, setCert] = useState<File | null>(null)
  const [errors, setErrors] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const load = () => vget<Details>('/details').then((x) => { setD(x); setF({ ...x.vendor, bank_account: '', bank_account_confirm: '' }) }).catch((e) => setErrors([e.message]))
  useEffect(() => { load() }, [])
  if (!d || !f) return <p className="sub">{errors[0] || 'Loading…'}</p>
  const v = d.vendor
  const set = (k: string, val: unknown) => setF({ ...f, [k]: val })
  const inp = (k: string, extra: Record<string, unknown> = {}) => <input value={String(f[k] ?? '')} onChange={(e) => set(k, e.target.value)} {...extra} />
  const bankChange = !!f.bank_account || (f.ifsc || '').toUpperCase() !== v.ifsc
  const pending = d.changes.find((c) => c.status === 'pending')
  async function submit() {
    setBusy(true); setErrors([])
    try {
      const r = await vuploadFiles<{ message: string }>('/details', f, { bank_proof: proof, msme_certificate: f!.msme ? cert : null })
      toast(r.message); setProof(null); setCert(null); await load()
    } catch (e) { setErrors(e instanceof ApiError ? e.errors : [String(e)]) } finally { setBusy(false) }
  }
  return (
    <>
      <div className="head"><div><h1>My details</h1><p className="sub">{v.legal_name} · {v.code}. Ask for a change here; Saltstayz Finance checks it, and your details change once they approve.</p></div></div>
      {d.changes.length > 0 && <section className="card"><h2>Your requests</h2>
        {d.changes.map((c) => <p key={c.id}><Pill tone={TONE[c.status]}>{LABEL[c.status] || c.status}</Pill> {c.changes} <span className="hint">· asked {dateTime(c.asked_at)}{c.decided_at ? ' · decided ' + dateTime(c.decided_at) : ''}</span>
          {c.note && <><br /><span className="hint">Finance: {c.note}</span></>}</p>)}
        {pending && <p className="hint">Sending a new request replaces the one waiting.</p>}</section>}
      <section className="card"><h2>Business</h2><div className="grid2">
        <Field label="Legal name">{inp('legal_name')}</Field>
        <Field label="Contact person">{inp('contact_person')}</Field>
        <Field label="Address">{inp('address')}</Field>
        <Field label="City">{inp('city')}</Field>
        <Field label="PIN code">{inp('pin', { inputMode: 'numeric', maxLength: 6 })}</Field>
        <Field label="Mobile">{inp('mobile', { inputMode: 'numeric', maxLength: 10 })}</Field>
        <Field label="Email">{inp('email', { type: 'email' })}</Field>
        <Field label="Supplies" hint="Set by Saltstayz.">{v.supplies.join(', ') || '—'}</Field>
      </div></section>
      <section className="card"><h2>Tax and MSME</h2><div className="grid2">
        <Field label="GST status"><select value={f.gst_registered ? 'yes' : 'no'} onChange={(e) => set('gst_registered', e.target.value === 'yes')}><option value="yes">GST registered</option><option value="no">Not registered</option></select></Field>
        {f.gst_registered ? <Field label="GSTIN">{inp('gstin', { maxLength: 15 })}</Field> : <Field label="State code" hint="Two letters, for example HR.">{inp('state_code', { maxLength: 2 })}</Field>}
        <Field label="PAN">{inp('pan', { maxLength: 10 })}</Field>
        <Field label="MSME registered"><select value={f.msme ? 'yes' : 'no'} onChange={(e) => set('msme', e.target.value === 'yes')}><option value="no">No</option><option value="yes">Yes</option></select></Field>
        {f.msme && <><Field label="Udyam number">{inp('udyam')}</Field>
          <Field label="Udyam certificate" hint={v.msme ? 'Attach a new one only to replace the one on file.' : 'Required for an MSME vendor.'}><input type="file" accept="image/*,application/pdf" onChange={(e) => setCert(e.target.files?.[0] || null)} /></Field></>}
      </div></section>
      <section className="card"><h2>Bank account</h2>
        <p className="hint">On file: {v.bank_account_masked || 'none'} · IFSC {v.ifsc || '—'}. Leave the account blank to keep it. To change the account or IFSC, enter the new account number twice and attach a cancelled cheque or bank letter.</p>
        <div className="grid2">
          <Field label="Account holder name">{inp('bank_holder')}</Field>
          <Field label="IFSC">{inp('ifsc', { maxLength: 11, autoComplete: 'off' })}</Field>
          <Field label="New account number">{inp('bank_account', { className: 'masked', inputMode: 'numeric', autoComplete: 'off' })}</Field>
          <Field label="Re-enter account number">{inp('bank_account_confirm', { inputMode: 'numeric', autoComplete: 'off' })}</Field>
          {bankChange && <Field label="Cancelled cheque or bank letter"><input type="file" accept="image/*,application/pdf" onChange={(e) => setProof(e.target.files?.[0] || null)} /></Field>}
        </div></section>
      <Errors list={errors} />
      <div className="btns"><button className="btn ghost" onClick={() => setF({ ...v, bank_account: '', bank_account_confirm: '' })}>Undo my edits</button>
        <button className="btn" disabled={busy} onClick={submit}>Send to Saltstayz for approval</button></div>
    </>
  )
}
