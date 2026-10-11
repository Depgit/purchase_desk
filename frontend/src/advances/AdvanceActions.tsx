// What a viewer can do with one advance, as the server allows (advance.actions): approve, reject, cancel, mark paid,
// record the vendor's refund, carry what is left forward to the vendor's next invoices, or take that back.
import { useState } from 'react'
import { post } from '../api/client'
import type { Advance } from '../api/types'
import { inr, today } from '../format/format'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Pill, type Tone } from '../ui/Pill'
import { useAction } from '../ui/useAction'

const LABEL: Record<Advance['status'], [string, Tone]> = {
  PENDING: ['Waiting for Finance', 'warn'], APPROVED: ['Approved, to pay', ''], PAID: ['Paid', 'ok'], REJECTED: ['Rejected', 'bad'], CANCELLED: ['Cancelled', 'off'],
}

export function AdvanceStatus({ a }: { a: Advance }) {
  const [label, tone] = LABEL[a.status]
  if (a.status !== 'PAID') return <Pill tone={tone}>{label}</Pill>
  if (a.open < 0.005) return <Pill tone="ok">Settled</Pill>
  return <><Pill tone="warn">Paid · {inr(a.open)} to adjust</Pill>{a.carry_forward && <> <Pill>Carried forward</Pill></>}</>
}

type Ask = 'reject' | 'cancel' | 'paid' | 'refund' | null

export function AdvanceActions({ a, onDone }: { a: Advance; onDone: () => void }) {
  const act = useAction()
  const [ask, setAsk] = useState<Ask>(null)
  const done = (r: { message: string }) => { setAsk(null); onDone(); return r.message }
  const has = (k: string) => a.actions.includes(k)
  if (!a.actions.length) return null
  return (
    <span className="tools" style={{ flexWrap: 'nowrap' }}>
      {has('approve') && <button className="btn ghost small" disabled={act.busy} onClick={() => act.run(() => post(`/advances/${a.id}/approve`, { note: '' }), done)}>Approve</button>}
      {has('paid') && <button className="btn ghost small" onClick={() => setAsk('paid')}>Mark paid</button>}
      {has('reject') && <button className="link" onClick={() => setAsk('reject')}>Reject</button>}
      {has('cancel') && <button className="link" onClick={() => setAsk('cancel')}>Cancel</button>}
      {has('refund') && <button className="link" onClick={() => setAsk('refund')}>Record refund</button>}
      {has('carry') && <button className="link" disabled={act.busy} onClick={() => act.run(() => post(`/advances/${a.id}/carry-forward`, { carry: true }), done)}>Adjust on next invoices</button>}
      {has('keep') && <button className="link" disabled={act.busy} onClick={() => act.run(() => post(`/advances/${a.id}/carry-forward`, { carry: false }), done)}>Stop carrying forward</button>}
      {act.errors.length > 0 && !ask && <Errors list={act.errors} />}
      {ask && <AskModal a={a} ask={ask} onClose={() => setAsk(null)} onDone={done} />}
    </span>
  )
}

function AskModal({ a, ask, onClose, onDone }: { a: Advance; ask: Exclude<Ask, null>; onClose: () => void; onDone: (r: { message: string }) => string }) {
  const act = useAction()
  const [note, setNote] = useState('')
  const [ref, setRef] = useState('')
  const [on, setOn] = useState(today())
  const [amt, setAmt] = useState(String(a.open))
  const title = { reject: `Reject ${a.number}`, cancel: `Cancel ${a.number}`, paid: `Mark ${a.number} paid`, refund: `Refund on ${a.number}` }[ask]
  const send = () => act.run(() => {
    if (ask === 'paid') return post(`/advances/${a.id}/paid`, { reference: ref, paid_on: on })
    if (ask === 'refund') return post(`/advances/${a.id}/refund`, { amount: +amt, reference: ref, on })
    return post(`/advances/${a.id}/${ask}`, { note })
  }, onDone)
  return (
    <Modal title={title} onClose={onClose}>
      {ask === 'paid' && <>
        <p className="sub">Pay {a.vendor} <b>{inr(a.pay_vendor)}</b>{a.tds_amount > 0 ? ` (advance ${inr(a.amount)} less TDS ${inr(a.tds_amount)})` : ''} against {a.po_number}. The vendor is emailed once it is marked paid.</p>
        <Field label="Payment reference (UTR or cheque number)"><input value={ref} onChange={(e) => setRef(e.target.value)} /></Field>
        <Field label="Paid on"><input type="date" value={on} max={today()} onChange={(e) => setOn(e.target.value)} /></Field></>}
      {ask === 'refund' && <>
        <p className="sub">{inr(a.open)} of {a.number} is left to adjust. Record what the vendor paid back.</p>
        <Field label="Part of the advance refunded" hint={a.tds_amount > 0 ? 'Enter the advance amount; the TDS held on it is taken off what the vendor returns.' : undefined}>
          <input type="number" value={amt} onChange={(e) => setAmt(e.target.value)} /></Field>
        <Field label="Refund reference (UTR or cheque number)"><input value={ref} onChange={(e) => setRef(e.target.value)} /></Field>
        <Field label="Received on"><input type="date" value={on} max={today()} onChange={(e) => setOn(e.target.value)} /></Field></>}
      {(ask === 'reject' || ask === 'cancel') && <Field label={ask === 'reject' ? 'Why is it rejected?' : 'Note (optional)'}><textarea value={note} onChange={(e) => setNote(e.target.value)} /></Field>}
      <Errors list={act.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Back</button><button className="btn" disabled={act.busy} onClick={send}>{title}</button></div>
    </Modal>
  )
}
