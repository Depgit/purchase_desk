// Users & Rights › Approval matrix: for each live or pre-opening property and item category (purchases), or bill
// type (utility and AMC bills), the L1 and L2 approver. "Not set only" lists the rows still missing one, so the Admin
// fills them in and they drop off; ticking several rows sets the same pair on all of them. Above the table: other gaps
// (warehouse sub-categories with no Warehouse Approver L1, C1 / C2 limits with nobody holding the role).
import { useMemo, useState } from 'react'
import { put } from '../api/client'
import { useApi } from '../api/useApi'
import { UploadModal } from '../bulkupload/UploadModal'
import { useScreen } from '../screenaccess/screen'
import { DataTable } from '../ui/DataTable'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'

interface Row { property_id: number; property: string; property_code: string; property_kind: string; cluster_id: number; kind: 'category' | 'bill_type'; ref_id: number; name: string
  l1_user_id: number; l1: string; l2_user_id: number; l2: string }
interface Person { id: number; name: string; step: 'al1' | 'al2'; all: boolean; cluster_ids: number[]; property_ids: number[] }
interface Gap { what: string; fix: string }
interface Data { rows: Row[]; people: Person[]; gaps: Gap[] }

const keyOf = (r: Row) => `${r.property_id}|${r.kind}|${r.ref_id}`
const covers = (p: Person, r: Row) => p.all || p.cluster_ids.includes(r.cluster_id) || p.property_ids.includes(r.property_id)

