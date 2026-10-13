// Approved requests waiting for a PO, and POs sent to vendors. The Purchase Team can tick several and issue them at once.
import { useState, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { post } from '../api/client'
import { useLookups } from '../api/lookups'
import { PAYMENT_TERMS, type ListRow } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { addDays, today, daysBack } from '../format/format'
import { DataTable } from '../ui/DataTable'
import { Errors } from '../ui/Errors'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'
import { BulkResults, type BulkResult } from '../ui/BulkResults'
import { col, placeFilters, search } from '../requests/columns'
import { useScreen } from '../screenaccess/screen'

export function PurchaseOrdersPage() {
  const nav = useNavigate()
  const l = useLookups()
  const { hasRight } = useSession()
  const sc = useScreen('purchase_orders')
  const [days, setDays] = useState(90)
  const fromChanged = useCallback((f: string) => setDays(daysBack(f)), [])
  const { data, reload } = useApi<ListRow[]>(`/requests?view=po&days=${days}`)
  const [sel, setSel] = useState<Set<string | number>>(new Set())
  const [issuing, setIssuing] = useState(false)
  const [results, setResults] = useState<BulkResult[] | null>(null)
  const rows = data || []
  const st = (r: ListRow) => r.status === 'APPROVED' ? <Pill tone="warn">PO pending</Pill> : r.status === 'PO_ISSUED' ? <Pill>{r.is_transfer ? 'Warehouse to dispatch' : 'Awaiting delivery'}</Pill> : <Pill tone="ok">{r.status_label}</Pill>
  return (
    <>
      <div className="head"><div><h1>Purchase Orders</h1><p className="sub">Approved requests to convert, and orders sent to vendors. Tick several approved requests to issue their POs at once.</p></div>
        {(sc.edit && hasRight('issue_po')) && <div className="acts"><button className="btn" disabled={!sel.size} onClick={() => setIssuing(true)}>Issue POs for selected ({sel.size})</button></div>}</div>
      {results && <BulkResults title="Last bulk issue" results={results} label={(id) => rows.find((r) => r.id === id)?.number || String(id)} onClose={() => setResults(null)} />}
      <DataTable screen="purchase-orders" rows={rows} rowKey={(r) => r.id} date={{ label: 'Raised', value: (r) => r.created_at, onFrom: fromChanged }} search={search} onOpen={(r) => nav('/documents/' + r.id)}
        selectable={(sc.edit && hasRight('issue_po')) ? (r) => r.status === 'APPROVED' && !r.is_transfer : undefined} selected={sel} onSelect={setSel}
        filters={[{ id: 'st', label: 'Status', options: [['', 'All'], ['APPROVED', 'PO pending'], ['PO_ISSUED', 'Awaiting delivery'], ['DONE', 'Received']], test: (r, v) => v === 'DONE' ? !['APPROVED', 'PO_ISSUED'].includes(r.status) : r.status === v }, ...placeFilters(l)]}
        columns={[col.po, col.number, col.vendor, col.property, col.deliver, col.terms, col.amount, { key: 'status', label: 'Status', render: st, csv: (r) => r.status_label }]} csvName="purchase-orders" />
      {issuing && <BulkIssue ids={[...sel] as number[]} onClose={() => setIssuing(false)} onDone={(r) => { setIssuing(false); setSel(new Set()); setResults(r); reload() }} />}
    </>
  )
}

function BulkIssue({ ids, onClose, onDone }: { ids: number[]; onClose: () => void; onDone: (r: BulkResult[]) => void }) {
  const [due, setDue] = useState(addDays(today(), 3))
  const [terms, setTerms] = useState('')
  const a = useAction()
  return (
    <Modal title={`Issue ${ids.length} purchase order${ids.length === 1 ? '' : 's'}`} onClose={onClose}>
      <p className="hint">Each PO goes to its vendor at the rates on the request. A request with an estimated rate is skipped: open it to confirm the rate with the vendor.</p>
      <div className="grid2">
        <label className="field"><span>Deliver by</span><input type="date" min={today()} value={due} onChange={(e) => setDue(e.target.value)} /></label>
        <label className="field"><span>Payment terms</span>
          <select value={terms} onChange={(e) => setTerms(e.target.value)}><option value="">Each vendor's own terms</option>{PAYMENT_TERMS.filter((t) => t !== 'Advance').map((t) => <option key={t}>{t}</option>)}</select>
            <span className="hint">A PO on Advance terms is issued on its own page, where the advance is asked with it.</span></label>
      </div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post<{ done: number; failed: number; results: BulkResult[] }>('/purchase-orders/bulk', { ids, deliver_by: due, payment_terms: terms }),
          (r) => { onDone(r.results); return `${r.done} PO${r.done === 1 ? '' : 's'} issued${r.failed ? `, ${r.failed} not issued` : ''}` })}>Issue {ids.length}</button></div>
    </Modal>
  )
}
