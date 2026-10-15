// Onboard or edit a vendor. Formats are checked on save and duplicates by name, GSTIN or PAN are blocked.
// A change to the bank account, GST details or PAN sends the vendor back to Finance for verification.
import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { ApiError, fileUrl, get, post, upload } from '../api/client'
import { useLookups } from '../api/lookups'
import { Checks, Field } from '../ui/Field'
import { Errors } from '../ui/Errors'
import { PAYMENT_TERMS } from '../api/types'
import { useAction } from '../ui/useAction'
import { usePopup } from '../ui/Toast'
import { PortalAccess } from '../vendorportal/PortalAccess'
import { ZohoMasterCard } from '../zohobooks/ZohoMasterCard'
import { useScreen } from '../screenaccess/screen'

export interface Vendor {
  id: number; code: string; vendor_type: string; legal_name: string; address: string; city: string; pin: string; contact_person: string; mobile: string; email: string
  gst_registered: boolean; gstin: string; pan: string; state_code: string; msme: boolean; udyam: string; bank_holder: string; bank_account_masked: string; ifsc: string
  bank_proof_file_id: number | null; msme_file_id: number | null; bank_verified: boolean; bank_verified_by: string; payment_terms: string; sheet_mode: string; sheet_time: string; sheet_channel: string
  active: boolean; inactive_reason: string; sub_category_ids: number[]; bill_type_ids: number[]; supplies: string[]
}

const blank = { vendor_type: 'goods', legal_name: '', address: '', city: '', pin: '', contact_person: '', mobile: '', email: '', gst_registered: true, gstin: '', pan: '',
  state_code: '', msme: false, udyam: '', bank_holder: '', bank_account: '', bank_account_confirm: '', ifsc: '', payment_terms: 'Net 30', sub_category_ids: [] as number[], bill_type_ids: [] as number[] }

