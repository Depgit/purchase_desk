// The vendor's purchase orders: the list, newest open ones first, and one PO with what the vendor can do on it now:
// accept (with a dispatch date), decline with a reason, move the dispatch date, and upload its invoice.
import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { ApiError } from '../api/client'
import { dateTime, day, inr, qty, today } from '../format/format'
import { Errors } from '../ui/Errors'
import { Pill } from '../ui/Pill'
import { Seg } from '../ui/Tabs'
import { useToast } from '../ui/Toast'
import { vfileUrl, vget, vpost, vupload } from './portalApi'

interface OrderRow {
  id: number; po_number: string; po_date: string; property: string; city: string; deliver_by: string; payment_terms: string
  total: number; received: number; status: string; vendor_status: string; dispatch_date: string
}
interface Order {
  id: number; po_number: string; po_date: string; property: string; city: string; deliver_by: string; payment_terms: string
  status: string; status_label: string; vendor_status: string; vendor_note: string; dispatch_date: string; po_closed_short: boolean; total: number
  lines: { sku_code: string; item: string; pack_size: string; hsn: string; qty: number; rate: number; gst_pct: number; value: number; received: number; rejected: number }[]
  invoices: { id: number; invoice_number: string; invoice_date: string; amount: number; file_id: number; note: string; goods_receipt_id: number | null; created_at: string }[]
  receipts: { number: string; received_at: string; items: string }[]
  actions: string[]
}

/** The vendor's words for where a PO stands. */
function stage(r: { status: string; vendor_status: string; dispatch_date: string }) {
  if (r.status === 'PO_ISSUED') {
    if (r.vendor_status === 'declined') return <Pill tone="bad">Declined</Pill>
    if (r.vendor_status === 'accepted') return <Pill tone="ok">{r.dispatch_date ? 'Accepted · dispatch ' + day(r.dispatch_date) : 'Accepted'}</Pill>
    return <Pill tone="warn">New, to accept</Pill>
  }
  if (r.status === 'RECEIVED') return <Pill tone="ok">Delivered</Pill>
  if (r.status === 'CLOSED') return <Pill tone="ok">Paid and closed</Pill>
  return <Pill tone="off">Cancelled</Pill>
}

function useLoad<T>(path: string) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const reload = useCallback(() => { vget<T>(path).then(setData).catch((e) => setError(e.message)) }, [path])
  useEffect(reload, [reload])
  return { data, error, reload }
}

export function VendorOrdersPage() {
  const { data, error } = useLoad<OrderRow[]>('/orders')
  const [show, setShow] = useState<'open' | 'all'>('open')
  const nav = useNavigate()
  const rows = (data || []).filter((r) => show === 'all' || r.status === 'PO_ISSUED')
  const fresh = (data || []).filter((r) => r.status === 'PO_ISSUED' && !r.vendor_status).length
  return (
    <>
      <div className="head"><div><h1>Purchase orders</h1>
        <p className="sub">{fresh ? `${fresh} new order${fresh === 1 ? '' : 's'} to accept.` : 'Open orders first. Tap one to accept it, give the dispatch date or upload the invoice.'}</p></div></div>
      <Seg label="Show" value={show} onChange={setShow} items={[['open', 'Open'], ['all', 'All, last 6 months']]} />
      {error && <p className="note bad">{error}</p>}
      {!data ? <p className="sub">Loading…</p> : !rows.length ? <p className="note">No {show === 'open' ? 'open ' : ''}purchase orders.</p> : (
        <div className="vp-list">{rows.map((r) => (
          <button key={r.id} className="card vp-row" onClick={() => nav('/vendor/orders/' + r.id)}>
            <span><b>{r.po_number}</b><span className="hint">{r.property}, {r.city} · {day(r.po_date)}</span></span>
            <span className="vp-meta"><span>{inr(r.total)}</span><span className="hint">Deliver by {day(r.deliver_by)}</span>{stage(r)}</span>
          </button>))}
        </div>)}
    </>
  )
}

