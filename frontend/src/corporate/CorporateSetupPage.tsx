// Approvers for each department of the corporate office, and the expense heads (each mapped to a Zoho account, and
// covering sub-categories: a corporate line's item fills its head from these).
import { useActiveFilter } from '../ui/ActiveFilter'
import { useEffect, useState } from 'react'
import { get, post, put } from '../api/client'
import { useLookups } from '../api/lookups'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { ActiveToggle } from '../ui/ActiveToggle'
import { Errors } from '../ui/Errors'
import { Checks, Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { Tabs } from '../ui/Tabs'
import { useAction } from '../ui/useAction'
import { useToast } from '../ui/Toast'
import { useScreen } from '../screenaccess/screen'
import { UploadModal } from '../bulkupload/UploadModal'

interface Dept { id: number; name: string; l1_user_id: number; l1: string; l2_user_id: number; l2: string; backup_user_id: number | null; backup: string; active: boolean; open_requests: number }
interface Head { id: number; name: string; active: boolean; used: number; fin_status: string; sub_category_ids: number[]; sub_categories: string }

export function CorporateSetupPage() {
  const { hasRight, me, hasStep } = useSession()
  const sc = useScreen('corporate_setup')
  const [tab, setTab] = useState<'mx' | 'eh' | 'ru'>('mx')
  const deps = useApi<Dept[]>('/corporate/departments')
  const heads = useApi<Head[]>('/corporate/expense-heads')
  const st = useActiveFilter()
  const maps = useApi<{ expense_heads: { ref: string; account_id: string; account_active: boolean }[] }>('/zoho/mappings')
  const lists = useApi<{ accounts: { id: string; name: string; code: string; type: string; active: boolean }[] }>('/zoho/lists')
  const [edit, setEdit] = useState<Dept | 'new' | null>(null)
  const [addHead, setAddHead] = useState<Head | 'new' | null>(null)
  const [upload, setUpload] = useState(false)
  const [l1Only, setL1Only] = useState<number | null>(null)
  const toast = useToast()
  useEffect(() => { get('/settings').then((s: any) => setL1Only(s.corp_l1_only_below)) }, [])
  const ed = sc.edit && hasRight('masters')
  const canMap = me?.is_admin || (sc.edit && hasStep('finance'))
  return (
    <>
      <div className="head"><div><h1>Corporate Setup</h1><p className="sub">Approvers for each department and the expense heads used instead of SKUs. A change applies to new requests; open requests keep the approvers they started with.</p></div>
        <div className="acts">{ed && hasRight('upload') && tab !== 'ru' && <button className="btn ghost" onClick={() => setUpload(true)}>Bulk upload / edit</button>}{ed && tab === 'mx' && <button className="btn" onClick={() => setEdit('new')}>+ Add department</button>}{ed && tab === 'eh' && <button className="btn" onClick={() => setAddHead('new')}>+ Add expense head</button>}</div></div>
      <Tabs value={tab} onChange={setTab} items={[['mx', 'Approval matrix'], ['eh', 'Expense heads'], ['ru', 'Rules and Zoho']]} />
      {tab !== 'ru' && <div className="tools">{st.control}</div>}
      {tab === 'mx' && <>
        <p className="note">Every corporate purchase and bill goes to the department's <b>L1</b> and then its <b>L2</b>. If the person raising it is the L1, it goes straight to the L2. The backup can approve at either step when the L1 or L2 is away, but never their own request.</p>
        <section className="card flush"><div className="scroll"><table><thead><tr><th>Department</th><th>L1</th><th>L2</th><th>Backup</th><th className="r">Open requests</th><th>Status</th><th></th></tr></thead>
          <tbody>{(deps.data || []).filter((d) => st.passes(d.active)).map((d) => <tr key={d.id} className={d.active ? '' : 'dim'}><td className="id">{d.name}</td><td>{d.l1}</td><td>{d.l2}</td><td>{d.backup || 'None'}</td><td className="r">{d.open_requests}</td>
            <td><ActiveToggle kind="department" id={d.id} name={d.name} active={d.active} onDone={deps.reload} /></td><td className="r">{ed && <button className="link" onClick={() => setEdit(d)}>Edit</button>}</td></tr>)}</tbody></table></div></section></>}
      {tab === 'eh' && <>
        <p className="note">Each corporate line, or each bill, picks an expense head. The head decides the Zoho account the cost is posted to; a head without an account holds the bill at the Zoho push. Give each head the <b>sub-categories</b> it covers: picking an item on a corporate purchase then fills its head by itself (it can still be changed). A sub-category belongs to one head only.</p>
        <section className="card flush"><div className="scroll"><table><thead><tr><th>Expense head</th><th>Sub-categories (fills the head)</th><th>Zoho account</th><th className="r">Used on</th><th>Status</th><th></th></tr></thead>
          <tbody>{(heads.data || []).filter((h) => st.passes(h.active)).map((h) => { const m = maps.data?.expense_heads.find((x) => x.ref === String(h.id)); return <tr key={h.id} className={h.active ? '' : 'dim'}><td className="id">{h.name}{h.fin_status === 'pending' && <> <Pill tone="warn">Waits for Finance</Pill></>}</td>
            <td>{h.sub_categories || <span className="hint">None: picked by hand</span>}</td>
            <td><select disabled={!canMap} value={m?.account_id || ''} onChange={async (e) => { try { await put('/zoho/mappings', { kind: 'head', key: String(h.id), value: e.target.value }); toast('Mapping saved'); maps.reload() } catch (err: any) { toast(err.message, true) } }}>
              <option value="">Not mapped</option>{(lists.data?.accounts || []).filter((x) => x.active && ['expense', 'other_expense', 'cost_of_goods_sold', 'fixed_asset', 'other_asset'].includes(x.type)).map((x) => <option key={x.id} value={x.id}>{x.code ? x.code + ' · ' : ''}{x.name}</option>)}</select>
              {!m?.account_id && <> <Pill tone="bad">Bills held</Pill></>}</td>
            <td className="r">{h.used}</td><td><ActiveToggle kind="expense_head" id={h.id} name={h.name} active={h.active} onDone={heads.reload} /></td>
            <td className="r">{ed && <button className="link" onClick={() => setAddHead(h)}>Sub-categories</button>}</td></tr> })}</tbody></table></div></section></>}
      {tab === 'ru' && <div className="grid2">
        <section className="card"><h2>Rules</h2>
          <label className="field"><span>L1 alone is enough below this amount (₹)</span>
            <input type="number" min="0" step="1000" disabled={!me?.is_admin} defaultValue={l1Only ?? 0} key={String(l1Only)}
              onBlur={async (e) => { const v = +e.target.value; if (v === l1Only) return; try { await put('/settings', { corp_l1_only_below: v }); setL1Only(v); toast('Setting saved') } catch (err: any) { toast(err.message, true) } }} />
            <span className="hint">0 means L1 and L2 always approve. If the L1 raised it, the L2 still approves.</span></label>
          <p className="hint">No budget is checked for corporate requests. Spend shows by department and expense head in Reports.</p></section>
        <section className="card"><h2>Zoho</h2><p>The corporate office is a property with stage "Corporate office". Its reporting tag (set under Settings › Zoho Books › Properties) goes on every corporate bill; the department is written in the line description.</p></section>
      </div>}
      {edit && <DeptModal d={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onDone={() => { setEdit(null); deps.reload() }} />}
      {addHead && <HeadModal h={addHead === 'new' ? null : addHead} onClose={() => setAddHead(null)} onDone={() => { setAddHead(null); heads.reload() }} />}
      {upload && <UploadModal kind="corp_departments" title="Bulk upload or edit departments and expense heads" onClose={() => setUpload(false)} onDone={() => { deps.reload(); heads.reload() }} />}
    </>
  )
}

function DeptModal({ d, onClose, onDone }: { d: Dept | null; onClose: () => void; onDone: () => void }) {
  const l = useLookups()
  const people = (l?.people || []).filter((p) => p.active)
  const [f, setF] = useState({ name: d?.name || '', l1_user_id: d?.l1_user_id || 0, l2_user_id: d?.l2_user_id || 0, backup_user_id: d?.backup_user_id || 0 })
  const a = useAction()
  const pick = (k: 'l1_user_id' | 'l2_user_id' | 'backup_user_id', none: string) => <select value={f[k]} onChange={(e) => setF({ ...f, [k]: +e.target.value })}><option value={0}>{none}</option>{people.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select>
  return (
    <Modal title={d ? 'Edit ' + d.name : 'Add department'} onClose={onClose}>
      <div className="grid2">
        <Field label="Department"><input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
        <Field label="L1 approver (department head)">{pick('l1_user_id', 'Choose')}</Field>
        <Field label="L2 approver">{pick('l2_user_id', 'Choose')}</Field>
        <Field label="Backup approver" hint="Can approve at either step when the L1 or L2 is away.">{pick('backup_user_id', 'None')}</Field>
      </div>
      <p className="hint">People come from Users &amp; Rights. Give them the Department Head or Corporate Approver role so they can approve.</p>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => d ? put('/corporate/departments/' + d.id, f) : post('/corporate/departments', f), () => { onDone(); return f.name + ' saved' })}>Save</button></div>
    </Modal>
  )
}

/** Adds an expense head, or sets the sub-categories an existing one covers. */
function HeadModal({ h, onClose, onDone }: { h: Head | null; onClose: () => void; onDone: () => void }) {
  const l = useLookups()
  const [name, setName] = useState(h?.name || '')
  const [subs, setSubs] = useState<number[]>(h?.sub_category_ids || [])
  const a = useAction()
  const options = (l?.subCategories || []).filter((x) => x.active || subs.includes(x.id)).map((x) => [x.id, `${x.category} › ${x.name}`] as [number, string])
  return (
    <Modal title={h ? 'Sub-categories of ' + h.name : 'Add expense head'} onClose={onClose}>
      {!h && <Field label="Expense head"><input value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Legal and Compliance Fees" /></Field>}
      <Field label="Sub-categories it covers" hint="Items of these sub-categories fill this head on a corporate purchase. Leave empty to pick the head by hand.">
        <Checks value={subs} onChange={setSubs} options={options} placeholder="Choose sub-categories" label="Sub-categories" /></Field>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => h ? put(`/corporate/expense-heads/${h.id}/sub-categories`, { sub_category_ids: subs }) : post('/corporate/expense-heads', { name, sub_category_ids: subs }),
          (r: any) => { onDone(); return r?.message || name + ' added' })}>Save</button></div>
    </Modal>
  )
}
