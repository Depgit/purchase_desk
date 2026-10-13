// The receiving store records a delivery on the phone: quantity per line, the vendor invoice (number, date, amount, photo or PDF)
// and item photos. A part delivery keeps the balance open on the PO, or closes the PO short. When the vendor will not
// deliver the rest (canReject), the switch at the top swaps this form for the reject-balance form in the same card.
// Save draft keeps what is filled in (invoice photo and item photos too); the draft comes back when the PO is opened.
import { useEffect, useRef, useState } from 'react'
import { get, upload } from '../api/client'
import type { Doc } from '../api/types'
import { RejectBalanceForm } from '../balancereject/RejectBalanceForm'
import { dateTime, inr, lineValue, qty, today } from '../format/format'
import { DraftControls, KeptFiles, useDraft, type Draft } from '../formdrafts/drafts'
import { Errors } from '../ui/Errors'
import { Seg } from '../ui/Tabs'
import { useAction } from '../ui/useAction'

export function ReceivePanel({ doc, onDone, canReject }: { doc: Doc; onDone: () => void; canReject?: boolean }) {
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
      {mode === 'reject' ? <RejectBalanceForm doc={doc} onCancel={() => setMode('receive')} onDone={onDone} /> : <ReceiveForm doc={doc} onDone={onDone} />}
    </section>
  )
}

function ReceiveForm({ doc, onDone }: { doc: Doc; onDone: () => void }) {
  const open = doc.lines.filter((l) => l.qty - l.received_qty > 0)
  const [q, setQ] = useState<Record<number, string>>(Object.fromEntries(open.map((l) => [l.id, String(l.qty - l.received_qty)])))
  const [invNo, setInvNo] = useState('')
  const [invDate, setInvDate] = useState(today())
  const [invAmt, setInvAmt] = useState('')
  const [invoice, setInvoice] = useState<File | null>(null)
  const [photos, setPhotos] = useState<File[]>([])
  const [balance, setBalance] = useState<'keep' | 'close'>('keep')
  const a = useAction()
  const draft = useDraft('grn', doc.id)
  const [restored, setRestored] = useState('')
  // A draft saved earlier for this PO comes back by itself.
  useEffect(() => {
    get<Draft<GrnFields>[]>(`/drafts?form=grn&ref=${doc.id}`).then(async (list) => {
      const d = list[0]
      if (!d) return
      const f = d.fields
      setQ({ ...q, ...Object.fromEntries(Object.entries(f.q || {}).filter(([k]) => open.some((l) => String(l.id) === k))) })
      setInvNo(f.invNo || ''); setInvDate(f.invDate || today()); setInvAmt(f.invAmt || ''); setBalance(f.balance || 'keep')
      const files = await draft.open(d)
      setInvoice(files.invoice?.[0] || null); setPhotos(files.photos || [])
      setRestored(d.updated_at)
    }).catch(() => {})
  }, [doc.id]) // eslint-disable-line react-hooks/exhaustive-deps
  const saveDraft = () => draft.save(`${doc.po_number || doc.number}${invNo ? ' · invoice ' + invNo : ''}`, { q, invNo, invDate, invAmt, balance } satisfies GrnFields, { invoice, photos })
  const value = open.reduce((t, l) => t + lineValue(+q[l.id] || 0, l.rate, l.gst_pct), 0)
  const short = open.some((l) => (+q[l.id] || 0) < l.qty - l.received_qty)
  return (
    <>
      <div className="phead"><div><h2>Receive goods{doc.lines.some((l) => l.received_qty > 0) ? ' · next delivery on ' + doc.po_number : ''}</h2>
        <p className="sub">Count what actually arrived and change the number if it is different.</p></div></div>
      {restored && <p className="note">Your draft from {dateTime(restored)} is filled in. Check it and save the goods receipt.</p>}
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
          <KeptFiles files={[invoice]} /></label>
        <label className="field"><span>Photos of items</span><input type="file" accept="image/*" capture="environment" multiple onChange={(e) => setPhotos(Array.from(e.target.files || []))} />
          <KeptFiles files={photos} /></label>
        {short && <label className="field"><span>Less than due arrived</span>
          <select value={balance} onChange={(e) => setBalance(e.target.value as 'keep' | 'close')}>
            <option value="keep">More is coming: keep the balance open on this PO</option>
            <option value="close">Nothing more is coming: close the PO short</option></select></label>}
      </div>
      <Errors list={a.errors} />
      <div className="btns"><DraftControls draft={draft} onSave={saveDraft} />
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => upload(`/documents/${doc.id}/receipts`, {
        quantities: Object.fromEntries(Object.entries(q).map(([k, v]) => [k, +v || 0])), invoice_number: invNo, invoice_date: invDate, invoice_amount: +invAmt, balance,
      }, { invoice, photos }), (r: any) => { draft.clear(); onDone(); return r.message })}>Save goods receipt</button></div>
    </>
  )
}

interface GrnFields { q: Record<number, string>; invNo: string; invDate: string; invAmt: string; balance: 'keep' | 'close' }
