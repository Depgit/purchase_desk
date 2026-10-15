// Finance's approval of masters that go to Zoho: new categories, sub-categories, items, properties, bill types and
// expense heads, and edits of categories, sub-categories and items (rejecting an edit puts the old values back). Until
// approved they cannot be used and are not sent to Zoho; once approved, Zoho gets them. Vendors are listed too (new or
// edited: approved by verifying them on the vendor page), and changes a vendor asked for on the vendor portal.
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { fileUrl, post } from '../api/client'
import { useApi } from '../api/useApi'
import { dateTime } from '../format/format'
import { useScreen } from '../screenaccess/screen'
import { DataTable } from '../ui/DataTable'
import { Errors } from '../ui/Errors'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'

interface Row { kind: string; id: number; code: string; name: string; detail: string; created_by: string; created_at: string; waits_for: string; change: string; edit: boolean; file_id: number | null }

const KIND: Record<string, string> = { category: 'Category', subcat: 'Sub-category', item: 'Item', property: 'Property', billtype: 'Bill type', head: 'Expense head', vendor: 'Vendor', vendor_change: "Vendor's own change" }

export function MasterApprovalsPage() {
  const sc = useScreen('master_approvals')
  const nav = useNavigate()
  const { data, reload } = useApi<Row[]>('/master-approvals')
  const [sel, setSel] = useState<Set<string | number>>(new Set())
  const [reject, setReject] = useState<Row[] | null>(null)
  const [notDone, setNotDone] = useState<string[]>([])
  const a = useAction()
  const key = (r: Row) => r.kind + ':' + r.id
  const rows = data || []
  const picked = rows.filter((r) => sel.has(key(r)) && r.kind !== 'vendor')
  const run = (items: Row[], action: 'approve' | 'reject', reason = '') => a.run(
    () => post<{ done: number; failed: number; results: { kind: string; id: number; ok: boolean; message: string }[] }>('/master-approvals/bulk', { items: items.map((r) => ({ kind: r.kind, id: r.id })), action, reason }),
    (r) => {
      setNotDone(r.results.filter((x) => !x.ok).map((x) => `${rows.find((y) => y.kind === x.kind && y.id === x.id)?.name || KIND[x.kind]}: ${x.message}`))
      setSel(new Set()); setReject(null); reload()
      return `${r.done} ${action === 'approve' ? 'approved' : 'rejected'}${r.failed ? `, ${r.failed} not` : ''}`
    })
  return (
    <>
      <div className="head"><div><h1>Masters to approve</h1>
        <p className="sub">New items, categories, sub-categories, properties, bill types and expense heads wait here, and so do edits of categories, sub-categories, items and vendors, and changes vendors ask for on the vendor portal. Until Finance approves one it cannot be used (no requests, bills or budgets) and is not sent to Zoho; once approved it goes to Zoho by itself. Rejecting an edit puts the previous values back. A category is approved before its sub-categories, and those before their items.</p></div>
        {sc.edit && <div className="acts">
          <button className="btn no" disabled={!picked.length || a.busy} onClick={() => setReject(picked)}>Reject selected ({picked.length})</button>
          <button className="btn" disabled={!picked.length || a.busy} onClick={() => run(picked, 'approve')}>Approve selected ({picked.length})</button></div>}</div>
      <Errors list={a.errors} />
      {notDone.length > 0 && <Errors list={notDone} />}
      <DataTable screen="master-approvals" rows={rows} rowKey={key} empty="Nothing waits for Finance."
        selectable={sc.edit ? (r) => r.kind !== 'vendor' : undefined} selected={sel} onSelect={setSel}
        search={(r) => [r.code, r.name, r.detail, r.created_by, KIND[r.kind]].join(' ')}
        filters={[{ id: 'kind', label: 'What', options: [['', 'Everything'], ...Object.entries(KIND)], test: (r, v) => r.kind === v },
          { id: 'new', label: 'New or edit', options: [['', 'New and edits'], ['new', 'New'], ['edit', 'Edits']], test: (r, v) => (r.edit ? 'edit' : 'new') === v }]}
        columns={[
          { key: 'name', label: 'Name', render: (r) => <>{r.name}{r.code && <><br /><span className="hint">{r.code}</span></>}</>, sort: (r) => r.name, csv: (r) => r.name },
          { key: 'kind', label: 'What', render: (r) => <>{KIND[r.kind]}<br /><Pill tone={r.edit ? 'warn' : ''}>{r.edit ? 'Edit' : 'New'}</Pill></>, csv: (r) => KIND[r.kind] + (r.edit ? ' (edit)' : '') },
          { key: 'detail', label: 'Details', render: (r) => <>{r.detail}{r.change && <><br /><b>{r.kind === 'vendor_change' ? 'Asks to change: ' : ''}{r.change}</b></>}
            {r.file_id && <><br /><a className="link" href={fileUrl(r.file_id)} target="_blank" rel="noreferrer">View the new bank proof</a></>}
            {r.waits_for && <><br /><Pill tone="warn">Approve {r.waits_for} first</Pill></>}</>, csv: (r) => [r.detail, r.change].filter(Boolean).join(' · ') },
          { key: 'by', label: 'Added or asked by', render: (r) => <>{r.created_by || '—'}<br /><span className="hint">{dateTime(r.created_at)}</span></>, sort: (r) => r.created_at, csv: (r) => r.created_by },
          { key: 'act', label: '', render: (r) => r.kind === 'vendor'
            ? <button className="link" onClick={() => nav(`/vendors/${r.id}/edit`)}>{r.edit ? 'Check and verify ›' : 'Verify bank details ›'}</button>
            : sc.edit && <span className="tools" style={{ flexWrap: 'nowrap' }}>
              <button className="link" disabled={a.busy} onClick={() => setReject([r])}>Reject</button>
              <button className="btn small" disabled={a.busy} onClick={() => run([r], 'approve')}>Approve</button></span> },
        ]} csvName="masters-to-approve" />
      {reject && <RejectModal count={reject.length} busy={a.busy} errors={a.errors} onClose={() => setReject(null)} onConfirm={(reason) => run(reject, 'reject', reason)} />}
    </>
  )
}

function RejectModal({ count, busy, errors, onClose, onConfirm }: { count: number; busy: boolean; errors: string[]; onClose: () => void; onConfirm: (reason: string) => void }) {
  const [reason, setReason] = useState('')
  return (
    <Modal title={`Reject ${count === 1 ? 'this master' : count + ' masters'}`} onClose={onClose}>
      <p className="sub">A rejected new master goes inactive with your reason. A rejected edit is undone: the previous values come back. A vendor's own change is not made, and the vendor sees your reason.</p>
      <label className="field"><span>Why</span><textarea rows={3} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. duplicate of an existing item" /></label>
      <Errors list={errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn no" disabled={busy || reason.trim().length < 3} onClick={() => onConfirm(reason)}>Reject</button></div>
    </Modal>
  )
}
