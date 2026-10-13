// Users & Rights, in three tabs. People: who can sign in, with which roles, for which properties and categories.
// Roles: each role's action rights, screen levels, reports and special rights (RolesTab). Access by property: people
// against properties, with the role each holds there (AccessGrid). A role with the create-users right adds users,
// giving only the roles it allows, and sees and edits only the users it added; the server checks the same.
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
import { Tabs } from '../ui/Tabs'
import { useAction } from '../ui/useAction'
import { AccessGrid } from './AccessGrid'
import { RolesTab } from './RolesTab'

interface RoleGrant { role_id: number; role_name?: string; step?: string; scope: string; cluster_ids: number[]; property_ids: number[]; category_ids: number[] }
interface User { id: number; name: string; email: string; mobile: string; department: string; has_password: boolean; active: boolean; roles: RoleGrant[]
  created_by: number; can_edit: boolean }

type Tab = 'people' | 'roles' | 'access'

export function UsersPage() {
  const [tab, setTab] = useState<Tab>('people')
  return (
    <>
      <div className="head"><div><h1>Users &amp; Rights</h1><p className="sub">People sign in with email or mobile. A role says what someone can do and see: its action rights, a level on every screen (no access, view or edit) and its reports. Access by property says where.</p></div></div>
      <Tabs value={tab} onChange={setTab} items={[['people', 'People'], ['roles', 'Roles'], ['access', 'Access by property']]} />
      {tab === 'people' && <People />}
      {tab === 'roles' && <RolesTab />}
      {tab === 'access' && <AccessGrid />}
    </>
  )
}

function People() {
  const { me } = useSession()
  const l = useLookups()
  const { data, reload } = useApi<User[]>('/users')
  const [edit, setEdit] = useState<User | 'new' | null>(null)
  const [upload, setUpload] = useState(false)
  const admin = !!me?.is_admin
  const adds = admin || !!me?.adds_users
  const roleNames = (ids: number[]) => ids.map((id) => l?.roles.find((r) => r.id === id)?.name).filter(Boolean).join(', ')
  const scopeText = (g: RoleGrant) => g.scope === 'all' ? 'All properties' : g.scope === 'clusters' ? g.cluster_ids.map((c) => l?.clusters.find((x) => x.id === c)?.name).join(', ') : g.property_ids.map((p) => l?.properties.find((x) => x.id === p)?.code).join(', ')
  return (
    <>
      {!admin && adds && <p className="note">Your role lets you add people as {roleNames(me?.user_roles || []) || 'no role yet'}, {me?.user_scope_any ? 'at any property' : 'for the properties you have access to'}. You can edit or deactivate only the people you added.</p>}
      <div className="tools" style={{ justifyContent: 'flex-end' }}>{admin && <button className="btn ghost" onClick={() => setUpload(true)}>Bulk upload / edit users</button>}{adds && <button className="btn" onClick={() => setEdit('new')}>+ Add user</button>}</div>
      <DataTable screen="users" rows={data || []} rowKey={(u) => u.id} dim={(u) => !u.active} search={(u) => [u.name, u.email, u.mobile, u.department, ...u.roles.map((r) => r.role_name || '')].join(' ')}
        filters={[{ id: 'role', label: 'Role', multi: true, options: [['', 'All roles'], ...(l?.roles || []).map((r) => [String(r.id), r.name] as [string, string])], test: (u, v) => u.roles.some((g) => String(g.role_id) === v) }]}
        columns={[
          { key: 'name', label: 'Name', render: (u) => u.name, csv: (u) => u.name },
          { key: 'login', label: 'Signs in with', render: (u) => [u.email, u.mobile].filter(Boolean).join(' · '), csv: (u) => u.email || u.mobile },
          { key: 'dep', label: 'Department', render: (u) => u.department, csv: (u) => u.department },
          { key: 'roles', label: 'Roles and access', render: (u) => u.roles.map((g) => <div key={g.role_id}>{g.role_name} <span className="hint">· {scopeText(g)}{g.category_ids.length ? ' · ' + g.category_ids.map((c) => l?.categories.find((x) => x.id === c)?.name).join(', ') : ''}</span></div>),
            csv: (u) => u.roles.map((g) => g.role_name).join(' + ') },
          { key: 'pwd', label: 'Password', render: (u) => u.has_password ? 'Set' : <Pill tone="warn">Code only</Pill> },
          { key: 'status', label: 'Status', render: (u) => <ActiveToggle kind="user" id={u.id} name={u.name} active={u.active} onDone={reload} canEdit={u.can_edit && u.id !== me?.id} />, csv: (u) => u.active ? 'Active' : 'Inactive' },
          { key: 'edit', label: '', render: (u) => u.can_edit ? <button className="link" onClick={() => setEdit(u)}>Edit</button> : null },
        ]} csvName="users" />
      {edit && <UserModal u={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onDone={() => { setEdit(null); reload(); refreshLookups() }} />}
      {upload && <UploadModal kind="users" title="Bulk upload or edit users" onClose={() => setUpload(false)} onDone={() => { reload(); refreshLookups() }} />}
    </>
  )
}

function UserModal({ u, onClose, onDone }: { u: User | null; onClose: () => void; onDone: () => void }) {
  const l = useLookups()
  const { me } = useSession()
  const admin = !!me?.is_admin
  // Someone whose role adds users gives only the roles it allows, and property access inside their own unless their
  // role allows any property.
  const mine = me?.user_roles || []
  const roleOpts = (l?.roles || []).filter((r) => r.active && (admin || mine.includes(r.id) || u?.roles.some((g) => g.role_id === r.id)))
  const myAll = admin || !!me?.user_scope_any || !!me?.roles.some((g) => g.scope === 'all')
  const myClusters = me?.roles.filter((g) => g.scope === 'clusters').flatMap((g) => g.cluster_ids) || []
  const myProps = me?.roles.filter((g) => g.scope === 'properties').flatMap((g) => g.property_ids) || []
  const clusterOpts = (l?.clusters || []).filter((c) => myAll || myClusters.includes(c.id))
  const propOpts = (l?.properties || []).filter((p) => p.active && (myAll || myClusters.includes(p.cluster_id) || myProps.includes(p.id)))
  const firstRole = admin ? l?.roles.find((r) => r.code === 'staff')?.id : roleOpts[0]?.id
  const [f, setF] = useState({ name: u?.name || '', email: u?.email || '', mobile: u?.mobile || '', department: u?.department || '', password: '',
    roles: u?.roles.map((g) => ({ ...g })) || [{ role_id: firstRole || 0, scope: 'properties', cluster_ids: [], property_ids: [], category_ids: [] } as RoleGrant] })
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
      <p className="hint">Each role has its own property access. To give a different role at another property, add another role here, or use Access by property.</p>
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
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => u ? put('/users/' + u.id, f) : post('/users', f), () => { onDone(); return 'User saved' })}>Save user</button></div>
    </Modal>
  )
}
