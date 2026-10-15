// Access by property: people down the side, properties across, and the role each person holds at each property. A
// cell given for named properties can be changed here to one role or No access. A cell that comes from a role given
// for all properties or a whole cluster shows greyed, and is changed on the user, so such a grant is never split.
import { MultiSelect } from '../ui/MultiSelect'
import { useMemo, useState } from 'react'
import { put } from '../api/client'
import { refreshLookups, useLookups } from '../api/lookups'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { Errors } from '../ui/Errors'
import { useAction } from '../ui/useAction'

interface Prop { id: number; code: string; name: string; cluster_id: number; mine: boolean }
interface Cell { roles: number[]; via: '' | 'properties' | 'clusters' | 'all'; editable: boolean }
interface Row { user_id: number; name: string; employee_id: string; department: string; active: boolean; cells: Record<string, Cell> }
interface Grid { properties: Prop[]; rows: Row[] }

const VIA: Record<string, string> = { all: 'given for all properties', clusters: 'given for the whole cluster', properties: '' }

export function AccessGrid() {
  const { me } = useSession()
  const l = useLookups()
  const { data, reload } = useApi<Grid>('/users/access-grid')
  const [q, setQ] = useState('')
  const [clusters, setClusters] = useState<string[]>([])
  const [inactive, setInactive] = useState(false)
  const a = useAction()
  const admin = !!me?.is_admin
  const giveable = (l?.roles || []).filter((r) => r.active && r.step !== 'admin' && (admin || (me?.user_roles || []).includes(r.id)))
  const roleName = (id: number) => l?.roles.find((r) => r.id === id)?.name || '?'
  const props = useMemo(() => (data?.properties || []).filter((p) => !clusters.length || clusters.includes(String(p.cluster_id))), [data, clusters])
  const rows = useMemo(() => (data?.rows || []).filter((r) => (inactive || r.active) && (!q || (r.name + ' ' + r.employee_id + ' ' + r.department).toLowerCase().includes(q.toLowerCase()))), [data, q, inactive])
  const change = (r: Row, p: Prop, role: number) => a.run(() => put('/users/' + r.user_id + '/property-access', { property_id: p.id, role_id: role }), () => { reload(); refreshLookups() })
  return (
    <>
      <p className="hint">Each cell is the role a person holds at that property. Pick a role or No access to change it. Greyed cells come from a role given for all properties or a whole cluster; change those on the person (People tab).</p>
      <div className="tools">
        <input type="search" placeholder="Search people" value={q} onChange={(e) => setQ(e.target.value)} />
        <MultiSelect ariaLabel="Cluster" value={clusters} onChange={setClusters} options={[['', 'All clusters'], ...(l?.clusters || []).filter((c) => (data?.properties || []).some((p) => p.cluster_id === c.id)).map((c) => [String(c.id), c.name] as [string, string])]} />
        <label className="ck"><input type="checkbox" checked={inactive} onChange={(e) => setInactive(e.target.checked)} />Show inactive people</label>
      </div>
      <Errors list={a.errors} />
      <section className="card"><div className="scroll agrid">
        <table>
          <thead><tr><th>Person</th>{props.map((p) => <th key={p.id} title={p.name}>{p.code}<div className="hint">{p.name}</div></th>)}</tr></thead>
          <tbody>
            {rows.length === 0 && <tr><td colSpan={props.length + 1} className="hint">No people to show.</td></tr>}
            {rows.map((r) => (
              <tr key={r.user_id} className={r.active ? '' : 'dim'}>
                <td><b>{r.name}</b>{(r.employee_id || r.department) && <div className="hint">{[r.employee_id, r.department].filter(Boolean).join(' · ')}</div>}</td>
                {props.map((p) => {
                  const c = r.cells[String(p.id)] || { roles: [], via: '', editable: false }
                  const text = c.roles.map(roleName).join(' + ')
                  if (!c.editable) return <td key={p.id} className={c.via === 'all' || c.via === 'clusters' ? 'via' : ''} title={VIA[c.via] || ''}>{text || <span className="hint">—</span>}</td>
                  const value = c.roles.length === 1 ? c.roles[0] : c.roles.length === 0 ? 0 : -1
                  return (
                    <td key={p.id}><select aria-label={r.name + ' at ' + p.name} value={value} disabled={a.busy} onChange={(e) => change(r, p, +e.target.value)}>
                      <option value={0}>No access</option>
                      {value === -1 && <option value={-1} disabled>{text}</option>}
                      {giveable.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
                      {c.roles.filter((id) => !giveable.some((x) => x.id === id)).map((id) => <option key={id} value={id} disabled>{roleName(id)}</option>)}
                    </select></td>)
                })}
              </tr>))}
          </tbody>
        </table>
      </div></section>
    </>
  )
}