export function VendorOrderPage() {
  const { id } = useParams()
  const { data: o, error, reload } = useLoad<Order>('/orders/' + id)
  if (error) return <p className="note bad">{error}</p>
  if (!o) return <p className="sub">Loading…</p>
  const can = (a: string) => o.actions.includes(a)
  return (
    <>
      <div className="head"><div><Link className="link" to="/vendor/orders">‹ Orders</Link><h1>{o.po_number}</h1>
        <p className="sub">{o.property}, {o.city} · issued {day(o.po_date)} · deliver by {day(o.deliver_by)} · {o.payment_terms}</p></div>
        <div className="acts">{stage(o)}</div></div>
      {o.vendor_status === 'declined' && <p className="note warn"><b>You declined this order:</b> {o.vendor_note}. Saltstayz will contact you; you can still accept it below if you now can supply it.</p>}
      {o.status === 'RECEIVED' && <p className="note ok"><b>Delivered{o.po_closed_short ? ', closed short' : ''}.</b> Follow the payment under Payments.</p>}
      {can('accept') && <AcceptCard o={o} onDone={reload} />}
      {can('dispatch') && <DispatchCard o={o} onDone={reload} />}
      <section className="card flush"><h2>Items · {inr(o.total)} incl. GST</h2><div className="scroll"><table>
        <thead><tr><th>Item</th><th>HSN</th><th className="r">Qty</th><th className="r">Rate</th><th className="r">GST</th><th className="r">Value</th><th className="r">Received</th></tr></thead>
        <tbody>{o.lines.map((l, i) => <tr key={i}><td>{l.item}<br /><span className="hint">{l.sku_code} · {l.pack_size}</span></td><td>{l.hsn || '—'}</td>
          <td className="r">{qty(l.qty)}{l.rejected > 0 && <><br /><span className="hint">{qty(l.rejected)} cancelled</span></>}</td><td className="r">{inr(l.rate)}</td><td className="r">{l.gst_pct}%</td><td className="r">{inr(l.value)}</td><td className="r">{qty(l.received)}</td></tr>)}</tbody>
      </table></div></section>
      {can('invoice') && <InvoiceCard o={o} onDone={reload} />}
      {o.invoices.length > 0 && <section className="card flush"><h2>Your invoices</h2><div className="scroll"><table>
        <thead><tr><th>Invoice</th><th>Date</th><th className="r">Amount</th><th>Status</th><th></th></tr></thead>
        <tbody>{o.invoices.map((x) => <tr key={x.id}><td className="id">{x.invoice_number}</td><td>{day(x.invoice_date)}</td><td className="r">{inr(x.amount)}</td>
          <td>{x.goods_receipt_id ? <Pill tone="ok">Matched to goods received</Pill> : <Pill>Waiting for the goods</Pill>}</td>
          <td><a className="link" href={vfileUrl(x.file_id)} target="_blank" rel="noreferrer">Open</a></td></tr>)}</tbody></table></div></section>}
      {o.receipts.length > 0 && <section className="card"><h2>Goods received by Saltstayz</h2>
        <ul className="log">{o.receipts.map((g) => <li key={g.number}><b>{g.number}</b><span>{dateTime(g.received_at)}</span><br />{g.items}</li>)}</ul></section>}
    </>
  )
}

function useRun(onDone: () => void) {
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const [errors, setErrors] = useState<string[]>([])
  const run = async (fn: () => Promise<{ message: string }>) => {
    setBusy(true); setErrors([])
    try { const r = await fn(); toast(r.message); onDone() } catch (e) { setErrors(e instanceof ApiError ? e.errors : [String(e)]) } finally { setBusy(false) }
  }
  return { busy, errors, run }
}

