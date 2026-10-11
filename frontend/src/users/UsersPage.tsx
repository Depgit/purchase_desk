// Who can sign in, with which roles, for which properties and categories; and the rights each role carries.
// The System Admin manages everyone and can let a user add users of chosen roles; such a user sees and edits only
// the users they added, with those roles, inside their own property access (the server checks the same).
import { useState } from 'react'
import { post, put } from '../api/client'
import { refreshLookups, useLookups } from '../api/lookups'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { UploadModal } from '../bulkupload/UploadModal'
import { ActiveToggle } from '../ui/ActiveToggle'
import { DataTable } from '../ui/DataTable'
import { Errors } from '../ui/Errors'
import { Checks, Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'
import { useToast } from '../ui/Toast'

interface RoleGrant { role_id: number; role_name?: string; step?: string; scope: string; cluster_ids: number[]; property_ids: number[]; category_ids: number[] }
interface User { id: number; name: string; email: string; mobile: string; department: string; has_password: boolean; active: boolean; roles: RoleGrant[]
  creatable_roles: number[]; created_by: number; can_edit: boolean }

const RIGHTS: [string, string][] = [['raise', 'Raise request or bill'], ['approve', 'Approve'], ['issue_po', 'Issue PO'], ['receive', 'Receive or dispatch goods'], ['match', 'Match invoice'], ['pay', 'Payment and Zoho'], ['masters', 'Edit masters'], ['upload', 'Upload opening and bulk data']]
const STEPS: [string, string][] = [['cc', 'Cost Controller: approves requests'], ['l2', 'Cluster Manager: approves event requests and bills'], ['preop', 'Pre-opening Team: approves warehouse and pre-opening orders'],
  ['purchase', 'Purchase Team: issues POs'], ['store', 'Receiving / Store: receives goods'], ['wh', 'Warehouse Manager: dispatches and receives'], ['ap', 'Accounts Payable: matches invoices'],
  ['finance', 'Finance: approves payment'], ['staff', 'Property staff: raises requests'], ['l1', 'Property Manager: raises requests'], ['', 'No step in the flow: view and reports only']]

export function UsersPage() {
  const { me } = useSession()
  const l = useLookups()
  const { data, reload } = useApi<User[]>('/users')
  const [edit, setEdit] = useState<User | 'new' | null>(null)
  const [newRole, setNewRole] = useState(false)
  const [upload, setUpload] = useState(false)
  const toast = useToast()
  const admin = !!me?.is_admin
  const mine = me?.creatable_roles || []
  const adds = admin || mine.length > 0
  const roleNames = (ids: number[]) => ids.map((id) => l?.roles.find((r) => r.id === id)?.name).filter(Boolean).join(', ')
  const scopeText = (g: RoleGrant) => g.scope === 'all' ? 'All properties' : g.scope === 'clusters' ? g.cluster_ids.map((c) => l?.clusters.find((x) => x.id === c)?.name).join(', ') : g.property_ids.map((p) => l?.properties.find((x) => x.id === p)?.code).join(', ')
  const toggleRight = async (r: { id: number; name: string; step: string; rights: string[] }, right: string) => {
    const rights = r.rights.includes(right) ? r.rights.filter((x) => x !== right) : [...r.rights, right]
    try { await put('/roles/' + r.id, { name: r.name, step: r.step, rights }); toast(`${r.name}: ${right} ${rights.includes(right) ? 'allowed' : 'removed'}`); refreshLookups() } catch (e: any) { toast(e.message, true) }
  }
  return (
    <>
      <div className="head"><div><h1>Users &amp; Rights</h1><p className="sub">Users sign in with email or mobile. A user can hold several roles, each with its own property and category scope. Nobody is deleted; inactive users cannot sign in.</p>
        {!admin && mine.length > 0 && <p className="note">You can add users as {roleNames(mine)}, for the properties you have access to. You can edit or deactivate only the users you added.</p>}</div>
        <div className="acts">{admin && <button className="btn ghost" onClick={() => setUpload(true)}>Bulk upload / edit users</button>}{adds && <button className="btn" onClick={() => setEdit('new')}>+ Add user</button>}</div></div>
      <DataTable screen="users" rows={data || []} rowKey={(u) => u.id} dim={(u) => !u.active} search={(u) => [u.name, u.email, u.mobile, u.department, ...u.roles.map((r) => r.role_name || '')].join(' ')}
        filters={[{ id: 'role', label: 'Role', multi: true, options: [['', 'All roles'], ...(l?.roles || []).map((r) => [String(r.id), r.name] as [string, string])], test: (u, v) => u.roles.some((g) => String(g.role_id) === v) }]}
        columns={[
          { key: 'name', label: 'Name', render: (u) => u.name, csv: (u) => u.name },
          { key: 'login', label: 'Signs in with', render: (u) => [u.email, u.mobile].filter(Boolean).join(' · '), csv: (u) => u.email || u.mobile },
          { key: 'dep', label: 'Department', render: (u) => u.department, csv: (u) => u.department },
          { key: 'roles', label: 'Roles and access', render: (u) => u.roles.map((g) => <div key={g.role_id}>{g.role_name} <span className="hint">· {scopeText(g)}{g.category_ids.length ? ' · ' + g.category_ids.map((c) => l?.categories.find((x) => x.id === c)?.name).join(', ') : ''}</span></div>),
            csv: (u) => u.roles.map((g) => g.role_name).join(' + ') },
          { key: 'adds', label: 'Can add users as', render: (u) => roleNames(u.creatable_roles) || '—', csv: (u) => roleNames(u.creatable_roles) },
          { key: 'pwd', label: 'Password', render: (u) => u.has_password ? 'Set' : <Pill tone="warn">Code only</Pill> },
          { key: 'status', label: 'Status', render: (u) => <ActiveToggle kind="user" id={u.id} name={u.name} active={u.active} onDone={reload} canEdit={u.can_edit && u.id !== me?.id} />, csv: (u) => u.active ? 'Active' : 'Inactive' },
          { key: 'edit', label: '', render: (u) => u.can_edit ? <button className="link" onClick={() => setEdit(u)}>Edit</button> : null },
        ]} csvName="users" />
      <section className="card flush"><div className="shead" style={{ background: 'none', borderTop: 0 }}><div style={{ flex: 1 }}><h2>Rights by role</h2>
        <p className="sub">{admin ? 'Tick or untick a right and it applies at once. A right works only at the step the role sits at.' : 'Only the System Admin can change rights or create roles.'}</p></div>
        {admin && <button className="btn" onClick={() => setNewRole(true)}>+ Create role</button>}</div>
        <div className="scroll"><table><thead><tr><th>Role</th><th>Step</th>{RIGHTS.map(([, n]) => <th key={n}>{n}</th>)}<th className="r">Users</th><th></th></tr></thead>
          <tbody>{(l?.roles || []).map((r) => <tr key={r.id} className={r.active ? '' : 'dim'}><td className="id">{r.name}{r.is_custom && <> <Pill>Custom</Pill></>}</td><td>{r.step || '—'}</td>
            {RIGHTS.map(([k]) => <td key={k}>{admin && r.step !== 'admin' ? <input type="checkbox" checked={r.rights.includes(k)} onChange={() => toggleRight(r, k)} aria-label={`${r.name}: ${k}`} /> : r.rights.includes(k) ? <Pill tone="ok">Yes</Pill> : <span className="sub">—</span>}</td>)}
            <td className="r">{r.users}</td><td>{r.is_custom && <ActiveToggle kind="role" id={r.id} name={r.name} active={r.active} onDone={refreshLookups} />}</td></tr>)}</tbody></table></div></section>
      {edit && <UserModal u={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onDone={() => { setEdit(null); reload(); refreshLookups() }} />}
      {newRole && <RoleModal onClose={() => setNewRole(false)} />}
      {upload && <UploadModal kind="users" title="Bulk upload or edit users" onClose={() => setUpload(false)} onDone={() => { reload(); refreshLookups() }} />}
    </>
  )
}

function UserModal({ u, onClose, onDone }: { u: User | null; onClose: () => void; onDone: () => void }) {
  const l = useLookups()
  const { me } = useSession()
  const admin = !!me?.is_admin
  // Someone the Admin lets add users gives only their allowed roles, and only property access inside their own.
  const mine = me?.creatable_roles || []
  const roleOpts = (l?.roles || []).filter((r) => r.active && (admin || mine.includes(r.id) || u?.roles.some((g) => g.role_id === r.id)))
  const myAll = admin || !!me?.roles.some((g) => g.scope === 'all')
  const myClusters = me?.roles.filter((g) => g.scope === 'clusters').flatMap((g) => g.cluster_ids) || []
  const myProps = me?.roles.filter((g) => g.scope === 'properties').flatMap((g) => g.property_ids) || []
  const clusterOpts = (l?.clusters || []).filter((c) => myAll || myClusters.includes(c.id))
  const propOpts = (l?.properties || []).filter((p) => p.active && (myAll || myClusters.includes(p.cluster_id) || myProps.includes(p.id)))
  const firstRole = admin ? l?.roles.find((r) => r.code === 'staff')?.id : roleOpts[0]?.id
  const [f, setF] = useState({ name: u?.name || '', email: u?.email || '', mobile: u?.mobile || '', department: u?.department || '', password: '',
    roles: u?.roles.map((g) => ({ ...g })) || [{ role_id: firstRole || 0, scope: 'properties', cluster_ids: [], property_ids: [], category_ids: [] } as RoleGrant],
    creatable_roles: u?.creatable_roles || [] })
  const a = useAction()
  const setRole = (i: number, patch: Partial<RoleGrant>) => setF({ ...f, roles: f.roles.map((g, j) => j === i ? { ...g, ...patch } : g) })
  return (
    <Modal title={u ? 'Edit ' + u.name : 'Add user'} onClose={onClose} wide>
      <div className="grid2">
        <Field label="Name"><input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
        <Field label="Department"><input value={f.department} onChange={(e) => setF({ ...f, department: e.target.value })} /></Field>
        <Field label="Email"><input type="email" value={f.email} onChange={(e) => setF({ ...f, email: e.target.value })} /></Field>
        <Field label="Mobile number"><input inputMode="numeric" maxLength={10} value={f.mobile} onChange={(e) => setF({ ...f, mobile: e.target.value })} /></Field>
        <Field label={u ? 'New password (leave blank to keep)' : 'Password (optional)'} hint="At least 8 characters. Without one, the user signs in with a one-time code.">
          <input type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} /></Field>
      </div>
      <h3>Roles</h3>
      {f.roles.map((g, i) => (
        <div key={i} className="xpbox">
          <div className="grid2">
            <Field label="Role"><select value={g.role_id} onChange={(e) => setRole(i, { role_id: +e.target.value })}>{roleOpts.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}</select></Field>
            <Field label="Property access"><select value={g.scope} onChange={(e) => setRole(i, { scope: e.target.value })}>{(myAll || g.scope === 'all') && <option value="all">All properties</option>}
              {(clusterOpts.length > 0 || g.scope === 'clusters') && <option value="clusters">Whole clusters</option>}<option value="properties">Named properties</option></select></Field>
          </div>
          {g.scope === 'clusters' && <Checks value={g.cluster_ids} onChange={(v) => setRole(i, { cluster_ids: v })} options={clusterOpts.map((c) => [c.id, c.name])} />}
          {g.scope === 'properties' && <Checks value={g.property_ids} onChange={(v) => setRole(i, { property_ids: v })} options={propOpts.map((p) => [p.id, p.name])} />}
          <div className="field"><span>Category limits (leave all unticked for every category)</span>
            <Checks value={g.category_ids} onChange={(v) => setRole(i, { category_ids: v })} options={(l?.categories || []).map((c) => [c.id, c.name])} /></div>
          {f.roles.length > 1 && <div><button className="link" onClick={() => setF({ ...f, roles: f.roles.filter((_, j) => j !== i) })}>Remove this role</button></div>}
        </div>))}
      <div><button className="btn ghost" onClick={() => setF({ ...f, roles: [...f.roles, { role_id: roleOpts[0]?.id || 0, scope: myAll ? 'all' : 'properties', cluster_ids: [], property_ids: [], category_ids: [] }] })}>+ Add another role</button></div>
      {admin && <><h3>Can add users</h3>
        <div className="field"><span>Roles this user may give when adding users (leave all unticked if they should not add users)</span>
          <Checks value={f.creatable_roles} onChange={(v) => setF({ ...f, creatable_roles: v })} options={(l?.roles || []).filter((r) => r.active && r.step !== 'admin').map((r) => [r.id, r.name])} />
          <span className="hint">They then see an Add user button with only these roles, for properties inside their own access, and can edit or deactivate only the users they add.</span></div></>}
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => {
          const body = admin ? f : { ...f, creatable_roles: undefined }
          a.run(() => u ? put('/users/' + u.id, body) : post('/users', body), () => { onDone(); return 'User saved' })
        }}>Save user</button></div>
    </Modal>
  )
}

function RoleModal({ onClose }: { onClose: () => void }) {
  const [f, setF] = useState({ name: '', step: 'cc', rights: ['approve'] })
  const a = useAction()
  return (
    <Modal title="Create role" onClose={onClose}>
      <p className="sub">Give the role a name, say where it sits in the flow, and tick its rights.</p>
      <Field label="Role name"><input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="e.g. Regional Cost Controller" /></Field>
      <Field label="Works in the flow as" hint="Decides which documents wait for this role."><select value={f.step} onChange={(e) => setF({ ...f, step: e.target.value })}>{STEPS.map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></Field>
      <div className="field"><span>Rights</span><Checks value={f.rights} onChange={(v) => setF({ ...f, rights: v })} options={RIGHTS} /></div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post('/roles', f), () => { refreshLookups(); onClose(); return f.name + ' created' })}>Create role</button></div>
    </Modal>
  )
}
