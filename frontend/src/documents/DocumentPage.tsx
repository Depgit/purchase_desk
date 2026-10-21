// One purchase request or bill: where it stands, what was asked and changed, and the next action for the signed-in user.
import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { fileUrl, get, post } from '../api/client'
import { useApi } from '../api/useApi'
import { AdvancesCard } from '../advances/AdvancesCard'
import { PAYMENT_TERMS } from '../api/types'
import type { Doc, Invoice } from '../api/types'
import { ReasonModal, type ReasonKind } from '../approvals/ReasonModal'
import { useSession } from '../auth/session'
import { addDays, dateTime, day, inr, lineValue, month, qty, today } from '../format/format'
import { AdvanceFields, advanceBody, advanceProblem, newAdvance, type AdvanceDraft } from '../advances/AdvanceFields'
import { ReceivePanel, type SentBack } from '../goodsreceipts/ReceivePanel'
import { BalanceDuePanel } from '../balancereject/RejectBalanceForm'
import { IssuePOPanel } from '../purchaseorders/IssuePOPanel'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Pill, statusTone } from '../ui/Pill'
import { useAction } from '../ui/useAction'
import { BudgetBox } from './BudgetBox'
import { DispatchPanel } from '../warehouse/DispatchPanel'
import { ReceiveTransferPanel } from '../warehouse/ReceiveTransferPanel'
import { VendorResponse, type PortalInvoice } from '../vendorportal/VendorResponse'

interface Detail {
  document: Doc
  log: { at: string; user: string; role: string; action: string; note: string }[]
  receipts: { id: number; number: string; delivery_no: number; received_by: string; received_at: string; photo_file_ids: number[]; voided: boolean; void_reason: string; invoice_id: number | null; lines: { line_id: number; qty: number }[] }[]
  actions: string[]
  raised_with: { id: number; number: string; po_number: string; vendor: string; items: number; status: string }[]
  owner_people: string[] | null
  vendor_invoices: PortalInvoice[]
  vendor_portal: boolean
  /** Approving now is the last step and raises the PO (no Purchase Team step). */
  raises_po: boolean
  /** Approving now goes this far over the month budget, so the approver must say why (0 = no reason needed). */
  over_budget: number
  /** Advances already paid to this vendor and carried forward from earlier POs: adjusted on its next invoice payment. */
  vendor_advances: { number: string; po_number: string; open: number }[]
  /** The vendor's balance now (its statement's closing balance): negative = advances paid, not yet billed. */
  vendor_balance: number | null
}

function stepper(d: Doc, invoices: Invoice[]) {
  const names = d.kind === 'bill' ? ['Bill', 'Approval', 'Payment', 'Paid'] : d.is_transfer ? ['Request', 'Approval', 'Transfer order', 'Dispatch', 'Receipt'] : ['Request', 'Approval', 'PO', 'Receipt', 'Match', 'Payment']
  let st = 1
  const stop = ['REJECTED', 'CANCELLED', 'WH_BACK'].includes(d.status)
  if (d.kind === 'bill') st = d.status === 'CLOSED' ? 4 : d.status === 'APPROVED' ? 2 : 1
  else if (d.is_transfer) st = { PO_ISSUED: 3, WH_SENT: 4, WH_DONE: 5 }[d.status] ?? 1
  else {
    if (d.status === 'APPROVED') st = 2
    if (d.status === 'PO_ISSUED') st = invoices.length ? 4 : 3
    if (d.status === 'RECEIVED') st = invoices.every((i) => !['RECEIVED'].includes(i.status)) ? 5 : 4
    if (d.status === 'CLOSED') st = 6
  }
  return <div className="steps">{names.map((n, i) => <div key={n} className={i < st ? 'done' : i === st ? (stop ? 'stop' : 'now') : ''}>{n}</div>)}</div>
}

