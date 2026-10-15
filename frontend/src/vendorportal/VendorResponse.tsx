// On a PO's page: what the vendor did with it (not answered yet, accepted with its dispatch date, or declined and
// why), the invoices it uploaded on the vendor portal, and, for the Purchase Team, confirming the PO for the vendor
// after a phone call or once a decline is sorted out.
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { fileUrl, post } from '../api/client'
import type { Doc } from '../api/types'
import { dateTime, day, inr, today } from '../format/format'
import { Errors } from '../ui/Errors'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'

export interface PortalInvoice { id: number; invoice_number: string; invoice_date: string; amount: number; file_id: number; note: string; goods_receipt_id: number | null; created_at: string }

export function VendorResponse({ doc, invoices, portal, canConfirm, onDone }: { doc: Doc; invoices: PortalInvoice[]; portal: boolean; canConfirm: boolean; onDone: () => void }) {
  const [open, setOpen] = useState(false)
  const [date, setDate] = useState('')
  const [note, setNote] = useState('')
  const a = useAction()
  const waiting = doc.status === 'PO_ISSUED'
  if (!waiting && !invoices.length) return null
  return (
    <section className="card">
      <h2>Vendor</h2>
      {waiting && (doc.vendor_status === 'accepted'
        ? <p><Pill tone="ok">Accepted, awaiting delivery</Pill> {doc.dispatch_date && <>Dispatch on <b>{day(doc.dispatch_date)}</b>. </>}
          <span className="hint">{doc.vendor_acted_at ? dateTime(doc.vendor_acted_at) : ''}{doc.vendor_note ? ' · ' + doc.vendor_note : ''}</span></p>
        : doc.vendor_status === 'declined'
          ? <p className="note bad"><b>{doc.vendor} declined this PO:</b> {doc.vendor_note}. Sort it out with the vendor and confirm it below, or reject the balance to cancel the PO.</p>
          : !doc.sent_to_vendor_at
            ? <p><Pill tone="warn">Push to vendor</Pill> <span className="hint">Not sent to {doc.vendor} yet: it goes out with the vendor's order sheet. </span>
              <Link className="link" to="/vendor-sheets">Vendor Order Sheets ›</Link></p>
          : <p><Pill tone="warn">Sent to vendor</Pill> <span className="hint">{dateTime(doc.sent_to_vendor_at)} · {portal ? 'Not accepted on the vendor portal yet.' : `${doc.vendor} has no vendor portal sign-in (give one on its vendor page). Confirm here once it agrees by phone or email.`}</span></p>)}
      {waiting && canConfirm && (open ? <>
        <div className="grid2">
          <label className="field"><span>Dispatch date (optional)</span><input type="date" min={today()} value={date} onChange={(e) => setDate(e.target.value)} /></label>
          <label className="field"><span>{doc.vendor_status === 'declined' ? 'What was agreed with the vendor' : 'Note (optional)'}</span><input value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. confirmed on call with Ramesh" /></label>
        </div>
        <Errors list={a.errors} />
        <div className="btns"><button className="btn ghost" onClick={() => setOpen(false)}>Cancel</button>
          <button className="btn" disabled={a.busy} onClick={() => a.run(() => post(`/documents/${doc.id}/vendor-confirm`, { dispatch_date: date, note }), () => { setOpen(false); onDone() })}>Confirm for the vendor</button></div>
      </> : <div className="btns"><button className="btn ghost" onClick={() => setOpen(true)}>Vendor confirmed by phone or email</button></div>)}
      {invoices.length > 0 && <>
        <h3>Invoices uploaded by the vendor</h3>
        <ul className="log">{invoices.map((x) => <li key={x.id}><b>{x.invoice_number}</b> · {day(x.invoice_date)} · {inr(x.amount)} · <a className="link" href={fileUrl(x.file_id)} target="_blank" rel="noreferrer">Open</a>
          <span>{x.goods_receipt_id ? 'Used by a goods receipt' : 'Not received yet: the goods receipt starts from it'} · uploaded {dateTime(x.created_at)}</span>{x.note && <><br />“{x.note}”</>}</li>)}</ul></>}
    </section>
  )
}
