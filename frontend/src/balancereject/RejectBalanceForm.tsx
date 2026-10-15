// Reject the undelivered balance of a PO when the vendor will not deliver it: per item (all of what is still due by
// default), a reason and a remark. It sits in the page, in place of the receive form, and says before saving what
// will happen: the vendor and requester are told, the value leaves the budget, and the PO stays open, closes or is
// cancelled.
import { useEffect, useState } from 'react'
import { get, post } from '../api/client'
import type { Doc } from '../api/types'
import { inr, lineValue, qty } from '../format/format'
import { Errors } from '../ui/Errors'
import { usePopup } from '../ui/Toast'
import { useAction } from '../ui/useAction'

export function RejectBalanceForm({ doc, onCancel, onDone }: { doc: Doc; onCancel: () => void; onDone: () => void }) {
  const due = doc.lines.filter((l) => l.qty - l.received_qty > 0)
  const [q, setQ] = useState<Record<number, string>>(Object.fromEntries(due.map((l) => [l.id, String(l.qty - l.received_qty)])))
  const [reasons, setReasons] = useState<string[]>([])
  const [reason, setReason] = useState('')
  const [note, setNote] = useState('')
  const a = useAction()
  const popup = usePopup()
  useEffect(() => { get<Record<string, string[]>>('/pick-lists').then((l) => setReasons(l.balance_reasons || [])).catch(() => {}) }, [])
  const n = (id: number) => Math.max(0, +q[id] || 0)
  const left = due.reduce((t, l) => t + (l.qty - l.received_qty - Math.min(n(l.id), l.qty - l.received_qty)), 0)
  const value = due.reduce((t, l) => t + lineValue(Math.min(n(l.id), l.qty - l.received_qty), l.rate, l.gst_pct), 0)
  const any = due.some((l) => n(l.id) > 0)
  const delivered = doc.lines.some((l) => l.received_qty > 0)
  const other = reason.startsWith('Other')
  const po = doc.po_number || doc.number
  const outcome = left > 0 ? `${po} stays open for the ${qty(left)} still to come.`
    : delivered ? `Nothing will be left due, so ${po} closes with what was delivered.` : `Nothing was delivered, so ${po} is cancelled.`
  return (
    <>
      <div className="phead"><div><h2>Vendor will not deliver the rest</h2>
        <p className="sub">Choose how much of each item to reject. It comes off {po}; to still get it, raise a new request.</p></div></div>
      <div className="scroll"><table>
        <thead><tr><th>Item</th><th className="r nophone">Ordered</th><th className="r nophone">Received</th><th className="r">Still due</th><th className="r">Reject</th></tr></thead>
        <tbody>{due.map((l) => {
          const open = l.qty - l.received_qty
          return (
            <tr key={l.id} className={n(l.id) === 0 ? 'zero' : ''}><td>{l.item_name}<br /><span className="hint">{l.sku_code} · {l.pack_size}</span></td>
              <td className="r nophone">{qty(l.qty + (l.rejected_qty || 0))}</td><td className="r nophone">{qty(l.received_qty)}</td><td className="r"><b>{qty(open)}</b></td>
              <td className="r"><input className="qty" type="number" min="0" max={open} inputMode="decimal" aria-label={'Reject ' + l.item_name}
                value={q[l.id]} onChange={(e) => setQ({ ...q, [l.id]: e.target.value })} />
                {n(l.id) !== open && <><br /><button className="link" onClick={() => setQ({ ...q, [l.id]: String(open) })}>All {qty(open)}</button></>}</td></tr>)
        })}</tbody></table></div>
      <div className="grid2">
        <label className="field"><span>Reason</span>
          <select value={reason} onChange={(e) => setReason(e.target.value)}><option value="">Choose a reason</option>{reasons.map((r) => <option key={r}>{r}</option>)}</select></label>
        <label className="field"><span>Remark{other ? '' : ' (optional)'}</span><input value={note} onChange={(e) => setNote(e.target.value)} placeholder={other ? 'Needed for Other' : ''} /></label>
      </div>
      {any && <div className="outcome bad">
        <div><b>Rejecting {inr(value)}.</b> {outcome}</div>
        <div>The vendor is emailed not to supply it, the requester is told, and {inr(value)} is released from the budget.</div></div>}
      <Errors list={a.errors} />
      <div className="btns">
        <button className="btn ghost" onClick={onCancel}>Back</button>
        <button className="btn no" disabled={a.busy || !any || !reason || (other && note.trim().length < 3)} onClick={() => a.run(() => post(`/documents/${doc.id}/reject-balance`, {
          quantities: Object.fromEntries(Object.entries(q).map(([k, v]) => [k, +v || 0])), reason, note,
        }), (r: any) => { onDone(); if (r.closed) popup('PO closed', r.closed); return r.message })}>Reject {inr(value)}</button>
      </div>
    </>
  )
}

// For Purchase or the Admin when they do not receive goods themselves: what is still due, and the reject form on demand.
export function BalanceDuePanel({ doc, onDone }: { doc: Doc; onDone: () => void }) {
  const due = doc.lines.filter((l) => l.qty - l.received_qty > 0)
  const [open, setOpen] = useState(false)
  if (!due.length) return null
  return (
    <section className={'card' + (open ? ' act' : '')}>
      {open ? <RejectBalanceForm doc={doc} onCancel={() => setOpen(false)} onDone={() => { setOpen(false); onDone() }} /> : <>
        <div className="phead"><div><h2>Balance still due</h2>
          <p className="sub">{due.length === 1 ? '1 item is' : `${due.length} items are`} still to come on {doc.po_number}.</p></div>
          <button className="btn no small" onClick={() => setOpen(true)}>Vendor won't deliver</button></div>
        <div className="scroll"><table><thead><tr><th>Item</th><th className="r">Still due</th></tr></thead>
          <tbody>{due.map((l) => <tr key={l.id}><td>{l.item_name} <span className="hint">{l.sku_code}</span></td><td className="r">{qty(l.qty - l.received_qty)}</td></tr>)}</tbody></table></div></>}
    </section>
  )
}
