// The receiving store records a delivery on the phone: quantity per line, the vendor invoice (number, date, amount, photo or PDF)
// and item photos. A part delivery keeps the balance open on the PO, or closes the PO short. When the vendor will not
// deliver the rest (canReject), the switch at the top swaps this form for the reject-balance form in the same card.
// Save draft keeps what is filled in (invoice photo and item photos too); the draft comes back when the PO is opened.
// A delivery Accounts sent back for a recount comes back filled in as the store entered it, to correct, not retype.
// When less than due arrived the PO closes short unless the store says more is coming; a popup confirms the close.
// An invoice the vendor uploaded on the vendor portal fills the invoice fields; the store still counts the goods.
import { useEffect, useRef, useState } from 'react'
import { get, upload } from '../api/client'
import type { Doc } from '../api/types'
import { RejectBalanceForm } from '../balancereject/RejectBalanceForm'
import { dateTime, inr, lineValue, qty, today } from '../format/format'
import { DraftControls, KeptFiles, fetchFile, useDraft, type Draft } from '../formdrafts/drafts'
import { Errors } from '../ui/Errors'
import { Seg } from '../ui/Tabs'
import { usePopup } from '../ui/Toast'
import { useAction } from '../ui/useAction'
import type { PortalInvoice } from '../vendorportal/VendorResponse'

/** The delivery Accounts sent back for a recount, as the store entered it. */
export interface SentBack {
  grn: string; reason: string; quantities: Record<number, number>
  invoice_number: string; invoice_date: string; amount: number; invoice_file_id: number | null; photo_file_ids: number[]
}

export function ReceivePanel({ doc, onDone, canReject, sentBack, fromVendor }: { doc: Doc; onDone: () => void; canReject?: boolean; sentBack?: SentBack | null; fromVendor?: PortalInvoice }) {
  const [mode, setMode] = useState<'receive' | 'reject'>('receive')
  // The switch replaces the whole card's content, so bring its top into view: the reject form starts there.
  const card = useRef<HTMLElement>(null)
  const first = useRef(true)
  useEffect(() => {
    if (first.current) { first.current = false; return }
    card.current?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }, [mode])
  return (
    <section ref={card} className={'card act' + (mode === 'reject' ? ' rejecting' : '')}>
      {canReject && <Seg label="What happened" value={mode} onChange={setMode} items={[['receive', 'Goods arrived'], ['reject', "Vendor won't deliver the rest"]]} />}
      {mode === 'reject' ? <RejectBalanceForm doc={doc} onCancel={() => setMode('receive')} onDone={onDone} /> : <ReceiveForm doc={doc} onDone={onDone} sentBack={sentBack} fromVendor={fromVendor} />}
    </section>
  )
}