export function DocumentPage() {
  const { id } = useParams()
  const { me } = useSession()
  const nav = useNavigate()
  const { data, reload, error } = useApi<Detail>(`/documents/${id}`)
  const inv = useApi<Invoice[]>(`/invoices?document_id=${id}`)
  const refresh = () => { reload(); inv.reload() }
  const [extend, setExtend] = useState(false) // the Extend delivery date dialog (hooks stay above the early returns)
  if (error) return <p className="note bad">{error.message}</p>
  if (!data) return <p className="sub">Loading…</p>
  const d = data.document
  const bill = d.kind === 'bill' || d.kind === 'corp_bill'
  const corp = d.kind === 'corp_purchase' || d.kind === 'corp_bill'
  const live = d.lines.filter((l) => l.qty > 0 || l.original_qty || l.rejected_qty > 0)
  const hasRcv = d.lines.some((l) => l.received_qty > 0)
  const rejected = d.lines.filter((l) => l.rejected_qty > 0)
  const mine = d.kind === 'purchase' && d.requester_id === me?.id
  return (
    <>
      <div className="head">
        <div><button className="link" onClick={() => nav(-1)}>‹ Back</button>
          <h1>{d.po_number || d.number}</h1>
          <p className="sub">{d.po_number ? d.number + ' · ' : ''}raised {day(d.created_at)} by {d.requester}</p></div>
        <div className="acts">
          {data.actions.includes('edit') && <button className="btn ghost" onClick={() => nav(`/requests/${d.id}/edit`)}>Edit</button>}
          {mine && <button className="btn ghost" onClick={() => nav(`/requests/new?clone=${d.id}`)} title="Start a new request with the same property and items">Clone</button>}
          {d.kind === 'bill' && <button className="btn ghost" onClick={() => nav(`/bills/new/${d.bill_section}?clone=${d.id}`)} title="Start the next bill with the same property, type and vendor">Clone</button>}
          {(d.kind === 'corp_purchase' || d.kind === 'corp_bill') && d.requester_id === me?.id && <button className="btn ghost" onClick={() => nav(`/corporate/new/${d.kind === 'corp_bill' ? 'bill' : 'purchase'}?clone=${d.id}`)} title="Start a new one with the same department, vendor and items">Clone</button>}
          <Pill tone={statusTone(d.status)}>{d.status_label}</Pill></div>
      </div>
      {stepper(d, inv.data || [])}
      {d.modified_note && <p className="note warn"><b>Quantity changed by {d.modified_by}{d.modified_at ? ' on ' + day(d.modified_at) : ''}.</b> {d.modified_note}</p>}
      {d.event_reason && <p className="note"><b>Extra quantity for an event:</b> {d.event_reason} · {d.event_note}{d.cm_approved_at ? ' · approved by the Cluster Manager on ' + day(d.cm_approved_at) : ''}</p>}
      {d.routed_reason && <p className="note"><b>Sent to the Cluster Manager by the Cost Controller:</b> {d.routed_reason}{d.routed_note ? ' · ' + d.routed_note : ''}</p>}
      {(d.property_kind === 'preopening' || d.property_kind === 'warehouse') && d.status === 'PENDING' && <p className="note"><b>{d.property_kind === 'preopening' ? 'Pre-opening property order' : 'Warehouse stock order'}.</b> Waiting for {d.owner === 'preop' ? (d.preop_user || 'the Pre-opening Team') : 'the ' + d.owner_name}.</p>}
      {d.over_budget_reason && <p className="note warn"><b>Approved over the month budget by {d.over_budget_by}:</b> {d.over_budget_reason}</p>}
      {rejected.length > 0 && <p className="note warn"><b>Undelivered balance rejected:</b> {rejected.map((l) => `${l.item_name} ${qty(l.rejected_qty)}`).join(', ')}. It will not be supplied on this PO; the reason is in the history. Raise a new request if it is still needed.</p>}
      {d.status === 'RETURNED' && <p className="note warn"><b>Returned:</b> {d.return_reason}{d.return_note ? ' · ' + d.return_note : ''}</p>}
      {d.status === 'REJECTED' && <p className="note bad"><b>Rejected:</b> {d.return_reason}{d.return_note ? ' · ' + d.return_note : ''}</p>}
      {d.bill_flags?.map((f) => <p key={f} className="note warn">{f}</p>)}
      {data.raised_with?.length > 0 && <div className="note"><b>This order was split by vendor.</b> The request had {live.length + data.raised_with.reduce((t, x) => t + x.items, 0)} items; {d.vendor} supplies the {live.length} below. The rest are on {data.raised_with.length === 1 ? 'this order' : 'these orders'}:
        <ul>{data.raised_with.map((x) => <li key={x.id}><Link to={'/documents/' + x.id}>{x.po_number || x.number}</Link> · {x.vendor} · {x.items} item{x.items === 1 ? '' : 's'} · {x.status}</li>)}</ul></div>}
      {d.is_transfer && ['PO_ISSUED', 'WH_SENT'].includes(d.status) && (d.delivery
        ? <p className={'note ' + (d.status === 'WH_SENT' ? 'ok' : '')}><b>{d.status === 'WH_SENT' ? 'On the way' : 'Delivery scheduled'}{d.delivery.version > 1 ? ' (rescheduled)' : ''}:</b> {day(d.delivery.date)}, {d.delivery.slot} · {d.delivery.partner}{d.delivery.vehicle ? ' · vehicle ' + d.delivery.vehicle : ''} · {d.delivery.driver}, {d.delivery.phone}{d.delivery.note ? ' · ' + d.delivery.note : ''}</p>
        : d.wait_until ? <p className="note warn"><b>The warehouse is waiting for stock.</b> Expected by {day(d.wait_until)}{d.wait_note ? ' · ' + d.wait_note : ''}.</p>
        : <p className="note">The warehouse has this order and has not scheduled the delivery yet.</p>)}
      {d.status === 'WH_BACK' && <p className="note bad"><b>The warehouse could not supply this order:</b> {d.return_reason}{d.return_note ? ' · ' + d.return_note : ''}. The budget is released. Raise it again when stock is back, or ask Purchase to change the mapping.</p>}
      {(data.vendor_advances || []).length > 0 && ['PENDING', 'APPROVED', 'PO_ISSUED'].includes(d.status) && <p className="note"><b>Advance already paid to {d.vendor}: {inr(data.vendor_advances.reduce((t, x) => t + x.open, 0))}</b> ({data.vendor_advances.map((x) => `${x.number} from ${x.po_number}, ${inr(x.open)}`).join('; ')}). It was carried forward from an earlier PO and is adjusted on this vendor's next invoice at payment approval.</p>}
      {d.status === 'PO_ISSUED' && !d.is_transfer && d.deliver_by && <p className="hint">If the goods are not delivered by {day(d.deliver_by)}, the undelivered balance is rejected automatically{d.deliver_by_extended ? '' : ' (Purchase or an approver can extend the date once)'}.</p>}
      {extend && <ExtendModal doc={d} onClose={() => setExtend(false)} onDone={() => { setExtend(false); refresh() }} />}
      <div className="cols">
        <div className="stack">
          <ActionPanel detail={data} invoices={inv.data ?? (inv.error ? [] : null)} onDone={refresh} />
          {(d.kind === 'purchase' || d.kind === 'corp_purchase') && !d.is_transfer && d.po_number &&
            <VendorResponse doc={d} invoices={data.vendor_invoices || []} portal={data.vendor_portal} canConfirm={data.actions.includes('vendor_confirm')} onDone={refresh} />}
          {data.actions.includes('reject_balance') && !data.actions.includes('receive') && <BalanceDuePanel doc={d} onDone={refresh} />}
          {d.kind === 'corp_bill' ? (
            <section className="card"><h2>Corporate bill</h2>
              <dl className="kv"><dt>Department</dt><dd>{d.department}</dd><dt>Expense head</dt><dd>{d.expense_head}</dd><dt>Amount incl. GST</dt><dd>{inr(d.total)}</dd>{d.remark && <><dt>Why</dt><dd>{d.remark}</dd></>}</dl>
            </section>
          ) : bill ? (
            <section className="card"><h2>Bill details</h2>
              <dl className="kv">
                <dt>Type</dt><dd>{d.bill_section === 'amc' ? 'Maintenance AMC' : 'Utility bill'} · {d.bill_type}</dd>
                <dt>Period</dt><dd>{month(d.period_start)}{d.months > 1 ? ` for ${d.months} months` : ''}</dd>
                <dt>Amount incl. GST</dt><dd className="num">{inr(d.total)}</dd>
                {d.previous_bill != null && <><dt>Previous bill</dt><dd>{inr(d.previous_bill)}</dd></>}
                {d.remark && <><dt>Remark</dt><dd>{d.remark}</dd></>}
              </dl>
            </section>
          ) : (
            <section className="card flush"><h2>Items</h2>
              <div className="scroll"><table>
                <thead><tr><th>Item</th><th>Sub-category</th><th className="r">{rejected.length ? 'Ordered' : 'Qty'}</th>{hasRcv && <th className="r">Received</th>}{rejected.length > 0 && <th className="r">Rejected</th>}<th className="r">Rate</th><th className="r">GST</th><th className="r">Amount</th></tr></thead>
                <tbody>{live.map((l) => (
                  <tr key={l.id} className={l.qty === 0 && !l.received_qty ? 'dim' : ''}>
                    <td>{l.brand} {l.item_name}<br /><span className="hint">{l.sku_code ? l.sku_code + ' · ' : ''}{l.pack_size}</span></td><td>{l.sub_category}</td>
                    <td className="r">{qty(l.qty + l.rejected_qty)}{l.original_qty != null && l.original_qty !== l.qty + l.rejected_qty && <> <s className="sub">{qty(l.original_qty)}</s></>}</td>
                    {hasRcv && <td className="r">{qty(l.received_qty)}</td>}
                    {rejected.length > 0 && <td className="r">{l.rejected_qty ? qty(l.rejected_qty) : '—'}</td>}
                    <td className="r">{inr(l.rate)}{l.estimated && !d.po_number ? <> <Pill tone="warn">est.</Pill></> : null}</td><td className="r">{l.gst_pct}%</td><td className="r">{inr(l.value)}</td></tr>))}
                  <tr className="sum"><td colSpan={5 + (hasRcv ? 1 : 0) + (rejected.length ? 1 : 0)}>Total incl. GST{rejected.length ? ', without the rejected balance' : ''}</td><td className="r">{inr(d.total)}</td></tr>
                </tbody>
              </table></div>
            </section>
          )}
          {(d.kind === 'purchase' || d.kind === 'corp_purchase') && !d.is_transfer && d.po_number && <AdvancesCard docId={d.id} poNumber={d.po_number} onDone={refresh} />}
          {(inv.data || []).length > 0 && (
            <section className="card flush"><h2>Invoices</h2>
              <div className="scroll"><table>
                <thead><tr><th>Voucher</th><th>Invoice</th><th>Delivery</th><th className="r">Amount</th><th>Status</th><th>Zoho</th><th></th></tr></thead>
                <tbody>{inv.data!.map((i) => (
                  <tr key={i.id}><td className="id">{i.voucher_number}</td><td>{i.invoice_number}</td><td>{i.grn_number ? `${i.grn_number} · delivery ${i.delivery_no}` : '—'}</td>
                    <td className="r">{inr(i.amount)}</td><td><Pill tone={statusTone(i.status)}>{i.status.replace('_', ' ').toLowerCase()}</Pill></td>
                    <td>{i.zoho_bill_id || '—'}</td>
                    <td className="r">{i.file_id && <a className="link" href={fileUrl(i.file_id)} target="_blank" rel="noreferrer">File</a>} <Link className="link" to={'/invoices/' + i.id}>Open ›</Link></td></tr>))}
                </tbody>
              </table></div>
            </section>
          )}
          {data.receipts.length > 0 && (
            <section className="card flush"><h2>Goods receipts</h2>
              <div className="scroll"><table>
                <thead><tr><th>Receipt</th><th>Received</th><th>By</th><th>Photos</th></tr></thead>
                <tbody>{data.receipts.map((g) => (
                  <tr key={g.id} className={g.voided ? 'dim' : ''}><td className="id">{g.number} · delivery {g.delivery_no}{g.voided && <><br /><Pill tone="bad">Sent back: {g.void_reason}</Pill></>}</td>
                    <td>{dateTime(g.received_at)}</td><td>{g.received_by}</td>
                    <td>{g.photo_file_ids.map((f, i) => <a key={f} className="link" href={fileUrl(f)} target="_blank" rel="noreferrer">Photo {i + 1} </a>)}</td></tr>))}
                </tbody>
              </table></div>
            </section>
          )}
        </div>
        <div className="stack">
          {d.status === 'PENDING' && !bill && <BudgetBox doc={d} />}
          <section className="card"><h2>Details</h2>
            <dl className="kv">
              <dt>Property</dt><dd>{d.property}</dd><dt>Cluster</dt><dd>{d.cluster} · {d.city}</dd>
              <dt>Vendor</dt><dd>{d.vendor}{d.vendor_msme && <> <Pill tone="warn">MSME</Pill></>}</dd>
              {data.vendor_balance != null && <><dt>Vendor balance</dt><dd><VendorBalance v={data.vendor_balance} /></dd></>}
              <dt>GST</dt><dd>{d.vendor_gst_registered ? 'Registered' : 'Unregistered'}</dd>
              {d.deliver_by && <><dt>Deliver by</dt><dd>{day(d.deliver_by)}{d.deliver_by_extended && <> <Pill>Extended</Pill></>}
                {data.actions.includes('extend_delivery') && <> <button className="link" onClick={() => setExtend(true)}>Extend</button></>}</dd></>}
              {d.payment_terms && <><dt>Terms</dt><dd>{d.payment_terms}</dd></>}
              {d.po_number && !d.is_transfer && <><dt>Zoho purchase order</dt><dd><ZohoPOState d={d} onDone={refresh} /></dd></>}
              {d.route_steps.length > 0 && <><dt>Approval route</dt><dd>{d.route_steps.map((s) => ({ l2: 'Cluster Manager', cc: 'Cost Controller', preop: 'Pre-opening Team', cl1: 'L1 ' + d.corp_l1, cl2: 'L2 ' + d.corp_l2, whl1: 'Warehouse Approver L1', whl2: 'Warehouse Approver L2', c1: 'C1', c2: 'C2', cfo: 'CFO' } as Record<string, string>)[s] || s).join(' → ')}</dd></>}
              {corp && <><dt>Department</dt><dd>{d.department}</dd>{d.corp_backup && <><dt>Backup approver</dt><dd>{d.corp_backup}</dd></>}{d.kind === 'corp_purchase' && d.remark && <><dt>Why</dt><dd>{d.remark}</dd></>}
                {d.quote_file_id && <><dt>Quotation</dt><dd><a className="link" href={fileUrl(d.quote_file_id)} target="_blank" rel="noreferrer">Open</a></dd></>}</>}
            </dl>
          </section>
          <section className="card"><h2>History</h2>
            <ul className="log">{data.log.map((l, i) => <li key={i}><b>{l.action}</b><span>{l.user} ({l.role}) · {dateTime(l.at)}</span>{l.note && <><br />“{l.note}”</>}</li>)}</ul>
          </section>
        </div>
      </div>
    </>
  )
}

/** The latest delivery when Accounts sent it back for a recount, as the store entered it, to fill the receive form again. */
function sentBack(detail: Detail, invoices: Invoice[]): SentBack | null {
  const last = [...detail.receipts].sort((a, b) => b.id - a.id)[0]
  if (!last?.voided) return null
  const x = invoices.find((i) => i.id === last.invoice_id)
  return {
    grn: last.number, reason: last.void_reason, quantities: Object.fromEntries(last.lines.map((l) => [l.line_id, l.qty])),
    invoice_number: x?.invoice_number || '', invoice_date: x?.invoice_date || '', amount: x?.amount || 0, invoice_file_id: x?.file_id ?? null, photo_file_ids: last.photo_file_ids,
  }
}

function ActionPanel({ detail, invoices, onDone }: { detail: Detail; invoices: Invoice[] | null | undefined; onDone: () => void }) {
  const d = detail.document
  const acts = detail.actions
  if (d.status === 'APPROVED' && d.advance_hold_po) return <p className="note warn"><b>{d.advance_hold_po} is prepared on Advance terms and waits for the advance to be {d.advance_approved ? 'paid' : 'approved and paid'}.</b> It is raised and sent to {d.vendor} only once the advance shows Paid: Finance marks it paid with the UTR, or it is read paid from Zoho (see Advance to vendor below). Until then there is no goods receipt. If Finance rejects the advance, the Purchase Team issues the PO again.</p>
  if (acts.includes('approve')) return <ApprovePanel doc={d} canRoute={acts.includes('route')} canPreop={acts.includes('preop_approver')} raisesPO={!!detail.raises_po} overBudget={detail.over_budget || 0} vendorBalance={detail.vendor_balance} onDone={onDone} />
  if (acts.includes('resubmit')) return <ResubmitPanel doc={d} canEdit={acts.includes('edit')} onDone={onDone} />
  if (acts.includes('issue_po')) return <IssuePOPanel doc={d} onDone={onDone} />
  if (acts.includes('receive')) {
    if (!invoices) return <p className="sub">Loading…</p>
    return <ReceivePanel doc={d} onDone={onDone} canReject={acts.includes('reject_balance')} sentBack={sentBack(detail, invoices)}
      fromVendor={(detail.vendor_invoices || []).find((x) => !x.goods_receipt_id)} />
  }
  if (acts.includes('edit') && d.status === 'PENDING') return <p className="note"><b>Nobody has approved this yet.</b> You can still change anything on it with Edit; once the {d.owner_name} acts, it is locked.</p>
  if (acts.includes('dispatch')) return <DispatchPanel doc={d} onDone={onDone} />
  if (acts.includes('receive_transfer')) return <ReceiveTransferPanel doc={d} onDone={onDone} />
  const owner = d.owner_name
  if (!owner) return null
  const people = detail.owner_people
  // Nobody holding the step's role at this property means the document cannot move until the Admin gives someone access.
  if (people && !people.length) return <p className="note bad"><b>No one can take the next step.</b> It is with the {owner}, but no active user has that role with access to {d.property}. The System Admin should give a user the {owner} role for this property (Settings → Users &amp; Rights).</p>
  return <p className="note">Next step is with the <b>{owner}</b>{people?.length ? <>: {people.slice(0, 5).join(', ')}{people.length > 5 ? ` and ${people.length - 5} more` : ''}</> : ''}.</p>
}

function ApprovePanel({ doc, canRoute, canPreop, raisesPO, overBudget, vendorBalance, onDone }: { doc: Doc; canRoute: boolean; canPreop: boolean; raisesPO: boolean; overBudget: number; vendorBalance: number | null; onDone: () => void }) {
  const { me } = useSession()
  // Over the month budget: the Cost Controller (or the last approver without one) says why, for tracking.
  const [budgetReasons, setBudgetReasons] = useState<string[]>([])
  useEffect(() => { if (overBudget > 0) get<Record<string, string[]>>('/pick-lists').then((l) => setBudgetReasons(l.over_budget_reasons || [])) }, [overBudget])
  const [budgetReason, setBudgetReason] = useState('')
  const [budgetNote, setBudgetNote] = useState('')
  const [q, setQ] = useState<Record<number, string>>(Object.fromEntries(doc.lines.map((l) => [l.id, String(l.qty)])))
  const [note, setNote] = useState('')
  const [reason, setReason] = useState<ReasonKind | null>(null)
  const [preop, setPreop] = useState(false)
  const a = useAction()
  const changed = Object.fromEntries(doc.lines.filter((l) => +q[l.id] !== l.qty).map((l) => [l.id, +q[l.id] || 0]))
  // The last approval raises the PO: estimated rates are confirmed here, and on Advance terms the advance is asked.
  const estimated = raisesPO ? doc.lines.filter((l) => l.qty > 0 && (l.estimated || l.rate <= 0)) : []
  const [rates, setRates] = useState<Record<number, string>>(Object.fromEntries(estimated.map((l) => [l.id, l.rate > 0 ? String(l.rate) : ''])))
  // The approver picks the PO's payment terms (the vendor's own by default); Advance asks the advance for Finance.
  const [terms, setTerms] = useState(PAYMENT_TERMS.includes(doc.vendor_terms) ? doc.vendor_terms : 'Net 30')
  // The PO's delivery date, given by the approver who raises it (Flow Settings days ahead to start with); once it has
  // passed, the undelivered balance is rejected automatically.
  const [due, setDue] = useState(addDays(today(), 2))
  useEffect(() => { if (raisesPO) get<{ auto_po_days: number }>('/settings').then((s) => setDue(addDays(today(), s.auto_po_days || 0))).catch(() => {}) }, [raisesPO])
  const advanceTerms = raisesPO && terms === 'Advance'
  const [adv, setAdv] = useState<AdvanceDraft>(newAdvance())
  const poValue = doc.lines.reduce((t, l) => t + lineValue(q[l.id] === undefined ? l.qty : +q[l.id] || 0, estimated.some((e) => e.id === l.id) ? +rates[l.id] || 0 : l.rate, l.gst_pct), 0)
  const approve = () => {
    const missing = estimated.filter((l) => !(+rates[l.id] > 0) && (+q[l.id] || 0) > 0)
    if (missing.length) return a.setErrors(missing.map((l) => `Confirm the rate for ${l.item_name}: it has no rate card.`))
    if (advanceTerms) { const p = advanceProblem(adv, poValue, poValue); if (p) return a.setErrors([p]) }
    if (overBudget > 0 && !budgetReason) return a.setErrors([`This request is ${inr(overBudget)} over the month budget: choose why you approve it anyway.`])
    a.run(() => post(`/documents/${doc.id}/approve`, {
      quantities: changed, note, budget_reason: budgetReason, budget_note: budgetNote,
      ...(raisesPO ? { deliver_by: due, payment_terms: terms, rates: Object.fromEntries(estimated.map((l) => [l.id, +rates[l.id] || 0])), advance: advanceTerms ? advanceBody(adv, poValue) : undefined } : {}),
    }), (r: any) => { onDone(); return r.message })
  }
  return (
    <section className="card act">
      <h2>{me?.is_admin ? `Decision (acting for the ${doc.owner_name})` : 'Your decision'}</h2>
      {vendorBalance != null && vendorBalance !== 0 && <p className={'note' + (vendorBalance < 0 ? ' warn' : '')}><b>{doc.vendor}: <VendorBalance v={vendorBalance} /></b>{vendorBalance < 0 ? ' — advances already paid and not yet billed; they are taken off this vendor\'s next invoices.' : ' — bills not yet paid.'}</p>}
      {doc.kind === 'purchase' && <>
        <div className="grid2">{doc.lines.map((l) => (
          <label className="field" key={l.id}><span>{l.item_name} ({l.pack_size})</span>
            <input type="number" min="0" inputMode="decimal" value={q[l.id]} onChange={(e) => setQ({ ...q, [l.id]: e.target.value })} /></label>))}
        </div>
        <p className="hint">Change a quantity before approving, or set it to 0 to drop that item. The requester sees what changed and by whom.</p>
      </>}
      {raisesPO && <div className="note"><b>Approving raises the PO.</b> {advanceTerms
        ? `On Advance terms the PO is prepared and goes to ${doc.vendor} once Finance approves and pays the advance below.`
        : `It goes to ${doc.vendor} on ${terms} terms.`}</div>}
      {raisesPO && <label className="field"><span>Payment terms for this PO</span>
        <select value={terms} onChange={(e) => setTerms(e.target.value)}>{PAYMENT_TERMS.map((t) => <option key={t} value={t}>{t}{t === doc.vendor_terms ? ' (vendor\'s usual)' : ''}</option>)}</select>
        <span className="hint">Choose Advance when the vendor must be paid before it dispatches; you then say how much.</span></label>}
      {raisesPO && <label className="field"><span>Deliver by</span><input type="date" min={today()} value={due} onChange={(e) => setDue(e.target.value)} />
        <span className="hint">The date agreed with {doc.vendor}. If the goods have not come by then, the undelivered balance is rejected automatically (it can be extended once).</span></label>}
      {estimated.length > 0 && <div className="grid2">{estimated.map((l) => (
        <label className="field" key={l.id}><span>Rate for {l.item_name} (₹, before GST)</span>
          <input type="number" min="0" step="0.01" inputMode="decimal" value={rates[l.id]} onChange={(e) => setRates({ ...rates, [l.id]: e.target.value })} />
          <span className="hint">No rate card: the requester estimated {l.rate > 0 ? inr(l.rate) : 'nothing'}. The PO goes out at the rate you confirm.</span></label>))}
      </div>}
      {advanceTerms && <><h3>Advance to ask Finance for</h3>
        <AdvanceFields a={adv} onChange={setAdv} poValue={poValue} room={poValue} poLabel="this PO" noFile /></>}
      {overBudget > 0 && <div className="xpbox">
        <p className="note warn"><b>Over the month budget by {inr(overBudget)}.</b> Approving needs a reason; it is kept on the request and in the Approved over budget report.</p>
        <div className="grid2">
          <Field label="Why approve over budget"><select value={budgetReason} onChange={(e) => setBudgetReason(e.target.value)}><option value="">Choose a reason</option>{budgetReasons.map((r) => <option key={r}>{r}</option>)}</select></Field>
          <Field label={budgetReason.startsWith('Other') ? 'Details' : 'Details (optional)'}><input value={budgetNote} onChange={(e) => setBudgetNote(e.target.value)} placeholder="e.g. 92% occupancy this week" /></Field>
        </div></div>}
      <label className="field"><span>Remark (optional)</span><textarea value={note} onChange={(e) => setNote(e.target.value)} /></label>
      <Errors list={a.errors} />
      <div className="btns">
        {canPreop && <button className="btn ghost" onClick={() => setPreop(true)}>Change approver</button>}
        <button className="btn no" onClick={() => setReason('reject')}>Reject</button>
        <button className="btn amber" onClick={() => setReason('return')}>Return for changes</button>
        {canRoute && <button className="btn ghost" onClick={() => setReason('route')}>Send to Cluster Manager</button>}
        <button className="btn" disabled={a.busy} onClick={approve}>{raisesPO ? 'Approve and raise PO' : 'Approve'}</button>
      </div>
      {reason && <ReasonModal kind={reason} count={1} busy={a.busy} errors={a.errors} onClose={() => setReason(null)}
        onConfirm={(r, n) => a.run(() => post(`/documents/${doc.id}/${reason}`, { reason: r, note: n, quantities: reason === 'route' ? changed : undefined }), (x: any) => { setReason(null); onDone(); return x.message })} />}
      {preop && <PreopModal doc={doc} onClose={() => setPreop(false)} onDone={() => { setPreop(false); onDone() }} />}
    </section>
  )
}

function PreopModal({ doc, onClose, onDone }: { doc: Doc; onClose: () => void; onDone: () => void }) {
  const { data } = useApi<{ preop_members: { id: number; name: string }[] }>(`/requests/form?property_id=${doc.property_id}`)
  const [uid, setUid] = useState<number>(doc.preop_user_id || 0)
  const a = useAction()
  return (
    <Modal title={`Pre-opening approver for ${doc.number}`} onClose={onClose}>
      <label className="field"><span>Pre-opening team member</span>
        <select value={uid} onChange={(e) => setUid(+e.target.value)}><option value={0}>Choose</option>{data?.preop_members.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</select></label>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={!uid || a.busy} onClick={() => a.run(() => post(`/documents/${doc.id}/preop-approver`, { user_id: uid }), (r: any) => { onDone(); return r.message })}>Save</button></div>
    </Modal>
  )
}

function ResubmitPanel({ doc, canEdit, onDone }: { doc: Doc; canEdit: boolean; onDone: () => void }) {
  const nav = useNavigate()
  const [q, setQ] = useState<Record<number, string>>(Object.fromEntries(doc.lines.map((l) => [l.id, String(l.qty)])))
  const [invNo, setInvNo] = useState('')
  const [amt, setAmt] = useState(String(doc.total || ''))
  const a = useAction()
  return (
    <section className="card act">
      <h2>Change and resubmit</h2>
      {doc.kind === 'bill' ? (
        <div className="grid2">
          <label className="field"><span>Invoice number</span><input value={invNo} onChange={(e) => setInvNo(e.target.value)} placeholder="Enter again, corrected if needed" /></label>
          <label className="field"><span>Invoice amount incl. GST (₹)</span><input type="number" value={amt} onChange={(e) => setAmt(e.target.value)} /></label>
        </div>
      ) : (
        <div className="grid2">{doc.lines.map((l) => (
          <label className="field" key={l.id}><span>Quantity of {l.item_name}</span>
            <input type="number" min="0" value={q[l.id]} onChange={(e) => setQ({ ...q, [l.id]: e.target.value })} /></label>))}</div>
      )}
      <Errors list={a.errors} />
      <div className="btns">
        <button className="btn no" disabled={a.busy} onClick={() => a.run(() => post(`/documents/${doc.id}/cancel`), (r: any) => { onDone(); return r.message })}>Cancel request</button>
        {canEdit && <button className="btn ghost" onClick={() => nav(`/requests/${doc.id}/edit`)}>Change items or property</button>}
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post(`/documents/${doc.id}/resubmit`, {
          quantities: Object.fromEntries(Object.entries(q).map(([k, v]) => [k, +v || 0])), invoice_number: invNo, invoice_amount: +amt,
        }), (r: any) => { onDone(); return r.message })}>Resubmit</button>
      </div>
    </section>
  )
}

