// The vendor's payments: each invoice and where it stands (being checked, approved with its pay-by date, or paid with
// the reference), and the advances on its orders.
import { useEffect, useState } from 'react'
import { day, inr } from '../format/format'
import { Pill } from '../ui/Pill'
import { vget } from './portalApi'

interface Payments {
  due: number; paid: number
  invoices: { invoice_number: string; invoice_date: string; po_number: string; property: string; amount: number; tds_amount: number; advance_adjusted: number
    net_payable: number; stage: string; pay_by: string; paid_at: string | null; payment_ref: string }[]
  advances: { number: string; po_number: string; amount: number; tds_amount: number; status: string; paid_on: string | null; payment_ref: string; adjusted: number }[]
}

export function VendorPaymentsPage() {
  const [p, setP] = useState<Payments | null>(null)
  const [error, setError] = useState('')
  useEffect(() => { vget<Payments>('/payments').then(setP).catch((e) => setError(e.message)) }, [])
  if (error) return <p className="note bad">{error}</p>
  if (!p) return <p className="sub">Loading…</p>
  const tone = (s: string) => s === 'Paid' ? 'ok' : s.startsWith('Approved') ? 'warn' : ''
  return (
    <>
      <div className="head"><div><h1>Payments</h1><p className="sub">Your invoices with Saltstayz and where each one stands. Paid invoices from the last 12 months.</p></div></div>
      <div className="tiles">
        <div className="tile"><span>Approved, to be paid</span><b>{inr(p.due)}</b></div>
        <div className="tile"><span>Paid, last 12 months</span><b>{inr(p.paid)}</b></div>
      </div>
      <section className="card flush"><h2>Invoices</h2>{!p.invoices.length ? <p className="sub" style={{ padding: '0 16px 16px' }}>No invoices yet.</p> : <div className="scroll"><table>
        <thead><tr><th>Invoice</th><th>Order</th><th className="r">Amount</th><th className="r">TDS</th><th className="r">Advance set off</th><th className="r">You get</th><th>Status</th></tr></thead>
        <tbody>{p.invoices.map((x) => <tr key={x.invoice_number + x.po_number}>
          <td className="id">{x.invoice_number}<br /><span className="hint">{day(x.invoice_date)}</span></td>
          <td>{x.po_number || '—'}<br /><span className="hint">{x.property}</span></td>
          <td className="r">{inr(x.amount)}</td><td className="r">{x.tds_amount ? inr(x.tds_amount) : '—'}</td><td className="r">{x.advance_adjusted ? inr(x.advance_adjusted) : '—'}</td>
          <td className="r"><b>{inr(x.net_payable)}</b></td>
          <td><Pill tone={tone(x.stage)}>{x.stage}</Pill>{x.pay_by && <><br /><span className="hint">by {day(x.pay_by)}</span></>}
            {x.paid_at && <><br /><span className="hint">{day(x.paid_at)}{x.payment_ref ? ' · ' + x.payment_ref : ''}</span></>}</td></tr>)}</tbody>
      </table></div>}</section>
      {p.advances.length > 0 && <section className="card flush"><h2>Advances</h2><div className="scroll"><table>
        <thead><tr><th>Advance</th><th>Order</th><th className="r">Amount</th><th className="r">Set off so far</th><th>Status</th></tr></thead>
        <tbody>{p.advances.map((a) => <tr key={a.number}><td className="id">{a.number}</td><td>{a.po_number}</td><td className="r">{inr(a.amount - a.tds_amount)}{a.tds_amount > 0 && <><br /><span className="hint">after TDS {inr(a.tds_amount)}</span></>}</td>
          <td className="r">{a.adjusted ? inr(a.adjusted) : '—'}</td>
          <td><Pill tone={a.status === 'Paid' ? 'ok' : 'warn'}>{a.status}</Pill>{a.paid_on && <><br /><span className="hint">{day(a.paid_on)}{a.payment_ref ? ' · ' + a.payment_ref : ''}</span></>}</td></tr>)}</tbody>
      </table></div></section>}
    </>
  )
}
