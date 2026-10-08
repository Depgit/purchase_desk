// One invoice: ordered, received and invoiced side by side; confirm the match, accept a difference with a remark,
// or send it back to the store. Finance approves payment here too.
import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { fileUrl, post } from '../api/client'
import type { Invoice } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { day, inr, qty } from '../format/format'
import { Errors } from '../ui/Errors'
import { Pill, statusTone } from '../ui/Pill'
import { useAction } from '../ui/useAction'
import { PHASE, usePhase } from '../releasephase/phase'
import { ZohoPreview } from '../payments/ZohoPreview'

export function InvoicePage() {
  const { id } = useParams()
  const { data: x, reload, error } = useApi<Invoice>(`/invoices/${id}`)
  const { hasStep } = useSession()
  const zoho = usePhase().on(PHASE.zoho)
  const [remark, setRemark] = useState('')
  const [preview, setPreview] = useState(false)
  const a = useAction()
  if (error) return <p className="note bad">{error.message}</p>
  if (!x) return <p className="sub">Loading…</p>
  const done = (r: any) => { reload(); return r.message }
  return (
    <>
      <div className="head"><div><Link className="link" to={'/documents/' + x.document_id}>‹ {x.po_number || x.document_number}</Link>
        <h1>Invoice {x.invoice_number}</h1><p className="sub">{x.vendor} · {x.property} · dated {day(x.invoice_date)}{x.grn_number ? ` · ${x.grn_number}, delivery ${x.delivery_no}` : ''}</p></div>
        <Pill tone={statusTone(x.status)}>{x.status.replace('_', ' ').toLowerCase()}</Pill></div>
      <div className="cols">
        <div className="stack">
          {x.status === 'RECEIVED' && hasStep('ap') && (
            <section className="card act"><h2>Three-way match</h2>
              <div className="scroll"><table><thead><tr><th>Purchase order</th><th>Goods received</th><th>Invoice</th><th className="r">Difference</th></tr></thead>
                <tbody><tr><td className="num">{inr(x.ordered_value)}</td><td className="num">{inr(x.received_value)}</td><td className="num">{inr(x.amount)}</td><td className="r">{inr(Math.abs(x.difference))}</td></tr></tbody></table></div>
              {x.within_tolerance ? <p className="note ok">The invoice matches what was received, within the tolerance.</p>
                : <p className="note bad">The invoice is {inr(Math.abs(x.difference))} {x.difference > 0 ? 'more' : 'less'} than the value of goods received.</p>}
              <label className="field"><span>Remark{x.within_tolerance ? ' (optional)' : ''}</span><textarea value={remark} onChange={(e) => setRemark(e.target.value)} placeholder={x.within_tolerance ? '' : 'Needed to accept the difference'} /></label>
              <Errors list={a.errors} />
              <div className="btns">
                <button className="btn ghost" disabled={a.busy} onClick={() => a.run(() => post(`/invoices/${x.id}/send-back`, { remark }), done)}>Send back to store</button>
                <button className="btn" disabled={a.busy} onClick={() => a.run(() => post(`/invoices/${x.id}/match`, { remark }), done)}>{x.within_tolerance ? 'Confirm match' : 'Accept difference'}</button>
              </div>
            </section>
          )}
          {x.status === 'MATCHED' && hasStep('finance') && (
            <section className="card act"><h2>Payment approval</h2>
              <dl className="kv"><dt>Payable</dt><dd>{inr(x.amount)}</dd><dt>Terms</dt><dd>{x.payment_terms} · pay by {day(x.pay_by)}</dd></dl>
              {x.vendor_msme && <p className="note warn">MSME vendor: pay by {day(x.msme_limit)} at the latest.</p>}
              {!x.vendor_gst_registered && <p className="note warn">Unregistered vendor: no GST is charged.</p>}
              <Errors list={a.errors} />
              <div className="btns"><button className="btn" disabled={a.busy} onClick={() => a.run(() => post(`/invoices/${x.id}/approve-payment`), done)}>Approve for payment</button></div>
            </section>
          )}
          {x.status === 'PAY_APPROVED' && !zoho && (
            <section className="card act"><h2>Payment approved</h2>
              <p className="note ok">Finance approved this invoice for payment. After the bank transfer, the System Admin marks it paid under Payments.</p>
              <div className="btns"><Link className="btn" to="/payments">Go to Payments</Link></div>
            </section>
          )}
          {x.status === 'PAY_APPROVED' && zoho && (
            <section className="card act"><h2>Push to Zoho</h2>
              {x.held.length ? <p className="note bad"><b>Held back:</b> {x.held.join('; ')}. Fix these under Settings › Zoho Books.</p> : <p className="note ok">All Zoho mappings are in place for this bill.</p>}
              {x.zoho_error && <p className="note warn">Last attempt: {x.zoho_error}</p>}
              <div className="btns"><button className="btn ghost" onClick={() => setPreview(true)}>Preview what is sent</button>
                <Link className="btn" to="/payments">Go to Payments</Link></div>
            </section>
          )}
          {x.lines && x.lines.length > 0 && (
            <section className="card flush"><h2>Items in this delivery</h2>
              <div className="scroll"><table><thead><tr><th>Item</th><th className="r">Ordered</th><th className="r">Received</th><th className="r">Rate</th><th className="r">GST</th><th className="r">Value</th></tr></thead>
                <tbody>{x.lines.map((l) => <tr key={l.line_id}><td>{l.item}<br /><span className="hint">{l.sku_code} · {l.pack_size}{l.hsn ? ' · HSN ' + l.hsn : ''}</span></td>
                  <td className="r">{qty(l.ordered)}</td><td className="r">{qty(l.received)}</td><td className="r">{inr(l.rate)}</td><td className="r">{l.gst_pct}%</td><td className="r">{inr(l.value)}</td></tr>)}</tbody></table></div>
            </section>
          )}
        </div>
        <div className="stack">
          <section className="card"><h2>Details</h2>
            <dl className="kv">
              <dt>Invoice amount</dt><dd>{inr(x.amount)}</dd><dt>Received value</dt><dd>{x.kind === 'bill' ? '—' : inr(x.received_value)}</dd>
              <dt>Pay by</dt><dd>{day(x.pay_by)}</dd>{x.match_remark && <><dt>Match remark</dt><dd>{x.match_remark}</dd></>}
              {zoho && <><dt>Zoho bill</dt><dd>{x.zoho_bill_id || '—'}{x.zoho_status ? ` · ${x.zoho_status}` : ''}</dd></>}
              {x.zoho_balance != null && <><dt>Balance in Zoho</dt><dd>{inr(x.zoho_balance)}</dd></>}
              {x.paid_at && <><dt>Paid</dt><dd>{day(x.paid_at)}{x.payment_ref ? ' · ' + x.payment_ref : ''}</dd></>}
              <dt>File</dt><dd>{x.file_id ? <a className="link" href={fileUrl(x.file_id)} target="_blank" rel="noreferrer">Open invoice</a> : '—'}</dd>
            </dl>
          </section>
        </div>
      </div>
      {preview && <ZohoPreview invoiceId={x.id} onClose={() => setPreview(false)} />}
    </>
  )
}
