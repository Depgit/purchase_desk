// The advances on one purchase order, on its document page: a summary (PO value, asked, paid, left to adjust), each
// advance with its status, how much of it is used and the next step, and the request form (whoever issues the PO),
// which opens in the card with a live preview of what the vendor gets.
import { useEffect, useRef, useState } from 'react'
import { fileUrl, upload } from '../api/client'
import type { Advance } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { day, inr } from '../format/format'
import { Errors } from '../ui/Errors'
import { useAction } from '../ui/useAction'
import { AdvanceActions, AdvanceStatus } from './AdvanceActions'
import { AdvanceFields, advanceAmount, advanceBody, advanceProblem, newAdvance, type AdvanceDraft } from './AdvanceFields'

interface ForDoc { advances: Advance[]; can_request: boolean; po_value: number; po_tds: number; requested: number; paid: number }

export function AdvancesCard({ docId, poNumber, onDone }: { docId: number; poNumber: string; onDone: () => void }) {
  const { data, reload } = useApi<ForDoc>(`/documents/${docId}/advances`)
  const [asking, setAsking] = useState(false)
  const { hasRight } = useSession()
  // The card sits low on the page, so opening the request form brings it into view instead of opening off screen.
  const card = useRef<HTMLElement>(null)
  useEffect(() => { if (asking) card.current?.scrollIntoView({ behavior: 'smooth', block: 'start' }) }, [asking])
  if (!data || (!data.advances.length && !data.can_request)) return null
  // Advances are money matters: shown to whoever can ask for one, Finance, and anyone with a step on one, not the store.
  if (!data.can_request && !hasRight('pay') && !hasRight('issue_po') && !data.advances.some((a) => a.actions.length)) return null
  const done = () => { reload(); onDone() }
  const left = data.advances.reduce((t, a) => t + a.open, 0)
  return (
    <section ref={card} className={'card' + (asking ? ' act' : '')}>
      {asking ? <RequestForm docId={docId} poNumber={poNumber} d={data} onCancel={() => setAsking(false)} onDone={() => { setAsking(false); done() }} /> : <>
        <div className="phead"><div><h2>Advance to vendor</h2>
          <p className="sub">{data.advances.length ? `Paid advances are adjusted on the vendor's invoice when Finance approves its payment.` : `${poNumber} is on Advance terms. Ask Finance for a further advance if the vendor needs one.`}</p></div>
          {data.can_request && <button className="btn small" onClick={() => setAsking(true)}>Ask for a further advance</button>}</div>
        {data.advances.length > 0 && <>
          <div className="stats">
            <div className="stat"><span>PO value</span><b>{inr(data.po_value)}</b></div>
            <div className="stat"><span>Advance asked</span><b>{inr(data.requested)}</b></div>
            <div className="stat"><span>Paid</span><b>{inr(data.paid)}</b></div>
            <div className={'stat' + (left > 0.005 ? ' warn' : '')}><span>Left to adjust</span><b>{inr(left)}</b></div>
          </div>
          {data.advances.map((a) => <AdvanceRow key={a.id} a={a} onDone={done} />)}</>}
      </>}
    </section>
  )
}

function AdvanceRow({ a, onDone }: { a: Advance; onDone: () => void }) {
  const dead = a.status === 'REJECTED' || a.status === 'CANCELLED'
  const used = a.adjusted + a.refunded
  return (
    <div className="advrow" style={dead ? { opacity: 0.6 } : undefined}>
      <div className="ahead"><div><b>{a.number}</b> <AdvanceStatus a={a} /></div>
        <div className="amt">{inr(a.amount)}{a.pct ? <span className="hint"> · {a.pct}% of PO</span> : null}</div></div>
      <div className="meta">
        <span>{a.reason}{a.note ? ` · ${a.note}` : ''}</span>
        <span>Asked by <b>{a.requester}</b> on {day(a.requested_at)}</span>
        {a.tds_amount > 0 && <span>TDS <b>{inr(a.tds_amount)}</b> · vendor gets <b>{inr(a.pay_vendor)}</b></span>}
        {a.paid_on && <span>Paid <b>{day(a.paid_on)}</b> · ref {a.payment_ref}</span>}
        {a.proforma_file_id ? <a className="link" href={fileUrl(a.proforma_file_id)} target="_blank" rel="noreferrer">Proforma</a> : null}
      </div>
      {a.decision_note && <p className="hint" style={{ margin: 0 }}>{a.status === 'REJECTED' ? 'Rejected: ' : a.status === 'CANCELLED' ? 'Cancelled: ' : 'Note: '}{a.decision_note}</p>}
      {a.status === 'PAID' && <div>
        <div className="bar"><i style={{ width: `${Math.min(100, (used / a.amount) * 100)}%` }} /></div>
        <p className="hint" style={{ margin: '4px 0 0' }}>{inr(a.adjusted)} adjusted on invoices{a.refunded ? ` · ${inr(a.refunded)} refunded` : ''} · <b>{inr(a.open)} left</b>{a.carry_forward && a.open > 0 ? ' · carried forward to the next invoices' : ''}</p></div>}
      {a.actions.length > 0 && <AdvanceActions a={a} onDone={onDone} />}
    </div>
  )
}

function RequestForm({ docId, poNumber, d, onCancel, onDone }: { docId: number; poNumber: string; d: ForDoc; onCancel: () => void; onDone: () => void }) {
  const act = useAction()
  const room = Math.max(0, Math.round((d.po_value - d.requested) * 100) / 100)
  const [adv, setAdv] = useState<AdvanceDraft>(() => ({ ...newAdvance(), val: room >= d.po_value / 2 ? '50' : '' }))
  const problem = advanceProblem(adv, d.po_value, room)
  const amount = advanceAmount(adv, d.po_value)
  return (
    <>
      <div className="phead"><div><h2>Ask for a further advance on {poNumber}</h2>
        <p className="sub">This PO is on Advance terms. Finance approves and pays it, and the vendor is emailed once it is paid. It is then adjusted on the vendor's invoice.</p></div></div>
      <div className="stats">
        <div className="stat"><span>PO value</span><b>{inr(d.po_value)}</b></div>
        {d.requested > 0 && <div className="stat"><span>Already asked</span><b>{inr(d.requested)}</b></div>}
        <div className="stat"><span>Can still ask</span><b>{inr(room)}</b></div>
      </div>
      <AdvanceFields a={adv} onChange={setAdv} poValue={d.po_value} room={room} poTDS={d.po_tds} asked={d.requested} poLabel={poNumber} />
      <Errors list={act.errors} />
      <div className="btns"><button className="btn ghost" onClick={onCancel}>Cancel</button>
        <button className="btn" disabled={act.busy || !!problem} title={problem || undefined}
          onClick={() => act.run(() => upload(`/documents/${docId}/advances`, advanceBody(adv, d.po_value), { proforma: adv.file }), (r: any) => { onDone(); return r.message })}>
          Send {amount > 0 ? inr(amount) : ''} to Finance</button></div>
    </>
  )
}
