// The Warehouse Manager's panel on a transfer order: schedule the delivery, mark it waiting for stock,
// return it when the warehouse cannot supply, or dispatch what is in stock (keeping or closing any balance).
import { useEffect, useState } from 'react'
import { get, post } from '../api/client'
import type { Doc } from '../api/types'
import { useApi } from '../api/useApi'
import { addDays, qty, today } from '../format/format'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'

interface Stock { sku_id: number; closing: number }
export interface Partner { id: number; name: string; kind: string; driver: string; phone: string; active: boolean; open_deliveries: number }

export function DispatchPanel({ doc, onDone }: { doc: Doc; onDone: () => void }) {
  const stock = useApi<Stock[]>('/warehouse/stock')
  const have = (sku: number) => stock.data?.find((s) => s.sku_id === sku)?.closing ?? 0
  const [q, setQ] = useState<Record<number, string>>({})
  useEffect(() => {
    if (stock.data) setQ(Object.fromEntries(doc.lines.map((l) => [l.id, String(Math.max(0, Math.min(l.qty, have(l.sku_id))))])))
  }, [stock.data]) // eslint-disable-line
  const [balance, setBalance] = useState<'keep' | 'close'>('keep')
  const [modal, setModal] = useState<'schedule' | 'wait' | 'back' | null>(null)
  const a = useAction()
  const short = doc.lines.some((l) => (+q[l.id] || 0) < l.qty)
  return (
    <section className="card act">
      <h2>Dispatch from warehouse</h2>
      <p className="hint">Enter what you are sending to {doc.property}. It cannot be more than the order or the stock on hand.</p>
      <div className="scroll"><table><thead><tr><th>Item</th><th className="r">Ordered</th><th className="r">In stock</th><th className="r">Dispatch</th></tr></thead>
        <tbody>{doc.lines.map((l) => { const h = have(l.sku_id); return (
          <tr key={l.id}><td>{l.item_name}<br /><span className="hint">{l.sku_code} · {l.pack_size}</span></td><td className="r">{qty(l.qty)}</td>
            <td className="r">{h < l.qty ? <Pill tone="warn">{qty(h)} only</Pill> : qty(h)}</td>
            <td className="r"><input className="qty" type="number" min="0" max={Math.min(l.qty, h)} value={q[l.id] ?? ''} onChange={(e) => setQ({ ...q, [l.id]: e.target.value })} aria-label={'Dispatch ' + l.item_name} /></td></tr>) })}</tbody></table></div>
      {doc.delivery ? <p className="note"><b>Scheduled:</b> {doc.delivery.date}, {doc.delivery.slot} · {doc.delivery.partner}{doc.delivery.vehicle ? ' · ' + doc.delivery.vehicle : ''}</p>
        : doc.wait_until ? <p className="note warn"><b>Marked awaiting stock</b> until {doc.wait_until}{doc.wait_note ? ' · ' + doc.wait_note : ''}</p>
        : <p className="hint">Schedule the delivery and assign a delivery partner before dispatch, so the property knows when to expect it.</p>}
      {short && <label className="field" style={{ maxWidth: 440 }}><span>If you send less than ordered</span>
        <select value={balance} onChange={(e) => setBalance(e.target.value as 'keep' | 'close')}><option value="keep">Keep the balance open for a later dispatch</option><option value="close">Close the balance as rejected</option></select></label>}
      <Errors list={a.errors} />
      <div className="btns">
        <button className="btn no" onClick={() => setModal('back')}>Cannot supply, return order</button>
        <button className="btn amber" onClick={() => setModal('wait')}>No stock yet, set expected date</button>
        <button className="btn ghost" onClick={() => setModal('schedule')}>{doc.delivery ? 'Reschedule' : 'Schedule delivery'}</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post(`/documents/${doc.id}/dispatch`, { quantities: Object.fromEntries(Object.entries(q).map(([k, v]) => [k, +v || 0])), balance }), (r: any) => { onDone(); return r.message })}>Dispatch</button>
      </div>
      {modal === 'schedule' && <ScheduleModal ids={[doc.id]} current={doc} onClose={() => setModal(null)} onDone={() => { setModal(null); onDone() }} />}
      {modal === 'wait' && <WaitModal doc={doc} onClose={() => setModal(null)} onDone={() => { setModal(null); onDone() }} />}
      {modal === 'back' && <BackModal doc={doc} onClose={() => setModal(null)} onDone={() => { setModal(null); onDone() }} />}
    </section>
  )
}

