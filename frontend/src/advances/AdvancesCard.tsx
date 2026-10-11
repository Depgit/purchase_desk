// The advances on one purchase order, on its document page: ask for an advance (whoever issues the PO), and the
// Finance steps on each advance. Shown once the PO is issued.
import { useEffect, useState } from 'react'
import { fileUrl, get, upload } from '../api/client'
import type { Advance } from '../api/types'
import { useApi } from '../api/useApi'
import { day, inr } from '../format/format'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Seg } from '../ui/Tabs'
import { useAction } from '../ui/useAction'
import { AdvanceActions, AdvanceStatus } from './AdvanceActions'

interface ForDoc { advances: Advance[]; can_request: boolean; po_value: number; requested: number }

export function AdvancesCard({ docId, poNumber, onDone }: { docId: number; poNumber: string; onDone: () => void }) {
  const { data, reload } = useApi<ForDoc>(`/documents/${docId}/advances`)
  const [asking, setAsking] = useState(false)
  if (!data || (!data.advances.length && !data.can_request)) return null
  const done = () => { reload(); onDone() }
  return (
    <section className="card flush">
      <div className="shead" style={{ background: 'none', borderTop: 0 }}><div style={{ flex: 1 }}><h2>Advances</h2>
        <p className="sub">PO value {inr(data.po_value)}{data.requested > 0 ? ` · advances asked ${inr(data.requested)}` : ''}. An advance is adjusted on the vendor's invoice at payment approval.</p></div>
        {data.can_request && <button className="btn ghost" onClick={() => setAsking(true)}>Request advance</button>}</div>
      {data.advances.length > 0 && <div className="scroll"><table>
        <thead><tr><th>Advance</th><th className="r">Amount</th><th>Reason</th><th>Status</th><th>Paid</th><th className="r">Adjusted</th><th></th></tr></thead>
        <tbody>{data.advances.map((a) => <tr key={a.id}>
          <td className="id">{a.number}<br /><span className="hint">{a.requester} · {day(a.requested_at)}</span></td>
          <td className="r">{inr(a.amount)}{a.pct ? <><br /><span className="hint">{a.pct}% of PO</span></> : null}{a.tds_amount > 0 ? <><br /><span className="hint">TDS {inr(a.tds_amount)}</span></> : null}</td>
          <td>{a.reason}{a.note ? <><br /><span className="hint">{a.note}</span></> : null}{a.proforma_file_id ? <><br /><a className="link" href={fileUrl(a.proforma_file_id)} target="_blank" rel="noreferrer">Proforma</a></> : null}</td>
          <td><AdvanceStatus a={a} />{a.decision_note ? <><br /><span className="hint">{a.decision_note}</span></> : null}</td>
          <td>{a.paid_on ? <>{day(a.paid_on)}<br /><span className="hint">{a.payment_ref}</span></> : '—'}</td>
          <td className="r">{a.adjusted ? inr(a.adjusted) : '—'}{a.refunded ? <><br /><span className="hint">refunded {inr(a.refunded)}</span></> : null}</td>
          <td className="r"><AdvanceActions a={a} onDone={done} /></td></tr>)}</tbody></table></div>}
      {asking && <RequestModal docId={docId} poNumber={poNumber} poValue={data.po_value} asked={data.requested} onClose={() => setAsking(false)} onDone={() => { setAsking(false); done() }} />}
    </section>
  )
}

function RequestModal({ docId, poNumber, poValue, asked, onClose, onDone }: { docId: number; poNumber: string; poValue: number; asked: number; onClose: () => void; onDone: () => void }) {
  const act = useAction()
  const [mode, setMode] = useState<'pct' | 'amount'>('pct')
  const [val, setVal] = useState('50')
  const [reason, setReason] = useState('')
  const [reasons, setReasons] = useState<string[]>([])
  const [note, setNote] = useState('')
  const [file, setFile] = useState<File | null>(null)
  useEffect(() => { get<Record<string, string[]>>('/pick-lists').then((l) => { setReasons(l.advance_reasons || []); setReason((l.advance_reasons || [])[0] || '') }).catch(() => {}) }, [])
  const amount = mode === 'pct' ? Math.round(poValue * (+val || 0)) / 100 : +val || 0
  return (
    <Modal title={`Request an advance on ${poNumber}`} onClose={onClose}>
      <p className="sub">PO value {inr(poValue)}{asked > 0 ? `, advances already asked ${inr(asked)}, so at most ${inr(Math.max(0, poValue - asked))} more` : ''}. Finance approves and pays it; it is then adjusted on the vendor's invoice.</p>
      <div className="field"><span>Advance as</span><Seg value={mode} onChange={(m) => { setMode(m); setVal(m === 'pct' ? '50' : '') }} items={[['pct', '% of PO value'], ['amount', 'Amount (₹)']]} /></div>
      <Field label={mode === 'pct' ? 'Percent of the PO value' : 'Amount including GST'} hint={mode === 'pct' ? `= ${inr(amount)}` : undefined}>
        <input type="number" value={val} onChange={(e) => setVal(e.target.value)} /></Field>
      <Field label="Reason"><select value={reason} onChange={(e) => setReason(e.target.value)}>{reasons.map((r) => <option key={r}>{r}</option>)}</select></Field>
      <Field label={reason.startsWith('Other') ? 'Note (needed for Other)' : 'Note (optional)'}><textarea value={note} onChange={(e) => setNote(e.target.value)} /></Field>
      <Field label="Vendor's proforma invoice or quotation (PDF or image)"><input type="file" accept="application/pdf,image/*" onChange={(e) => setFile(e.target.files?.[0] || null)} /></Field>
      <Errors list={act.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={act.busy || amount <= 0} onClick={() => act.run(() => upload(`/documents/${docId}/advances`, mode === 'pct' ? { pct: +val, reason, note } : { amount: +val, reason, note }, { proforma: file }), (r: any) => { onDone(); return r.message })}>
          Send to Finance</button></div>
    </Modal>
  )
}