function ReceiveForm({ doc, onDone, sentBack, fromVendor }: { doc: Doc; onDone: () => void; sentBack?: SentBack | null; fromVendor?: PortalInvoice }) {
  const open = doc.lines.filter((l) => l.qty - l.received_qty > 0)
  const [q, setQ] = useState<Record<number, string>>(Object.fromEntries(open.map((l) => [l.id, String(l.qty - l.received_qty)])))
  const [invNo, setInvNo] = useState('')
  const [invDate, setInvDate] = useState(today())
  const [invAmt, setInvAmt] = useState('')
  const [invoice, setInvoice] = useState<File | null>(null)
  const [photos, setPhotos] = useState<File[]>([])
  const [balance, setBalance] = useState<'' | 'keep' | 'close'>('close')
  const a = useAction()
  const popup = usePopup()
  const draft = useDraft('grn', doc.id)
  const [restored, setRestored] = useState('')
  const [refilled, setRefilled] = useState(false)
  const [vendorFilled, setVendorFilled] = useState(false)
  const only = (m: Record<string, string>) => Object.fromEntries(Object.entries(m).filter(([k]) => open.some((l) => String(l.id) === k)))
  // A draft saved earlier for this PO comes back by itself; without one, a delivery sent back by Accounts does.
  useEffect(() => {
    get<Draft<GrnFields>[]>(`/drafts?form=grn&ref=${doc.id}`).catch(() => [] as Draft<GrnFields>[]).then(async (list) => {
      const d = list[0]
      if (d) {
        const f = d.fields
        setQ({ ...q, ...only(f.q || {}) })
        setInvNo(f.invNo || ''); setInvDate(f.invDate || today()); setInvAmt(f.invAmt || ''); setBalance(f.balance || 'close')
        setRestored(d.updated_at)
        const files = await draft.open(d)
        setInvoice(files.invoice?.[0] || null); setPhotos(files.photos || [])
        return
      }
      if (!sentBack) {
        if (!fromVendor) return
        setInvNo(fromVendor.invoice_number); setInvDate(fromVendor.invoice_date.slice(0, 10)); setInvAmt(String(fromVendor.amount))
        setVendorFilled(true)
        setInvoice(await fetchFile(fromVendor.file_id, 'invoice-' + fromVendor.invoice_number))
        return
      }
      setQ({ ...q, ...only(Object.fromEntries(Object.entries(sentBack.quantities).map(([k, v]) => [k, String(v)]))) })
      setInvNo(sentBack.invoice_number); setInvDate(sentBack.invoice_date.slice(0, 10)); setInvAmt(String(sentBack.amount))
      setRefilled(true)
      if (sentBack.invoice_file_id) setInvoice(await fetchFile(sentBack.invoice_file_id, 'invoice-' + sentBack.invoice_number))
      const ph = await Promise.all(sentBack.photo_file_ids.map((id, i) => fetchFile(id, `photo-${i + 1}`)))
      setPhotos(ph.filter((x): x is File => !!x))
    })
  }, [doc.id]) // eslint-disable-line react-hooks/exhaustive-deps
  const saveDraft = () => draft.save(`${doc.po_number || doc.number}${invNo ? ' · invoice ' + invNo : ''}`, { q, invNo, invDate, invAmt, balance } satisfies GrnFields, { invoice, photos })
  const value = open.reduce((t, l) => t + lineValue(+q[l.id] || 0, l.rate, l.gst_pct), 0)
  const short = open.some((l) => (+q[l.id] || 0) < l.qty - l.received_qty)
  return (
    <>
      <div className="phead"><div><h2>Receive goods{doc.lines.some((l) => l.received_qty > 0) ? ' · next delivery on ' + doc.po_number : ''}</h2>
        <p className="sub">Count what actually arrived and change the number if it is different.</p></div></div>
      {restored && <p className="note">Your draft from {dateTime(restored)} is filled in. Check it and save the goods receipt.</p>}
      {vendorFilled && !restored && <p className="note"><b>{doc.vendor} uploaded invoice {fromVendor?.invoice_number} on the vendor portal.</b> Its number, date, amount and file are filled in; count the goods and check the invoice before saving.</p>}
      {!restored && sentBack && <p className="note warn"><b>Accounts sent {sentBack.grn} back for a recount:</b> {sentBack.reason}
        {refilled && <><br />What was entered then is filled in below, invoice and photos too. Correct what Accounts asked about and save again.</>}</p>}
      <div className="scroll"><table>
        <thead><tr><th>Item</th><th className="r">Still due</th><th className="r">Received now</th></tr></thead>
        <tbody>{open.map((l) => (
          <tr key={l.id}><td>{l.item_name}<br /><span className="hint">{l.sku_code} · {l.pack_size}</span></td>
            <td className="r">{qty(l.qty - l.received_qty)}</td>
            <td className="r"><input className="qty" type="number" min="0" max={l.qty - l.received_qty} inputMode="decimal" aria-label={'Received ' + l.item_name}
              value={q[l.id]} onChange={(e) => setQ({ ...q, [l.id]: e.target.value })} /></td></tr>))}
        </tbody>
      </table></div>
      <div className="grid2">
        <label className="field"><span>Invoice number</span><input value={invNo} onChange={(e) => setInvNo(e.target.value)} placeholder="As printed on the bill" /></label>
        <label className="field"><span>Invoice date</span><input type="date" max={today()} value={invDate} onChange={(e) => setInvDate(e.target.value)} /></label>
        <label className="field"><span>Invoice amount incl. GST (₹)</span><input type="number" min="0" step="0.01" inputMode="decimal" value={invAmt} onChange={(e) => setInvAmt(e.target.value)} />
          <span className="hint">Value of goods received: {inr(value)}</span></label>
        <label className="field"><span>Invoice photo or PDF</span><input type="file" accept="image/*,application/pdf" capture="environment" onChange={(e) => setInvoice(e.target.files?.[0] || null)} />
          <KeptFiles files={[invoice]} />{refilled && invoice && !restored && <span className="hint">Kept from the earlier entry: {invoice.name}</span>}</label>
        <label className="field"><span>Photos of items</span><input type="file" accept="image/*" capture="environment" multiple onChange={(e) => setPhotos(Array.from(e.target.files || []))} />
          <KeptFiles files={photos} />{refilled && photos.length > 0 && !restored && <span className="hint">Kept from the earlier entry: {photos.length} photo{photos.length === 1 ? '' : 's'}</span>}</label>
        {short && <label className="field"><span>Less than due arrived</span>
          <select value={balance} className={balance ? '' : 'required'} onChange={(e) => setBalance(e.target.value as '' | 'keep' | 'close')}>
            <option value="close">Nothing more is coming: close the PO short</option>
            <option value="keep">More is coming: keep the balance open on this PO</option></select></label>}
      </div>
      <Errors list={a.errors} />
      <div className="btns"><DraftControls draft={draft} onSave={saveDraft} />
        <button className="btn" disabled={a.busy} onClick={() => short && !balance
          ? a.setErrors(['Less than due arrived: choose whether more is coming or the PO closes short.'])
          : a.run(() => upload(`/documents/${doc.id}/receipts`, {
            quantities: Object.fromEntries(Object.entries(q).map(([k, v]) => [k, +v || 0])), invoice_number: invNo, invoice_date: invDate, invoice_amount: +invAmt, balance: short ? balance : 'keep',
          }, { invoice, photos }), (r: any) => { draft.clear(); onDone(); if (r.closed) popup(balance === 'close' && short ? 'No more items needed' : 'PO closed', r.closed); return r.message })}>Save goods receipt</button></div>
    </>
  )
}

interface GrnFields { q: Record<number, string>; invNo: string; invDate: string; invAmt: string; balance: '' | 'keep' | 'close' }
