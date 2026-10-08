// Onboard or edit a vendor. Formats are checked on save and duplicates by name, GSTIN or PAN are blocked.
// A change to the bank account, GST details or PAN sends the vendor back to Finance for verification.
import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { get, upload } from '../api/client'
import { useLookups } from '../api/lookups'
import { Checks, Field } from '../ui/Field'
import { Errors } from '../ui/Errors'
import { PHASE, usePhase } from '../releasephase/phase'
import { useAction } from '../ui/useAction'

export interface Vendor {
  id: number; code: string; vendor_type: string; legal_name: string; address: string; city: string; pin: string; contact_person: string; mobile: string; email: string
  gst_registered: boolean; gstin: string; pan: string; state_code: string; msme: boolean; udyam: string; bank_holder: string; bank_account_masked: string; ifsc: string
  bank_proof_file_id: number | null; bank_verified: boolean; bank_verified_by: string; payment_terms: string; sheet_mode: string; sheet_time: string; sheet_channel: string
  active: boolean; inactive_reason: string; sub_category_ids: number[]; bill_type_ids: number[]; supplies: string[]
}

const blank = { vendor_type: 'goods', legal_name: '', address: '', city: '', pin: '', contact_person: '', mobile: '', email: '', gst_registered: true, gstin: '', pan: '',
  state_code: '', msme: false, udyam: '', bank_holder: '', bank_account: '', bank_account_confirm: '', ifsc: '', payment_terms: 'Net 30', sub_category_ids: [] as number[], bill_type_ids: [] as number[] }

export function VendorForm() {
  const { id } = useParams()
  const bills = usePhase().on(PHASE.bills) // utility, AMC and other-service vendors open with bills in phase 3
  const nav = useNavigate()
  const l = useLookups()
  const [f, setF] = useState(blank)
  const [proof, setProof] = useState<File | null>(null)
  const [orig, setOrig] = useState<Vendor | null>(null)
  const a = useAction()
  useEffect(() => {
    if (!id) return
    get<Vendor>('/vendors/' + id).then((v) => {
      setOrig(v)
      setF({ ...blank, ...v, bank_account: '', bank_account_confirm: '', ifsc: '' })
    })
  }, [id])
  const set = <K extends keyof typeof f>(k: K, v: (typeof f)[K]) => setF({ ...f, [k]: v })
  const inp = (k: keyof typeof f, extra: Record<string, unknown> = {}) => <input value={String(f[k] ?? '')} onChange={(e) => set(k, e.target.value as never)} {...extra} />
  const goods = f.vendor_type === 'goods'
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
                .filter(([k]) => bills || k === 'goods' || k === f.vendor_type).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></Field>
            <Field label="Address">{inp('address')}</Field><Field label="City">{inp('city')}</Field>
            <Field label="PIN code">{inp('pin', { inputMode: 'numeric', maxLength: 6 })}</Field><Field label="Contact person">{inp('contact_person')}</Field>
            <Field label="Mobile number">{inp('mobile', { inputMode: 'numeric', maxLength: 10 })}</Field><Field label="Email">{inp('email', { type: 'email' })}</Field>
          </div></section>
          <section className="card"><h2>Tax and MSME</h2><div className="grid2">
            <Field label="GST status"><select value={f.gst_registered ? 'y' : 'n'} onChange={(e) => set('gst_registered', e.target.value === 'y')}><option value="y">GST registered</option><option value="n">Not registered</option></select></Field>
            {f.gst_registered ? <Field label="GSTIN">{inp('gstin', { maxLength: 15, placeholder: '15 characters' })}</Field>
              : <p className="note warn" style={{ alignSelf: 'end' }}>Unregistered vendor: no GST is charged. Finance sees this flag on every invoice.</p>}
            <Field label="PAN" hint={f.gst_registered ? 'Must match the PAN inside the GSTIN.' : ''}>{inp('pan', { maxLength: 10 })}</Field>
            {!f.gst_registered && <Field label="State code" hint="Two letters, for example HR. Used for GST on bills.">{inp('state_code', { maxLength: 2 })}</Field>}
            <Field label="MSME registered"><select value={f.msme ? 'y' : 'n'} onChange={(e) => set('msme', e.target.value === 'y')}><option value="n">No</option><option value="y">Yes</option></select></Field>
            {f.msme && <Field label="Udyam number">{inp('udyam', { placeholder: 'UDYAM-XX-00-0000000' })}</Field>}
          </div></section>
          <section className="card"><h2>Bank details</h2>
            {id && <p className="hint">Current account {orig?.bank_account_masked || 'none'} · IFSC {orig?.ifsc || '—'}. Leave blank to keep it.</p>}
            <div className="grid2">
              <Field label="Account holder name">{inp('bank_holder')}</Field><Field label={id ? 'New IFSC' : 'IFSC'}>{inp('ifsc', { maxLength: 11 })}</Field>
              <Field label={id ? 'New account number' : 'Account number'}>{inp('bank_account', { type: 'password', inputMode: 'numeric', autoComplete: 'off' })}</Field>
              <Field label="Re-enter account number">{inp('bank_account_confirm', { inputMode: 'numeric', autoComplete: 'off' })}</Field>
              <Field label="Cancelled cheque or bank letter"><input type="file" accept="image/*,application/pdf" onChange={(e) => setProof(e.target.files?.[0] || null)} /></Field>
              <Field label="Payment terms"><select value={f.payment_terms} onChange={(e) => set('payment_terms', e.target.value)}>{['Net 15', 'Net 30', 'Net 45', 'Advance'].map((t) => <option key={t}>{t}</option>)}</select></Field>
            </div></section>
          <section className="card"><h2>What they supply</h2>
            {goods ? <>
              <Checks value={f.sub_category_ids} onChange={(v) => set('sub_category_ids', v)} options={(l?.subCategories || []).filter((s) => s.active).map((s) => [s.id, `${s.name} (${s.category})`])} />
              <p className="hint">The sub-categories the vendor is approved to quote for. Which properties it supplies is set under Vendor Mapping.</p></>
              : <><Checks value={f.bill_type_ids} onChange={(v) => set('bill_type_ids', v)} options={bt.map((b) => [b.id, b.name])} />
                <p className="hint">Bill type missing? An admin adds it under Settings › Bill Types.</p></>}
          </section>
        </div>
        <div className="stack"><section className="card"><h2>What happens next</h2>
          <p>1. The vendor is saved as <b>bank verification pending</b>.</p>
          <p>2. Finance checks the cancelled cheque against the account details and verifies.</p>
          <p>3. Purchase maps the vendor to items and properties under Vendor Mapping and adds SKU rates.</p>
          <Errors list={a.errors} />
          <button className="btn" disabled={a.busy} onClick={() => a.run(() => upload(id ? '/vendors/' + id : '/vendors', f, { bank_proof: proof }, id ? 'PUT' : 'POST'),
            (r: any) => { nav('/vendors'); return r.sent_for_verification ? 'Saved. Waiting for Finance to verify again.' : id ? 'Vendor saved' : `${r.code} saved. Waiting for Finance to verify bank details.` })}>Save vendor</button>
        </section></div>
      </div>
    </>
  )
}