// Where the PO stands with its Zoho Books purchase order: in step, waiting to be sent, held for a mapping, or refused.
// Finance, Purchase or the System Admin can send it at once.
function ZohoPOState({ d, onDone }: { d: Doc; onDone: () => void }) {
  const { hasRight, hasStep } = useSession()
  const a = useAction()
  const what = { create: 'to create', update: 'to update', cancel: 'to cancel' }[d.zoho_po_sync as 'create' | 'update' | 'cancel']
  const held = d.zoho_po_error.startsWith('Held:')
  return (
    <>
      {!d.zoho_po_sync && d.zoho_po_id ? <Pill tone="ok">In Zoho</Pill> : d.zoho_po_sync ? <Pill tone={d.zoho_po_error ? 'bad' : 'warn'}>{held ? 'Held' : d.zoho_po_error ? 'Refused by Zoho' : 'Waiting'} {what}</Pill> : <span className="hint">Not sent</span>}
      {d.zoho_po_error && <><br /><span className="hint">{d.zoho_po_error}</span></>}
      {d.zoho_po_sync && (hasStep('finance') || hasRight('issue_po')) && <><br /><button className="link" disabled={a.busy} onClick={() => a.run(() => post(`/zoho/purchase-orders/${d.id}/sync`), (r: any) => { onDone(); return r.message })}>Sync now</button></>}
      <Errors list={a.errors} />
    </>
  )
}