export function ApprovalMatrixTab() {
  const { data, reload, setData } = useApi<Data>('/approval-matrix')
  const ed = useScreen('users').edit
  const [onlyMissing, setOnlyMissing] = useState(true)
  const [sel, setSel] = useState<Set<string | number>>(new Set())
  const [bulk, setBulk] = useState({ l1: 0, l2: 0 })
  const [upload, setUpload] = useState(false)
  const a = useAction()
  const rows = useMemo(() => (data?.rows || []).filter((r) => !onlyMissing || !r.l1_user_id || !r.l2_user_id), [data, onlyMissing])
  const missing = (data?.rows || []).filter((r) => !r.l1_user_id || !r.l2_user_id).length
  const choices = (step: 'al1' | 'al2', r?: Row) => (data?.people || []).filter((p) => p.step === step && (!r || covers(p, r)))
  const save = (cells: { property_id: number; kind: string; ref_id: number; l1_user_id: number; l2_user_id: number }[]) =>
    a.run(() => put('/approval-matrix', { cells }), (x: any) => { setSel(new Set()); reload(); return x.message })
  // One cell changed inline: show it at once, then save.
  const setCell = (r: Row, patch: Partial<Row>) => {
    const next = { ...r, ...patch }
    setData(data ? { ...data, rows: data.rows.map((x) => keyOf(x) === keyOf(r) ? next : x) } : data)
    save([{ property_id: r.property_id, kind: r.kind, ref_id: r.ref_id, l1_user_id: next.l1_user_id, l2_user_id: next.l2_user_id }])
  }
  const pick = (r: Row, step: 'al1' | 'al2') => {
    const v = step === 'al1' ? r.l1_user_id : r.l2_user_id
    const opts = choices(step, r)
    if (!ed) return (step === 'al1' ? r.l1 : r.l2) || <Pill tone="bad">Not set</Pill>
    return <select value={v} aria-label={(step === 'al1' ? 'L1 for ' : 'L2 for ') + r.property + ' ' + r.name} className={v ? '' : 'missing'}
      onChange={(e) => setCell(r, step === 'al1' ? { l1_user_id: +e.target.value } : { l2_user_id: +e.target.value })}>
      <option value={0}>{opts.length ? 'Not set' : `Nobody holds Approver ${step === 'al1' ? 'L1' : 'L2'} here`}</option>
      {opts.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select>
  }
  const selRows = (data?.rows || []).filter((r) => sel.has(keyOf(r)))
  return (
    <>
      <p className="note">Every purchase at a property or pre-opening property goes to the <b>L1</b> and then the <b>L2</b> set here for its items' category; a utility or AMC bill to the Cluster Manager, then the L1 and L2 of its bill type. The same person as L1 and L2 approves once. Approvers are picked from people holding the <b>Approver L1</b> / <b>Approver L2</b> role at that property (People tab). A request whose row is not set is refused when it is raised. Requests already raised keep the approvers they started with.</p>
      {(data?.gaps || []).length > 0 && <div className="note warn"><b>Also missing:</b><ul style={{ margin: '6px 0 0 18px' }}>{data!.gaps.map((g) => <li key={g.what}>{g.what}. <span className="hint">{g.fix}.</span></li>)}</ul></div>}
      <div className="tools" style={{ justifyContent: 'space-between' }}>
        <label className="ck"><input type="checkbox" checked={onlyMissing} onChange={(e) => setOnlyMissing(e.target.checked)} /> Not set only ({missing} row{missing === 1 ? '' : 's'})</label>
        {ed && <button className="btn ghost" onClick={() => setUpload(true)}>Bulk upload / edit (Excel)</button>}
      </div>
      {ed && sel.size > 0 && <section className="card act"><h3>Set approvers for {sel.size} ticked row{sel.size === 1 ? '' : 's'}</h3>
        <div className="grid2">
          <Field label="L1"><select value={bulk.l1} onChange={(e) => setBulk({ ...bulk, l1: +e.target.value })}><option value={0}>Keep each row's L1</option>
            {choices('al1').filter((p) => selRows.every((r) => covers(p, r))).map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></Field>
          <Field label="L2"><select value={bulk.l2} onChange={(e) => setBulk({ ...bulk, l2: +e.target.value })}><option value={0}>Keep each row's L2</option>
            {choices('al2').filter((p) => selRows.every((r) => covers(p, r))).map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></Field>
        </div>
        <p className="hint">Only people who hold the role at every ticked property are offered.</p>
        <div className="btns"><button className="btn ghost" onClick={() => setSel(new Set())}>Clear ticks</button>
          <button className="btn" disabled={a.busy || (!bulk.l1 && !bulk.l2)} onClick={() => save(selRows.map((r) => ({ property_id: r.property_id, kind: r.kind, ref_id: r.ref_id, l1_user_id: bulk.l1 || r.l1_user_id, l2_user_id: bulk.l2 || r.l2_user_id })))}>Save for ticked rows</button></div>
      </section>}
      <Errors list={a.errors} />
      <DataTable screen="approval-matrix" rows={rows} rowKey={keyOf} search={(r) => [r.property, r.property_code, r.name, r.l1, r.l2].join(' ')}
        selectable={ed ? () => true : undefined} selected={sel} onSelect={setSel}
        empty={onlyMissing ? 'Every row has its L1 and L2. Untick "Not set only" to change one.' : 'No live or pre-opening property yet.'}
        filters={[
          { id: 'prop', label: 'Property', options: [['', 'All properties'], ...[...new Map((data?.rows || []).map((r) => [String(r.property_id), `${r.property_code} · ${r.property}`])).entries()]], test: (r, v) => String(r.property_id) === v },
          { id: 'kind', label: 'Type', options: [['', 'Categories and bill types'], ['category', 'Item categories (purchases)'], ['bill_type', 'Bill types (bills)']], test: (r, v) => r.kind === v },
        ]}
        columns={[
          { key: 'prop', label: 'Property', render: (r) => <>{r.property_code} · {r.property}{r.property_kind === 'preopening' && <> <Pill>Pre-opening</Pill></>}</>, sort: (r) => r.property_code, csv: (r) => r.property_code },
          { key: 'name', label: 'Category / bill type', render: (r) => <>{r.name}{r.kind === 'bill_type' && <span className="hint"> · bill</span>}</>, sort: (r) => r.name, csv: (r) => r.name },
          { key: 'l1', label: 'L1', render: (r) => pick(r, 'al1'), sort: (r) => r.l1, csv: (r) => r.l1 },
          { key: 'l2', label: 'L2', render: (r) => pick(r, 'al2'), sort: (r) => r.l2, csv: (r) => r.l2 },
          { key: 'st', label: 'Status', render: (r) => r.l1_user_id && r.l2_user_id ? (r.l1_user_id === r.l2_user_id ? <Pill tone="ok">Set · one approval</Pill> : <Pill tone="ok">Set</Pill>) : <Pill tone="bad">Not set</Pill>,
            csv: (r) => r.l1_user_id && r.l2_user_id ? 'Set' : 'Not set' },
        ]} csvName="approval-matrix" />
      {upload && <UploadModal kind="approval_matrix" title="Bulk upload or edit the approval matrix" onClose={() => setUpload(false)} onDone={reload} />}
    </>
  )
}