export function VendorForm() {
  const { id } = useParams()
  const nav = useNavigate()
  const l = useLookups()
  const [f, setF] = useState(blank)
  const [proof, setProof] = useState<File | null>(null)
  const [msmeCert, setMsmeCert] = useState<File | null>(null)
  const [orig, setOrig] = useState<Vendor | null>(null)
  const a = useAction()
  const popup = usePopup()
  const sc = useScreen('vendors')
  // A new goods vendor can get its vendor portal sign-in in the same step (later: the Vendor portal card on its page).
  const [portalOn, setPortalOn] = useState(true)
  const [portalEmail, setPortalEmail] = useState<string | null>(null)
  useEffect(() => {
    if (!id) return
    get<Vendor>('/vendors/' + id).then((v) => {
      setOrig(v)
      setF({ ...blank, ...v, bank_account: '', bank_account_confirm: '' })
    })
  }, [id])
  const set = <K extends keyof typeof f>(k: K, v: (typeof f)[K]) => setF({ ...f, [k]: v })
  const inp = (k: keyof typeof f, extra: Record<string, unknown> = {}) => <input value={String(f[k] ?? '')} onChange={(e) => set(k, e.target.value as never)} {...extra} />
  const goods = f.vendor_type === 'goods'
  // Characters 3 to 12 of a GSTIN are the PAN, so a GST-registered vendor's PAN is filled from it.
  const setGSTIN = (v: string) => { const g = v.toUpperCase().replace(/\s/g, ''); setF({ ...f, gstin: g, pan: g.length >= 12 ? g.slice(2, 12) : f.pan }) }
  const bt = (l?.billTypes || []).filter((b) => b.active && (f.vendor_type === 'utility' ? b.section === 'utility' : f.vendor_type === 'amc' ? b.section === 'amc' : true))
  return (
    <>
      <div className="head"><div><button className="link" onClick={() => nav('/vendors')}>‹ Vendors</button><h1>{id ? `Edit ${orig?.legal_name || ''}` : 'Onboard vendor'}</h1>
        <p className="sub">Formats are checked as you save, and duplicates by name, GSTIN or PAN are blocked.</p></div></div>
      {id && <p className="note warn">Every field can be changed. A change to the bank account, GST details or PAN sends the vendor back to Finance, and it cannot be used on new requests or bills until verified again.</p>}
      <div className="cols">
        <div className="stack">
          <section className="card"><h2>Business details</h2><div className="grid2">
            <Field label="Legal entity name">{inp('legal_name')}</Field>
            <Field label="Vendor type"><select value={f.vendor_type} onChange={(e) => set('vendor_type', e.target.value)}>
              {([['goods', 'Goods supplier'], ['utility', 'Utility provider'], ['amc', 'AMC / maintenance contractor'], ['other', 'Other services']] as [string, string][])
                .map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></Field>
            <Field label="Address">{inp('address')}</Field><Field label="City">{inp('city')}</Field>
            <Field label="PIN code">{inp('pin', { inputMode: 'numeric', maxLength: 6 })}</Field><Field label="Contact person">{inp('contact_person')}</Field>
            <Field label="Mobile number">{inp('mobile', { inputMode: 'numeric', maxLength: 10 })}</Field><Field label="Email">{inp('email', { type: 'email' })}</Field>
          </div></section>
          <section className="card"><h2>Tax and MSME</h2><div className="grid2">
            <Field label="GST status"><select value={f.gst_registered ? 'y' : 'n'} onChange={(e) => set('gst_registered', e.target.value === 'y')}><option value="y">GST registered</option><option value="n">Not registered</option></select></Field>
            {f.gst_registered ? <Field label="GSTIN"><input value={f.gstin} maxLength={15} placeholder="15 characters" onChange={(e) => setGSTIN(e.target.value)} /></Field>
              : <p className="note warn" style={{ alignSelf: 'end' }}>Unregistered vendor: no GST is charged. Finance sees this flag on every invoice.</p>}
            <Field label="PAN" hint={f.gst_registered ? 'Filled from the GSTIN (characters 3 to 12).' : 'Required. 10 characters, for example ABCDE1234F.'}>{f.gst_registered
              ? <input value={f.pan} readOnly placeholder="Enter the GSTIN first" />
              : <input value={f.pan} maxLength={10} onChange={(e) => set('pan', e.target.value.toUpperCase())} />}</Field>
            {!f.gst_registered && <Field label="State code" hint="Two letters, for example HR. Used for GST on bills.">{inp('state_code', { maxLength: 2 })}</Field>}
            <Field label="MSME registered"><select value={f.msme ? 'y' : 'n'} onChange={(e) => set('msme', e.target.value === 'y')}><option value="n">No</option><option value="y">Yes</option></select></Field>
            {f.msme && <Field label="Udyam number">{inp('udyam', { placeholder: 'UDYAM-XX-00-0000000' })}</Field>}
            {f.msme && <Field label="MSME (Udyam) certificate" hint={orig?.msme_file_id ? 'A certificate is on file. Attach a new one only to replace it.' : 'Required for an MSME vendor: the 45-day payment rule depends on it.'}>
              <input type="file" accept="image/*,application/pdf" onChange={(e) => setMsmeCert(e.target.files?.[0] || null)} />
              {orig?.msme_file_id && <a className="link" href={fileUrl(orig.msme_file_id)} target="_blank" rel="noreferrer">View certificate on file</a>}</Field>}
          </div></section>
          <section className="card"><h2>Bank details</h2>
            {id && <p className="hint">Current account {orig?.bank_account_masked || 'none'}. For safety the full number is never shown again: leave it blank to keep it. To change the account or IFSC, enter the new account number twice; the vendor then goes back to Finance for bank verification.</p>}
            <div className="grid2">
              <Field label="Account holder name">{inp('bank_holder')}</Field><Field label="IFSC">{inp('ifsc', { maxLength: 11, autoComplete: 'off', 'data-1p-ignore': true, 'data-lpignore': 'true' })}</Field>
              <Field label={id ? 'New account number' : 'Account number'}>{inp('bank_account', { className: 'masked', inputMode: 'numeric', autoComplete: 'off', 'data-1p-ignore': true, 'data-lpignore': 'true' })}</Field>
              <Field label="Re-enter account number">{inp('bank_account_confirm', { inputMode: 'numeric', autoComplete: 'off', 'data-1p-ignore': true, 'data-lpignore': 'true' })}</Field>
              <Field label="Cancelled cheque or bank letter"><input type="file" accept="image/*,application/pdf" onChange={(e) => setProof(e.target.files?.[0] || null)} /></Field>
              <Field label="Payment terms"><select value={f.payment_terms} onChange={(e) => set('payment_terms', e.target.value)}>{PAYMENT_TERMS.map((t) => <option key={t}>{t}</option>)}</select></Field>
            </div></section>
          <section className="card"><h2>What they supply</h2>
            {goods ? <>
              <Checks label="Sub-categories" placeholder="Choose sub-categories" value={f.sub_category_ids} onChange={(v) => set('sub_category_ids', v)} options={(l?.subCategories || []).filter((s) => s.active).map((s) => [s.id, `${s.name} (${s.category})`])} />
              <p className="hint">The sub-categories the vendor is approved to quote for. Which properties it supplies is set under Vendor Mapping.</p></>
              : <><Checks label="Bill types" placeholder="Choose bill types" value={f.bill_type_ids} onChange={(v) => set('bill_type_ids', v)} options={bt.map((b) => [b.id, b.name])} />
                <p className="hint">Bill type missing? An admin adds it under Settings › Bill Types.</p></>}
          </section>
        </div>
        <div className="stack"><section className="card"><h2>What happens next</h2>
          <p>1. The vendor is saved as <b>bank verification pending</b>.</p>
          <p>2. Finance checks the cancelled cheque against the account details and verifies.</p>
          <p>3. Purchase maps the vendor to items and properties under Vendor Mapping and adds SKU rates.</p>
          {!id && goods && <div className="field"><label className="ck"><input type="checkbox" checked={portalOn} onChange={(e) => setPortalOn(e.target.checked)} />
            <span>Give the vendor a portal sign-in now<br /><span className="hint">A one-time password is shown after saving and emailed to the vendor. They sign in at /vendor and choose their own password.</span></span></label>
            {portalOn && <label className="field"><span>Portal sign-in email</span><input type="email" value={portalEmail ?? f.email} onChange={(e) => setPortalEmail(e.target.value)} placeholder="Defaults to the vendor's email" /></label>}</div>}
          <Errors list={a.errors} />
          <button className="btn" disabled={a.busy} onClick={() => a.run(async () => {
              const r: any = await upload(id ? '/vendors/' + id : '/vendors', id && f.ifsc.trim().toUpperCase() === (orig?.ifsc || '') ? { ...f, ifsc: '' } : f, { bank_proof: proof, msme_certificate: f.msme ? msmeCert : null }, id ? 'PUT' : 'POST')
              if (!id && goods && portalOn) {
                // The vendor is saved either way; a problem with the sign-in is fixed on its page.
                try { r.portal = await post<any>(`/vendors/${r.id}/portal`, { email: (portalEmail ?? f.email).trim() }) } catch (e) { r.portalError = e instanceof ApiError ? e.errors.join(' ') : String(e) }
              }
              return r
            }, (r: any) => {
              if (r.portal) popup('Vendor portal sign-in', `${r.code} signs in at ${r.portal.url} with ${r.portal.email} and this one-time password: ${r.portal.password}\n\n${r.portal.emailed ? 'It was also emailed to the vendor.' : 'Email is not set up, so send it to the vendor yourself.'} It is not shown again; the vendor sets its own password at the first sign-in.`)
              if (r.portalError) { nav(`/vendors/${r.id}/edit`); return `${r.code} saved, but the portal sign-in was not given: ${r.portalError} Fix it in the Vendor portal card.` }
              nav('/vendors')
              return r.sent_for_verification ? 'Saved. Waiting for Finance to verify again.' : id ? 'Vendor saved' : `${r.code} saved. Waiting for Finance to verify bank details.`
            })}>Save vendor</button>
        </section>
        {id && orig?.vendor_type === 'goods' && <PortalAccess vendorId={+id} canEdit={sc.edit} />}
        {id && <ZohoMasterCard kind="vendor" id={+id} />}</div>
      </div>
    </>
  )
}
