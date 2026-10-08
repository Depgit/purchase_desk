// Asks for a reason (from the Admin's pick-list) to reject, return, or send a request to the Cluster Manager.
import { useEffect, useState } from 'react'
import { get } from '../api/client'
import { Modal } from '../ui/Modal'
import { Errors } from '../ui/Errors'

export type ReasonKind = 'reject' | 'return' | 'route'
const META: Record<ReasonKind, { title: string; list: string; text: string; button: string; tone: string }> = {
  reject: { title: 'Reject', list: 'reject_reasons', text: 'A rejected request is closed and cannot be reopened.', button: 'Reject', tone: 'no' },
  return: { title: 'Return for changes', list: 'return_reasons', text: 'The requester will be asked to change and resubmit.', button: 'Return', tone: 'amber' },
  route: { title: 'Send to Cluster Manager', list: 'route_reasons', text: 'The Cluster Manager decides. If approved there, the request goes straight to the PO stage.', button: 'Send', tone: '' },
}

export function ReasonModal({ kind, count, onClose, onConfirm, busy, errors }: {
  kind: ReasonKind; count: number; onClose: () => void; onConfirm: (reason: string, note: string) => void; busy?: boolean; errors?: string[]
}) {
  const m = META[kind]
  const [list, setList] = useState<string[]>([])
  const [reason, setReason] = useState('')
  const [note, setNote] = useState('')
  useEffect(() => { get<Record<string, string[]>>('/pick-lists').then((l) => setList(l[m.list] || [])) }, [m.list])
  const other = reason.startsWith('Other')
  return (
    <Modal title={`${m.title}${count > 1 ? `: ${count} requests` : ''}`} onClose={onClose}>
      <p className="sub">{m.text}</p>
      <label className="field"><span>Reason</span>
        <select value={reason} onChange={(e) => setReason(e.target.value)}><option value="">Choose a reason</option>{list.map((r) => <option key={r}>{r}</option>)}</select></label>
      <label className="field"><span>Remark{other ? '' : ' (optional)'}</span><textarea value={note} onChange={(e) => setNote(e.target.value)} /></label>
      <Errors list={errors || []} />
      <div className="btns">
        <button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className={'btn ' + m.tone} disabled={busy || !reason || (other && note.trim().length < 3)} onClick={() => onConfirm(reason, note)}>{m.button}</button>
      </div>
    </Modal>
  )
}
