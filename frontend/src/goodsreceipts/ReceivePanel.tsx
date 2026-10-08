// The receiving store records a delivery on the phone: quantity per line, the vendor invoice (number, date, amount, photo or PDF)
// and item photos. A part delivery keeps the balance open on the PO, or closes the PO short.
import { useState } from 'react'
import { upload } from '../api/client'
import type { Doc } from '../api/types'
import { inr, lineValue, qty, today } from '../format/format'
import { Errors } from '../ui/Errors'
import { useAction } from '../ui/useAction'

export function ReceivePanel({ doc, onDone }: { doc: Doc; onDone: () => void }) {
  const open = doc.lines.filter((l) => l.qty - l.received_qty > 0)
  const [q, setQ] = useState<Record<number, string>>(Object.fromEntries(open.map((l) => [l.id, String(l.qty - l.received_qty)])))
  const [invNo, setInvNo] = useState('')
  const [invDate, setInvDate] = useState(today())
  const [invAmt, setInvAmt] = useState('')
  const [invoice, setInvoice] = useState<File | null>(null)
  const [photos, setPhotos] = useState<File[]>([])
  const [balance, setBalance] = useState<'keep' | 'close'>('keep')
  const a = useAction()
  const value = open.reduce((t, l) => t + lineValue(+q[l.id] || 0, l.rate, l.gst_pct), 0)
  const short = open.some((l) => (+q[l.id] || 0) < l.qty - l.received_qty)
  return (
    <section className="card act">
      <h2>Receive goods{doc.lines.some((l) => l.received_qty > 0) ? ' · next delivery on ' + doc.po_number : ''}</h2>
      <p className="hint">Count what actually arrived and change the number if it is different.</p>
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
        <label className="field"><span>Invoice photo or PDF</span><input type="file" accept="image/*,application/pdf" capture="environment" onChange={(e) => setInvoice(e.target.files?.[0] || null)} /></label>
        <label className="field"><span>Photos of items</span><input type="file" accept="image/*" capture="environment" multiple onChange={(e) => setPhotos(Array.from(e.target.files || []))} /></label>
        {short && <label className="field"><span>Less than due arrived</span>
          <select value={balance} onChange={(e) => setBalance(e.target.value as 'keep' | 'close')}>
            <option value="keep">More is coming: keep the balance open on this PO</option>
            <option value="close">Nothing more is coming: close the PO short</option></select></label>}
      </div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn" disabled={a.busy} onClick={() => a.run(() => upload(`/documents/${doc.id}/receipts`, {
        quantities: Object.fromEntries(Object.entries(q).map(([k, v]) => [k, +v || 0])), invoice_number: invNo, invoice_date: invDate, invoice_amount: +invAmt, balance,
      }, { invoice, photos }), (r: any) => { onDone(); return r.message })}>Save goods receipt</button></div>
    </section>
  )
}
