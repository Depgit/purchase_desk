// Cities and clusters. A property belongs to a cluster and a cluster to a city; the cluster sets the Cluster Manager.
import { useState } from 'react'
import { post, put } from '../api/client'
import { refreshLookups, useLookups, type Lookups } from '../api/lookups'
import { useSession } from '../auth/session'
import { ActiveToggle } from '../ui/ActiveToggle'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { useAction } from '../ui/useAction'
import { useScreen } from '../screenaccess/screen'
import { UploadModal } from '../bulkupload/UploadModal'

type Cl = Lookups['clusters'][number]

export function CitiesClustersPage() {
  const l = useLookups()
  const { hasRight } = useSession()
  const sc = useScreen('cities')
  const [city, setCity] = useState<Lookups['cities'][number] | 'new' | null>(null)
  const [cl, setCl] = useState<Cl | 'new' | null>(null)
  const [upload, setUpload] = useState(false)
  const ed = sc.edit && hasRight('masters')
  const count = (c: number) => (l?.properties || []).filter((p) => p.cluster_id === c).length
  return (
    <>
      <div className="head"><div><h1>Cities &amp; Clusters</h1><p className="sub">The cluster sets the Cluster Manager. City and cluster are the filters used in approvals, reports and budgets. The city's state code decides CGST + SGST or IGST.</p></div>
        <div className="acts">{ed && <>{hasRight('upload') && <button className="btn ghost" onClick={() => setUpload(true)}>Bulk upload / edit</button>}<button className="btn ghost" onClick={() => setCity('new')}>+ Add city</button><button className="btn" onClick={() => setCl('new')}>+ Add cluster</button></>}</div></div>
      <div className="cols">
        <section className="card flush"><h2>Clusters</h2><div className="scroll"><table>
          <thead><tr><th>Cluster</th><th>City</th><th>Cluster Manager</th><th className="r">Properties</th><th>Status</th><th></th></tr></thead>
          <tbody>{l?.clusters.map((c) => <tr key={c.id} className={c.active ? '' : 'dim'}><td className="id">{c.name}</td><td>{c.city}</td><td>{c.manager_name || 'Not set'}</td><td className="r">{count(c.id)}</td>
            <td><ActiveToggle kind="cluster" id={c.id} name={c.name} active={c.active} onDone={refreshLookups} /></td>
            <td className="r">{ed && <button className="link" onClick={() => setCl(c)}>Edit</button>}</td></tr>)}</tbody></table></div>
          <p className="hint" style={{ padding: '10px 14px' }}>To move a property to another cluster, use Edit on the Properties page.</p></section>
        <section className="card flush"><h2>Cities</h2><div className="scroll"><table>
          <thead><tr><th>City</th><th>State</th><th></th></tr></thead>
          <tbody>{l?.cities.map((c) => <tr key={c.id}><td className="id">{c.name}</td><td>{c.state_code}</td><td className="r">{ed && <button className="link" onClick={() => setCity(c)}>Edit</button>}</td></tr>)}</tbody></table></div></section>
      </div>
      {upload && <UploadModal kind="clusters" title="Bulk upload or edit cities and clusters" onClose={() => setUpload(false)} onDone={refreshLookups} />}
      {city && <CityModal c={city === 'new' ? null : city} onClose={() => setCity(null)} />}
      {cl && <ClusterModal c={cl === 'new' ? null : cl} onClose={() => setCl(null)} />}
    </>
  )
}

function CityModal({ c, onClose }: { c: Lookups['cities'][number] | null; onClose: () => void }) {
  const [name, setName] = useState(c?.name || '')
  const [st, setSt] = useState(c?.state_code || '')
  const a = useAction()
  return (
    <Modal title={c ? 'Edit city' : 'Add city'} onClose={onClose}>
      <Field label="City name"><input value={name} onChange={(e) => setName(e.target.value)} /></Field>
      <Field label="State code" hint="Two letters, for example HR, DL, KA."><input value={st} maxLength={2} onChange={(e) => setSt(e.target.value.toUpperCase())} /></Field>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => c ? put('/cities/' + c.id, { name, state_code: st }) : post('/cities', { name, state_code: st }), () => { refreshLookups(); onClose(); return 'City saved' })}>Save</button></div>
    </Modal>
  )
}

function ClusterModal({ c, onClose }: { c: Cl | null; onClose: () => void }) {
  const l = useLookups()
  const [f, setF] = useState({ name: c?.name || '', city_id: c?.city_id || l?.cities[0]?.id || 0, manager_id: c?.manager_id || 0 })
  const a = useAction()
  const mgrs = (l?.people || []).filter((u) => u.active && u.steps.includes('l2'))
  return (
    <Modal title={c ? 'Edit cluster' : 'Add cluster'} onClose={onClose}>
      <Field label="Cluster name"><input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
      <Field label="City"><select value={f.city_id} onChange={(e) => setF({ ...f, city_id: +e.target.value })}>{l?.cities.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></Field>
      <Field label="Cluster Manager" hint="From users with the Cluster Manager role."><select value={f.manager_id} onChange={(e) => setF({ ...f, manager_id: +e.target.value })}><option value={0}>Not set yet</option>{mgrs.map((u) => <option key={u.id} value={u.id}>{u.name}</option>)}</select></Field>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => c ? put('/clusters/' + c.id, f) : post('/clusters', f), () => { refreshLookups(); onClose(); return 'Cluster saved' })}>Save</button></div>
    </Modal>
  )
}
