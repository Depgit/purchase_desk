// The Roles tab: roles listed by group, and an editor for the chosen one. A role carries its step in the flow, its
// action rights, a level on every screen (no access, view, edit), the reports it sees, whether it may add users (which
// roles, at which properties) and, set by the System Admin only, whether it may manage roles. Someone allowed to
// manage roles never gives a role more than their own; the server refuses it.
import { useActiveFilter } from '../ui/ActiveFilter'
import { useEffect, useMemo, useState } from 'react'
import { post, put } from '../api/client'
import { refreshLookups } from '../api/lookups'
import type { Level, Role } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { ActiveToggle } from '../ui/ActiveToggle'
import { Errors } from '../ui/Errors'
import { Checks, Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { Seg } from '../ui/Tabs'
import { useAction } from '../ui/useAction'

interface Catalogue { screens: { key: string; label: string; group: string; view_only: boolean }[]; reports: { key: string; title: string; group: string }[]; rights: string[] }
interface Group { id: number; name: string; sort: number; roles: number }

const RIGHTS: Record<string, string> = { raise: 'Raise request or bill', approve: 'Approve', issue_po: 'Issue PO', receive: 'Receive or dispatch goods', match: 'Match invoice',
  pay: 'Payment and Zoho', masters: 'Edit masters', upload: 'Upload opening and bulk data' }
const STEPS: [string, string][] = [['cc', 'Cost Controller: approves requests'], ['l2', 'Cluster Manager: approves event requests and bills'], ['preop', 'Pre-opening Team: approves warehouse and pre-opening orders'],
  ['purchase', 'Purchase Team: issues POs'], ['store', 'Receiving / Store: receives goods'], ['wh', 'Warehouse Manager: dispatches and receives'], ['ap', 'Accounts Payable: matches invoices'],
  ['finance', 'Finance: approves payment'], ['staff', 'Property staff: raises requests'], ['l1', 'Property Manager: raises requests'], ['', 'No step in the flow: view and reports only']]
const stepName = (s: string) => s === 'admin' ? 'System Admin' : (STEPS.find(([k]) => k === s)?.[1].split(':')[0] || s || 'No step')
const LEVELS: [Level, string][] = [['none', 'No access'], ['view', 'View'], ['edit', 'Edit']]

export function RolesTab() {
  const { me } = useSession()
  const roles = useApi<Role[]>('/roles')
  const groups = useApi<Group[]>('/role-groups')
  const cat = useApi<Catalogue>('/roles/catalogue')
  const [pick, setPick] = useState<number | null>(null)
  const [adding, setAdding] = useState(false)
  const [group, setGroup] = useState<Group | 'new' | null>(null)
  const a = useAction()
  const manage = !!me?.manages_roles
  const st = useActiveFilter() // custom roles can be inactive
  const list = (roles.data || []).filter((r) => st.passes(r.active))
  const chosen = list.find((r) => r.id === pick) || list[0]
  const reload = () => { roles.reload(); groups.reload(); refreshLookups() }
  const byGroup = (groups.data || []).map((g) => ({ g, roles: list.filter((r) => r.group_id === g.id) }))
  const loose = list.filter((r) => !r.group_id || !(groups.data || []).some((g) => g.id === r.group_id))
  const copy = (r: Role) => a.run(() => post<{ id: number; message: string }>('/roles/' + r.id + '/copy', {}), (x) => { reload(); setPick(x.id); return x.message })
  return (
    <>
      <div className="tools">{st.control}<span style={{ flex: 1 }} />
        {manage && <><button className="btn ghost" onClick={() => setGroup('new')}>+ Add group</button>
        <button className="btn" onClick={() => setAdding(true)}>+ Add role</button></>}</div>
      <Errors list={a.errors} />
      <div className="rolesplit">
        <section className="card rolelist">
          {[...byGroup, ...(loose.length ? [{ g: null, roles: loose }] : [])].map(({ g, roles: rs }) => (
            <div key={g?.id || 'none'}>
              <div className="rgroup"><span>{g?.name || 'No group'}</span>{g && manage && <button className="link" onClick={() => setGroup(g)}>Rename</button>}</div>
              {rs.length === 0 && <p className="hint">No roles in this group yet.</p>}
              {rs.map((r) => (
                <button key={r.id} className={'rrow' + (chosen?.id === r.id ? ' on' : '') + (r.active ? '' : ' dim')} onClick={() => setPick(r.id)}>
                  <span><b>{r.name}</b>{r.is_custom && <> <Pill>Custom</Pill></>}{!r.active && <> <Pill tone="warn">Inactive</Pill></>}<br /><span className="hint">{stepName(r.step)}</span></span>
                  <span className="hint">{r.users} {r.users === 1 ? 'user' : 'users'}</span>
                </button>))}
            </div>))}
        </section>
        {chosen && cat.data && <RoleEditor key={chosen.id + ':' + chosen.name} r={chosen} cat={cat.data} groups={groups.data || []} roles={list} onCopy={copy} onSaved={reload} />}
      </div>
      {adding && <NewRole groups={groups.data || []} onClose={() => setAdding(false)} onDone={(id) => { setAdding(false); reload(); setPick(id) }} />}
      {group && <GroupModal g={group === 'new' ? null : group} onClose={() => setGroup(null)} onDone={() => { setGroup(null); reload() }} />}
    </>
  )
}

function RoleEditor({ r, cat, groups, roles, onCopy, onSaved }: { r: Role; cat: Catalogue; groups: Group[]; roles: Role[]; onCopy: (r: Role) => void; onSaved: () => void }) {
  const { me } = useSession()
  const admin = !!me?.is_admin
  const fixed = r.step === 'admin'
  const canEdit = !!me?.manages_roles && !fixed
  const start = () => ({ name: r.name, step: r.step, rights: [...r.rights], group_id: r.group_id, screens: { ...r.screens }, all: r.reports === null, reports: r.reports || [],
    can_users: r.can_users, user_role_ids: [...r.user_role_ids], user_scope: r.user_scope, can_roles: r.can_roles })
  const [f, setF] = useState(start)
  const [touched, setTouched] = useState<string[]>([])
  useEffect(() => { setF(start()); setTouched([]) }, [r]) // eslint-disable-line react-hooks/exhaustive-deps
  const a = useAction()
  const screenGroups = useMemo(() => [...new Set(cat.screens.map((s) => s.group))], [cat])
  const reportGroups = useMemo(() => [...new Set(cat.reports.map((x) => x.group))], [cat])
  const setLevel = (k: string, v: Level) => { setF({ ...f, screens: { ...f.screens, [k]: v } }); setTouched(touched.includes(k) ? touched : [...touched, k]) }
  const setAll = (v: Level) => { const s = { ...f.screens }; cat.screens.forEach((x) => { s[x.key] = v === 'edit' && x.view_only ? 'view' : v }); setF({ ...f, screens: s }); setTouched(cat.screens.map((x) => x.key)) }
  const giveable = roles.filter((x) => x.step !== 'admin' && x.active)
  const save = () => {
    const body: Record<string, unknown> = { name: f.name, group_id: f.group_id, rights: f.rights, can_users: f.can_users, user_role_ids: f.user_role_ids, user_scope: f.user_scope }
    if (r.is_custom) body.step = f.step
    // Only the screens someone changed are stored; the rest keep following the role's rights.
    body.screens = Object.fromEntries(touched.map((k) => [k, f.screens[k]]))
    if (f.all) body.all_reports = true
    else body.reports = f.reports
    if (admin) body.can_roles = f.can_roles
    return a.run(() => put('/roles/' + r.id, body), () => { setTouched([]); onSaved(); return f.name + ' saved' })
  }
  return (
    <section className="card roleed">
      <div className="ahead">
        <div><h2>{r.name}</h2><p className="sub">{stepName(r.step)} · {r.users} {r.users === 1 ? 'user' : 'users'}{r.is_custom ? ' · custom role' : ''}</p></div>
        <div className="acts">
          {!fixed && me?.manages_roles && <button className="btn ghost" onClick={() => onCopy(r)}>Copy role</button>}
          {r.is_custom && <ActiveToggle kind="role" id={r.id} name={r.name} active={r.active} onDone={onSaved} canEdit={!!me?.manages_roles} />}
        </div>
      </div>
      {fixed && <p className="note">The System Admin role always has every right and edit on every screen. It cannot be changed.</p>}
      {!fixed && !me?.manages_roles && <p className="note">You can see this role. Only the System Admin, or a role allowed to manage roles, can change it.</p>}
      <fieldset disabled={!canEdit} className="plain">
        <div className="grid2">
          <Field label="Role name"><input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
          <Field label="Group"><select value={f.group_id || ''} onChange={(e) => setF({ ...f, group_id: e.target.value ? +e.target.value : null })}>
            {groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}</select></Field>
          {r.is_custom && <Field label="Works in the flow as" hint="Decides which documents wait for this role."><select value={f.step} onChange={(e) => setF({ ...f, step: e.target.value })}>
            {STEPS.map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></Field>}
        </div>

        <h3>Action rights</h3>
        <p className="hint">What the role may do on the screens it can edit. Edit on a screen without the matching right still cannot, for example, approve.</p>
        <Checks label="Rights" placeholder="No rights" value={f.rights} onChange={(v) => setF({ ...f, rights: v })} options={cat.rights.map((k) => [k, RIGHTS[k] || k] as [string, string])} />

        <div className="ahead"><h3>Screens</h3>{canEdit && <span className="acts"><span className="hint">Set all to</span>
          {LEVELS.map(([k, l]) => <button key={k} className="link" onClick={() => setAll(k)}>{l}</button>)}</span>}</div>
        <p className="hint">No access hides the screen. View opens it without its buttons. Edit allows changes, within the role's rights and properties.</p>
        {screenGroups.map((g) => (
          <div key={g} className="lvls"><div className="lvlgroup">{g}</div>
            {cat.screens.filter((s) => s.group === g).map((s) => (
              <div key={s.key} className={'lvl' + (touched.includes(s.key) ? ' changed' : '')}><span>{s.label}{s.view_only && <span className="hint"> · changes stay with the System Admin</span>}</span>
                {canEdit ? <Seg label={s.label} value={f.screens[s.key] || 'none'} onChange={(v) => setLevel(s.key, v)} items={s.view_only ? LEVELS.slice(0, 2) : LEVELS} />
                  : <Pill tone={f.screens[s.key] === 'edit' ? 'ok' : f.screens[s.key] === 'view' ? '' : 'off'}>{LEVELS.find(([k]) => k === (f.screens[s.key] || 'none'))?.[1]}</Pill>}
              </div>))}
          </div>))}

        {cat.reports.length > 0 && <>
          <h3>Reports</h3>
          <label className="ck"><input type="checkbox" checked={f.all} onChange={(e) => setF({ ...f, all: e.target.checked, reports: e.target.checked ? [] : cat.reports.map((x) => x.key) })} />Every report, including ones added later</label>
          {!f.all && reportGroups.map((g) => (
            <div key={g} className="field"><span>{g}</span>
              <Checks label="Reports" placeholder="Choose reports" value={f.reports} onChange={(v) => setF({ ...f, reports: v })} options={cat.reports.filter((x) => x.group === g).map((x) => [x.key, x.title] as [string, string])} /></div>))}
        </>}

        <h3>Special rights</h3>
        <label className="ck"><input type="checkbox" checked={f.can_users} onChange={(e) => setF({ ...f, can_users: e.target.checked })} />Can add users</label>
        {f.can_users && <div className="xpbox">
          <div className="field"><span>Roles they may give</span>
            <Checks label="Roles" placeholder="Choose roles" value={f.user_role_ids} onChange={(v) => setF({ ...f, user_role_ids: v })} options={giveable.map((x) => [x.id, x.name] as [number, string])} /></div>
          <div className="field"><span>Properties they may give access to</span>
            <Seg label="User scope" value={f.user_scope} onChange={(v) => setF({ ...f, user_scope: v })} items={[['own', 'Only their own properties'], ['any', 'Any property']]} /></div>
          <p className="hint">They see and edit only the users they added.</p>
        </div>}
        <label className="ck"><input type="checkbox" checked={f.can_roles} disabled={!admin} onChange={(e) => setF({ ...f, can_roles: e.target.checked })} />
          Can manage roles{!admin && ' (set by the System Admin)'}</label>
        {f.can_roles && <p className="hint">They can create, copy and change roles, but never give a role more than their own.</p>}
      </fieldset>
      <Errors list={a.errors} />
      {canEdit && <div className="btns"><button className="btn ghost" onClick={() => { setF(start()); setTouched([]) }}>Undo changes</button>
        <button className="btn" disabled={a.busy} onClick={save}>Save role</button></div>}
    </section>
  )
}

function NewRole({ groups, onClose, onDone }: { groups: Group[]; onClose: () => void; onDone: (id: number) => void }) {
  const [f, setF] = useState({ name: '', step: 'cc', rights: ['approve'], group_id: groups[0]?.id || null as number | null })
  const a = useAction()
  return (
    <Modal title="Add role" onClose={onClose}>
      <p className="sub">Name it, say where it sits in the flow and tick its rights. Its screens start from its rights; set them next.</p>
      <Field label="Role name"><input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="e.g. Regional Cost Controller" /></Field>
      <Field label="Group"><select value={f.group_id || ''} onChange={(e) => setF({ ...f, group_id: +e.target.value })}>{groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}</select></Field>
      <Field label="Works in the flow as" hint="Decides which documents wait for this role."><select value={f.step} onChange={(e) => setF({ ...f, step: e.target.value })}>{STEPS.map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></Field>
      <div className="field"><span>Rights</span><Checks label="Rights" placeholder="No rights" value={f.rights} onChange={(v) => setF({ ...f, rights: v })} options={Object.entries(RIGHTS)} /></div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post<{ id: number; message: string }>('/roles', f), (x) => { onDone(x.id); return x.message })}>Add role</button></div>
    </Modal>
  )
}

function GroupModal({ g, onClose, onDone }: { g: Group | null; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState(g?.name || '')
  const a = useAction()
  return (
    <Modal title={g ? 'Rename group' : 'Add role group'} onClose={onClose}>
      <p className="sub">Groups only sort the role list, for example Head office, Property or Warehouse.</p>
      <Field label="Group name"><input value={name} onChange={(e) => setName(e.target.value)} /></Field>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => g ? put('/role-groups/' + g.id, { name }) : post('/role-groups', { name }), () => { onDone(); return 'Group saved' })}>Save</button></div>
    </Modal>
  )
}
