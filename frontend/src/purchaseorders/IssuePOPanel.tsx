// The Purchase Team converts an approved request into a PO: confirms each rate, the delivery date and payment terms.
// With Advance terms the advance is asked of Finance in the same step, saved together with the PO.
import { useState } from 'react'
import { post, upload } from '../api/client'
import { AdvanceFields, advanceAmount, advanceBody, advanceProblem, newAdvance } from '../advances/AdvanceFields'
import type { Doc } from '../api/types'
import { addDays, inr, today } from '../format/format'
import { Errors } from '../ui/Errors'
import { PAYMENT_TERMS } from '../api/types'
import { useAction } from '../ui/useAction'

export function IssuePOPanel({ doc, onDone }: { doc: Doc; onDone: () => void }) {
  const live = doc.lines.filter((l) => l.qty > 0)
  const [rates, setRates] = useState<Record<number, string>>(Object.fromEntries(live.map((l) => [l.id, String(l.rate)])))
  const [due, setDue] = useState(addDays(today(), 3))
  const [terms, setTerms] = useState(doc.vendor_terms || 'Net 30')
  const [adv, setAdv] = useState(newAdvance)
  const a = useAction()
  const total = Math.round(live.reduce((t, l) => t + l.qty * (+rates[l.id] || 0) * (1 + l.gst_pct / 100), 0) * 100) / 100
  const advance = terms === 'Advance'
  const problem = advance ? advanceProblem(adv, total, total) : ''
  const body = { rates: Object.fromEntries(Object.entries(rates).map(([k, v]) => [k, +v])), deliver_by: due, payment_terms: terms }
  const issue = () => advance
    ? upload(`/documents/${doc.id}/po`, { ...body, advance: advanceBody(adv, total) }, { proforma: adv.file })
    : post(`/documents/${doc.id}/po`, body)
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
          <select value={terms} onChange={(e) => setTerms(e.target.value)}>{PAYMENT_TERMS.map((t) => <option key={t}>{t}</option>)}</select></label>
      </div>
      <p className="sub">PO value incl. GST: <b>{inr(total)}</b></p>
      {advance && <div className="xpbox">
        <h3>Advance to ask Finance for</h3>
        <p className="hint">The PO is not raised yet: it waits until Finance approves and pays this advance (marked paid with the UTR, or read from Zoho), and only then goes to the vendor. The advance is adjusted on the vendor's invoice.</p>
        <AdvanceFields a={adv} onChange={setAdv} poValue={total} room={total} poLabel="this PO" />
      </div>}
      <Errors list={a.errors} />
      <div className="btns">{problem && <span className="hint">{problem}</span>}<button className="btn" disabled={a.busy || !!problem} onClick={() => a.run(issue, (r: any) => { onDone(); return r.message })}>
        {advance ? `Send ${problem ? 'advance' : inr(advanceAmount(adv, total)) + ' advance'} to Finance; PO waits for approval` : `Issue PO to ${doc.vendor}`}</button></div>
    </section>
  )
}