/** Schedules one or several transfer orders on one trip. */
export function ScheduleModal({ ids, current, onClose, onDone }: { ids: number[]; current?: Doc; onClose: () => void; onDone: () => void }) {
  const partners = useApi<Partner[]>('/delivery-partners')
  const [slots, setSlots] = useState<string[]>([])
  useEffect(() => { get<Record<string, string[]>>('/pick-lists').then((l) => setSlots(l.delivery_slots || [])) }, [])
  const d = current?.delivery
  const [f, setF] = useState({ partner_id: d?.partner_id || 0, date: d?.date || addDays(today(), 1), slot: d?.slot || '', vehicle: d?.vehicle || '', note: d?.note || '' })
  useEffect(() => { if (!f.slot && slots.length) setF((x) => ({ ...x, slot: slots[1] || slots[0] })) }, [slots, f.slot])
  useEffect(() => { const p = partners.data?.find((x) => x.active); if (!f.partner_id && p) setF((x) => ({ ...x, partner_id: p.id })) }, [partners.data, f.partner_id])
  const a = useAction()
  return (
    <Modal title={ids.length > 1 ? `Schedule ${ids.length} orders on one trip` : d ? 'Reschedule delivery' : 'Schedule delivery'} onClose={onClose}>
      <p className="sub">The property sees the date, time slot and delivery partner on its request as soon as you save, and gets a WhatsApp message.</p>
      <div className="grid2">
        <Field label="Delivery partner"><select value={f.partner_id} onChange={(e) => setF({ ...f, partner_id: +e.target.value })}>{partners.data?.filter((p) => p.active).map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></Field>
        <Field label="Vehicle number (optional)"><input value={f.vehicle} onChange={(e) => setF({ ...f, vehicle: e.target.value })} placeholder="e.g. HR 55 AB 1234" /></Field>
        <Field label="Delivery date"><input type="date" min={today()} value={f.date} onChange={(e) => setF({ ...f, date: e.target.value })} /></Field>
        <Field label="Time slot"><select value={f.slot} onChange={(e) => setF({ ...f, slot: e.target.value })}>{slots.map((s) => <option key={s}>{s}</option>)}</select></Field>
      </div>
      <Field label="Note for the property (optional)"><input value={f.note} onChange={(e) => setF({ ...f, note: e.target.value })} placeholder="e.g. Call the store keeper 30 minutes before" /></Field>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post('/warehouse/schedule', { ids, ...f }), (r: any) => { onDone(); return r.message })}>Save schedule</button></div>
    </Modal>
  )
}

function WaitModal({ doc, onClose, onDone }: { doc: Doc; onClose: () => void; onDone: () => void }) {
  const [date, setDate] = useState(doc.wait_until || addDays(today(), 3))
  const [note, setNote] = useState(doc.wait_note || '')
  const a = useAction()
  return (
    <Modal title={'No stock yet: ' + doc.po_number} onClose={onClose}>
      <p className="sub">The order stays open and shows as waiting for stock. If it cannot be supplied at all, return it instead.</p>
      <div className="grid2"><Field label="Stock expected by"><input type="date" min={today()} value={date} onChange={(e) => setDate(e.target.value)} /></Field>
        <Field label="Note (optional)"><input value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. Vendor delivery due Friday" /></Field></div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post(`/documents/${doc.id}/wait`, { Date: date, Note: note }), (r: any) => { onDone(); return r.message })}>Save</button></div>
    </Modal>
  )
}

function BackModal({ doc, onClose, onDone }: { doc: Doc; onClose: () => void; onDone: () => void }) {
  const [list, setList] = useState<string[]>([])
  const [reason, setReason] = useState('')
  const [note, setNote] = useState('')
  useEffect(() => { get<Record<string, string[]>>('/pick-lists').then((l) => setList(l.wh_back_reasons || [])) }, [])
  const a = useAction()
  return (
    <Modal title={'Return order to the property: ' + doc.po_number} onClose={onClose}>
      <p className="sub">The order closes and the property's budget is released. The warehouse is the mapped vendor, so there is no second vendor to fall back on: the property raises it again when stock is back, or Purchase changes the mapping.</p>
      <Field label="Reason"><select value={reason} onChange={(e) => setReason(e.target.value)}><option value="">Choose a reason</option>{list.map((r) => <option key={r}>{r}</option>)}</select></Field>
      <Field label="Remark"><textarea value={note} onChange={(e) => setNote(e.target.value)} /></Field>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn no" disabled={a.busy || !reason} onClick={() => a.run(() => post(`/documents/${doc.id}/cannot-supply`, { Reason: reason, Note: note }), (r: any) => { onDone(); return r.message })}>Return order</button></div>
    </Modal>
  )
}
