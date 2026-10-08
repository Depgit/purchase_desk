// The Purchase Team converts an approved request into a PO: confirms each rate, the delivery date and payment terms.
import { useState } from 'react'
import { post } from '../api/client'
import type { Doc } from '../api/types'
import { addDays, inr, today } from '../format/format'
import { Errors } from '../ui/Errors'
import { useAction } from '../ui/useAction'

export function IssuePOPanel({ doc, onDone }: { doc: Doc; onDone: () => void }) {
  const live = doc.lines.filter((l) => l.qty > 0)
  const [rates, setRates] = useState<Record<number, string>>(Object.fromEntries(live.map((l) => [l.id, String(l.rate)])))
  const [due, setDue] = useState(addDays(today(), 3))
  const [terms, setTerms] = useState(doc.vendor_terms || 'Net 30')
  const a = useAction()
  const total = live.reduce((t, l) => t + l.qty * (+rates[l.id] || 0) * (1 + l.gst_pct / 100), 0)
  return (
    <section className="card act">
      <h2>Convert to purchase order</h2>
      <div className="grid2">
        {live.map((l) => (
          <label className="field" key={l.id}><span>Rate for {l.item_name} ({l.pack_size}) × {l.qty}</span>
            <input type="number" min="0" step="0.01" inputMode="decimal" value={rates[l.id]} onChange={(e) => setRates({ ...rates, [l.id]: e.target.value })} />
            <span className="hint">{l.estimated ? 'Estimated by the requester. Confirm the rate agreed with the vendor.' : 'From the vendor rate card. A change is recorded in the log.'}</span></label>
        ))}
        <label className="field"><span>Deliver by</span><input type="date" min={today()} value={due} onChange={(e) => setDue(e.target.value)} /></label>
        <label className="field"><span>Payment terms</span>
          <select value={terms} onChange={(e) => setTerms(e.target.value)}>{['Net 15', 'Net 30', 'Net 45', 'Advance'].map((t) => <option key={t}>{t}</option>)}</select></label>
      </div>
      <p className="sub">PO value incl. GST: <b>{inr(total)}</b></p>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn" disabled={a.busy} onClick={() => a.run(() => post(`/documents/${doc.id}/po`, {
        rates: Object.fromEntries(Object.entries(rates).map(([k, v]) => [k, +v])), deliver_by: due, payment_terms: terms,
      }), (r: any) => { onDone(); return r.message })}>Issue PO to {doc.vendor}</button></div>
    </section>
  )
}