function AcceptCard({ o, onDone }: { o: Order; onDone: () => void }) {
  const [mode, setMode] = useState<'accept' | 'decline'>('accept')
  const [date, setDate] = useState(o.dispatch_date || '')
  const [note, setNote] = useState('')
  const [reason, setReason] = useState('')
  const a = useRun(onDone)
  const canDecline = o.actions.includes('decline')
  return (
    <section className="card act">
      <h2>{o.vendor_status === 'declined' ? 'Accept this order after all?' : 'Can you supply this order?'}</h2>
      {canDecline && <Seg label="Answer" value={mode} onChange={setMode} items={[['accept', 'Yes, accept'], ['decline', 'No, decline']]} />}
      {mode === 'accept' ? <>
        <div className="grid2">
          <label className="field"><span>Dispatch date</span><input type="date" min={today()} value={date} onChange={(e) => setDate(e.target.value)} />
            <span className="hint">When the goods leave you. You can change it later.</span></label>
          <label className="field"><span>Note (optional)</span><input value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. delivery in two lots" /></label>
        </div>
        <Errors list={a.errors} />
        <div className="btns"><button className="btn" disabled={a.busy} onClick={() => a.run(() => vpost(`/orders/${o.id}/accept`, { dispatch_date: date, note }))}>Accept order</button></div>
      </> : <>
        <label className="field"><span>Why can you not supply it?</span><textarea rows={3} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. out of stock until next month" /></label>
        <Errors list={a.errors} />
        <div className="btns"><button className="btn no" disabled={a.busy || reason.trim().length < 5} onClick={() => a.run(() => vpost(`/orders/${o.id}/decline`, { reason }))}>Decline order</button></div>
      </>}
    </section>
  )
}

function DispatchCard({ o, onDone }: { o: Order; onDone: () => void }) {
  const [date, setDate] = useState(o.dispatch_date || '')
  const [note, setNote] = useState('')
  const a = useRun(onDone)
  return (
    <section className="card">
      <h2>Dispatch date</h2>
      <p className="sub">{o.dispatch_date ? `You said you dispatch on ${day(o.dispatch_date)}. Change it if the plan moves.` : 'Tell Saltstayz when the goods will leave you.'}</p>
      <div className="grid2">
        <label className="field"><span>Dispatch date</span><input type="date" min={today()} value={date} onChange={(e) => setDate(e.target.value)} /></label>
        <label className="field"><span>Note (optional)</span><input value={note} onChange={(e) => setNote(e.target.value)} /></label>
      </div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" disabled={a.busy || !date || date === o.dispatch_date} onClick={() => a.run(() => vpost(`/orders/${o.id}/dispatch`, { dispatch_date: date, note }))}>Save dispatch date</button></div>
    </section>
  )
}

function InvoiceCard({ o, onDone }: { o: Order; onDone: () => void }) {
  const [no, setNo] = useState('')
  const [date, setDate] = useState(today())
  const [amt, setAmt] = useState('')
  const [note, setNote] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const a = useRun(() => { setNo(''); setAmt(''); setNote(''); setFile(null); onDone() })
  const open = o.lines.reduce((t, l) => t + Math.max(0, l.qty - l.received) * l.rate * (1 + l.gst_pct / 100), 0)
  return (
    <section className="card">
      <h2>Upload your invoice</h2>
      <p className="sub">Saltstayz's store checks it against what arrives. Upload the same invoice number again to correct it before the goods arrive.</p>
      <div className="grid2">
        <label className="field"><span>Invoice number</span><input value={no} onChange={(e) => setNo(e.target.value)} /></label>
        <label className="field"><span>Invoice date</span><input type="date" max={today()} value={date} onChange={(e) => setDate(e.target.value)} /></label>
        <label className="field"><span>Amount incl. GST (₹)</span><input type="number" min="0" step="0.01" inputMode="decimal" value={amt} onChange={(e) => setAmt(e.target.value)} />
          <span className="hint">Still to deliver on this order: {inr(open)}</span></label>
        <label className="field"><span>Invoice PDF or photo</span><input type="file" accept="image/*,application/pdf" onChange={(e) => setFile(e.target.files?.[0] || null)} /></label>
        <label className="field"><span>Note (optional)</span><input value={note} onChange={(e) => setNote(e.target.value)} /></label>
      </div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn" disabled={a.busy} onClick={() => a.run(() => vupload(`/orders/${o.id}/invoice`, { invoice_number: no, invoice_date: date, amount: +amt, note }, 'invoice', file))}>Upload invoice</button></div>
    </section>
  )
}
