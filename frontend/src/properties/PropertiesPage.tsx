// Properties with their eZee code, cluster, approvers, stage, and the F&B cost % that turn sales into the F&B budget.
import { useState } from 'react'
import { post, put } from '../api/client'
import { refreshLookups, useLookups } from '../api/lookups'
import type { Property } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { UploadModal } from '../bulkupload/UploadModal'
import { ActiveToggle } from '../ui/ActiveToggle'
import { DataTable } from '../ui/DataTable'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { PHASE, usePhase } from '../releasephase/phase'
import { useAction } from '../ui/useAction'

const KIND: Record<string, string> = { live: 'Live', warehouse: 'Warehouse', preopening: 'Pre-opening', corporate: 'Corporate office' }

export function PropertiesPage() {
  const l = useLookups()
  const { hasRight, hasStep } = useSession()
  const bud = usePhase().on(PHASE.budgets) // F&B cost % feeds budgets, which open in phase 3
  const { data, reload } = useApi<Property[]>('/properties')
  const [edit, setEdit] = useState<Property | 'new' | null>(null)
  const [pct, setPct] = useState<Property | null>(null)
  const [upload, setUpload] = useState<'properties' | 'property_pct' | null>(null)
  const ed = hasRight('masters')
  const done = () => { reload(); refreshLookups() }
  return (
    <>
      <div className="head"><div><h1>Properties</h1><p className="sub">{bud ? <>Locations with eZee code, cluster, approvers and the F&amp;B cost % used to turn sales into the F&amp;B budget.</> : 'Locations with eZee code, cluster and approvers.'}</p></div>
        <div className="acts">{ed && <>{hasRight('upload') && <><button className="btn ghost" onClick={() => setUpload('properties')}>Bulk upload properties</button>
          {bud && <button className="btn ghost" onClick={() => setUpload('property_pct')}>Bulk upload F&amp;B cost %</button>}</>}
          <button className="btn" onClick={() => setEdit('new')}>+ Add property</button></>}</div></div>
      <DataTable screen="properties" rows={data || []} rowKey={(p) => p.id} dim={(p) => !p.active} search={(p) => [p.code, p.name, p.ezee_code, p.l1_name, p.cluster].join(' ')}
        filters={[{ id: 'cl', label: 'Cluster', options: [['', 'All clusters'], ...(l?.clusters || []).map((c) => [String(c.id), c.name] as [string, string])], test: (p, v) => String(p.cluster_id) === v },
          { id: 'kind', label: 'Stage', options: [['', 'All stages'], ...Object.entries(KIND)], test: (p, v) => p.kind === v }]}
        columns={[
          { key: 'code', label: 'Code', render: (p) => p.code, csv: (p) => p.code },
          { key: 'name', label: 'Property', render: (p) => p.name, csv: (p) => p.name },
          { key: 'ezee', label: 'eZee code', render: (p) => p.ezee_code, csv: (p) => p.ezee_code },
          { key: 'cluster', label: 'Cluster', render: (p) => p.cluster, csv: (p) => p.cluster },
          { key: 'city', label: 'City', render: (p) => `${p.city} (${p.state_code})`, csv: (p) => p.city },
          { key: 'kind', label: 'Stage', render: (p) => p.kind === 'live' ? 'Live' : <Pill tone="warn">{KIND[p.kind]}</Pill>, csv: (p) => p.kind },
          { key: 'l1', label: 'Property Manager', render: (p) => p.l1_name || '—', csv: (p) => p.l1_name },
          { key: 'l2', label: 'Cluster Manager', render: (p) => p.l2_name || '—', csv: (p) => p.l2_name },
          ...(bud ? [{ key: 'pct', label: 'Plan / IRD / Banquet cost %', render: (p: Property) => <>{p.plan_pct} / {p.ird_pct} / {p.banquet_pct}{(ed || hasStep('cc')) && <> <button className="link" onClick={() => setPct(p)}>Edit</button></>}</>, csv: (p: Property) => `${p.plan_pct}/${p.ird_pct}/${p.banquet_pct}` }] : []),
          { key: 'status', label: 'Status', render: (p) => <ActiveToggle kind="property" id={p.id} name={p.name} active={p.active} reason={p.inactive_reason} onDone={done} />, csv: (p) => p.active ? 'Active' : 'Inactive' },
          { key: 'edit', label: '', render: (p) => ed ? <button className="link" onClick={() => setEdit(p)}>Edit</button> : null },
        ]} csvName="properties" />
      {edit && <PropertyModal p={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onDone={() => { setEdit(null); done() }} />}
      {pct && <PctModal p={pct} onClose={() => setPct(null)} onDone={() => { setPct(null); done() }} />}
      {upload && <UploadModal kind={upload} title={upload === 'properties' ? 'Bulk upload properties' : 'Bulk upload F&B cost % by property'} onClose={() => setUpload(null)} onDone={done} />}
    </>
  )
}

function PropertyModal({ p, onClose, onDone }: { p: Property | null; onClose: () => void; onDone: () => void }) {
  const l = useLookups()
  const [f, setF] = useState({ name: p?.name || '', ezee_code: p?.ezee_code || '', cluster_id: p?.cluster_id || l?.clusters[0]?.id || 0, l1_user_id: p?.l1_user_id || 0,
    kind: p?.kind || 'live', plan_pct: p?.plan_pct ?? 35, ird_pct: p?.ird_pct ?? 32, banquet_pct: p?.banquet_pct ?? 38, copy_from: 0 })
  const a = useAction()
  const l1s = (l?.people || []).filter((u) => u.active && u.steps.includes('l1'))
  const phase = usePhase()
  const kindOn = (k: string) => k === 'warehouse' ? phase.on(PHASE.warehouse) : k === 'corporate' ? phase.on(PHASE.corporate) : true
  return (
    <Modal title={p ? 'Edit ' + p.name : 'Add property'} onClose={onClose} wide>
      <div className="grid2">
        <Field label="Property name"><input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
        <Field label="eZee code"><input value={f.ezee_code} onChange={(e) => setF({ ...f, ezee_code: e.target.value })} placeholder="e.g. EZ-10441" /></Field>
        <Field label="Cluster" hint="City and Cluster Manager follow the cluster."><select value={f.cluster_id} onChange={(e) => setF({ ...f, cluster_id: +e.target.value })}>{l?.clusters.map((c) => <option key={c.id} value={c.id}>{c.name} · {c.city}</option>)}</select></Field>
        <Field label="Stage" hint="A pre-opening property has no monthly budget; its orders go to the Pre-opening Team.">
          <select value={f.kind} onChange={(e) => setF({ ...f, kind: e.target.value })}>{Object.entries(KIND).filter(([k]) => kindOn(k) || k === f.kind).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></Field>
        <Field label="Property Manager (L1)"><select value={f.l1_user_id} onChange={(e) => setF({ ...f, l1_user_id: +e.target.value })}><option value={0}>Not set</option>{l1s.map((u) => <option key={u.id} value={u.id}>{u.name}</option>)}</select></Field>
        {!p && <Field label="Copy vendors from" hint="The new property gets the same vendor for every item. Adjust later under Vendor Mapping.">
          <select value={f.copy_from} onChange={(e) => setF({ ...f, copy_from: +e.target.value })}><option value={0}>Do not copy, map later</option>{l?.properties.filter((x) => x.active && x.kind === 'live').map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></Field>}
        {phase.on(PHASE.budgets) && <>
        <Field label="Plan sale cost %"><input type="number" value={f.plan_pct} onChange={(e) => setF({ ...f, plan_pct: +e.target.value })} /></Field>
        <Field label="IRD sale cost %"><input type="number" value={f.ird_pct} onChange={(e) => setF({ ...f, ird_pct: +e.target.value })} /></Field>
        <Field label="Banquet sale cost %"><input type="number" value={f.banquet_pct} onChange={(e) => setF({ ...f, banquet_pct: +e.target.value })} /></Field></>}
      </div>
      {phase.on(PHASE.budgets) && <p className="hint">Budgets start at zero. Set them under Budgets after saving.</p>}
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => p ? put('/properties/' + p.id, f) : post('/properties', f), () => { onDone(); return 'Property saved' })}>Save property</button></div>
    </Modal>
  )
}

function PctModal({ p, onClose, onDone }: { p: Property; onClose: () => void; onDone: () => void }) {
  const [f, setF] = useState({ PlanPct: p.plan_pct, IRDPct: p.ird_pct, BanquetPct: p.banquet_pct })
  const a = useAction()
  return (
    <Modal title={'F&B cost % at ' + p.name} onClose={onClose}>
      <p className="sub">Allowed F&amp;B cost for a day = each sale type × its cost %. A change applies to the whole current month and is logged.</p>
      <div className="grid3">
        <Field label="Plan %"><input type="number" value={f.PlanPct} onChange={(e) => setF({ ...f, PlanPct: +e.target.value })} /></Field>
        <Field label="IRD %"><input type="number" value={f.IRDPct} onChange={(e) => setF({ ...f, IRDPct: +e.target.value })} /></Field>
        <Field label="Banquet %"><input type="number" value={f.BanquetPct} onChange={(e) => setF({ ...f, BanquetPct: +e.target.value })} /></Field>
      </div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => put(`/properties/${p.id}/pct`, f), () => { onDone(); return 'Cost % saved' })}>Save</button></div>
    </Modal>
  )
}