/** Moves a PO's delivery date once, with a reason (poexpiry). */
function ExtendModal({ doc, onClose, onDone }: { doc: Doc; onClose: () => void; onDone: () => void }) {
  const [date, setDate] = useState(addDays(doc.deliver_by || today(), 3))
  const [reason, setReason] = useState('')
  const a = useAction()
  return (
    <Modal title={'Extend the delivery date of ' + (doc.po_number || doc.number)} onClose={onClose}>
      <p className="sub">Now due by {day(doc.deliver_by)}. The date can be moved once; after the new date the undelivered balance is rejected automatically.</p>
      <div className="grid2">
        <Field label="New delivery date"><input type="date" min={addDays(doc.deliver_by || today(), 1)} value={date} onChange={(e) => setDate(e.target.value)} /></Field>
        <Field label="Why"><input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. Vendor short of stock, confirmed for Friday" /></Field>
      </div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post(`/documents/${doc.id}/deliver-by`, { deliver_by: date, reason }), (r: any) => { onDone(); return r.message })}>Extend</button></div>
    </Modal>
  )
}

/** A vendor's balance in words: negative = advance paid, not yet billed; positive = due to the vendor. */
function VendorBalance({ v }: { v: number }) {
  if (Math.abs(v) < 0.005) return <>Nothing due either way</>
  return v < 0 ? <>−{inr(-v)} advance paid, not yet billed</> : <>{inr(v)} due to the vendor</>
}
