// Zoho Books: connection, the hourly sync, and the four mappings a bill needs before it can be pushed:
// property → reporting tag, sub-category / bill type → account, GST rate → tax, vendor → Zoho vendor.
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { post, put } from '../api/client'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { UploadModal } from '../bulkupload/UploadModal'
import { dateTime, inr } from '../format/format'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { Tabs } from '../ui/Tabs'
import { useAction } from '../ui/useAction'
import { SearchSelect } from '../ui/SearchSelect'
import { useToast } from '../ui/Toast'

interface Opt { id: string; name: string; code: string; type: string; pct: number; specification: string; extra: string; active: boolean; tag_id: string }
interface Lists { accounts: Opt[]; taxes: Opt[]; tags: Opt[]; tag_options: Opt[]; locations: Opt[]; contacts: Opt[] }
interface Maps {
  properties: { property_id: number; code: string; name: string; cluster: string; kind: string; state_code: string; tag_option_id: string; tag_option_active: boolean; location_id: string; shared_with: number }[]
  sub_categories: { ref: string; name: string; group: string; account_id: string; account_active: boolean }[]
  bill_types: { ref: string; name: string; group: string; account_id: string; account_active: boolean }[]
  special: { ref: string; name: string; account_id: string; account_active: boolean }[]
  taxes: Record<string, string>
  vendors: { vendor_id: number; code: string; name: string; vendor_type: string; gstin: string; state_code: string; bank_verified: boolean; contact_id: string; contact_name: string; contact_gst: string; contact_active: boolean; how: string; suggest_id: string; suggest_name: string }[]
  can_edit: boolean
}
type Tab = 'conn' | 'prop' | 'acc' | 'tax' | 'ven' | 'queue' | 'journal' | 'map'
const EXP = ['expense', 'cost_of_goods_sold', 'other_expense']

export function ZohoBooksPage() {
  const [tab, setTab] = useState<Tab>('conn')
  const conn = useApi<any>('/zoho/connection')
  const lists = useApi<Lists>('/zoho/lists')
  const maps = useApi<Maps>('/zoho/mappings')
  const { hasStep } = useSession()
  const a = useAction()
  const reloadAll = () => { conn.reload(); lists.reload(); maps.reload() }
  const m = maps.data
  const counts = m ? {
    prop: [m.properties.filter((p) => p.tag_option_id && p.tag_option_active).length, m.properties.length],
    acc: [m.sub_categories.filter((s) => s.account_id && s.account_active).length, m.sub_categories.length],
    bt: [m.bill_types.filter((s) => s.account_id && s.account_active).length, m.bill_types.length],
    tax: [Object.keys(m.taxes).length, 8],
    ven: [m.vendors.filter((v) => v.contact_id).length, m.vendors.length],
  } : null
  const tile = (k: keyof NonNullable<typeof counts>, label: string, t: Tab) => counts && <button className={'tile' + (tab === t ? ' on' : '')} onClick={() => setTab(t)}><span>{label}</span>
    <b style={counts[k][0] < counts[k][1] ? { color: 'var(--bad)' } : undefined}>{counts[k][0]} of {counts[k][1]}</b></button>
  return (
    <>
      <div className="head"><div><h1>Zoho Books</h1><p className="sub">Everything a bill needs before it can go to Zoho. A bill with a gap is held back and says what is missing. Purchase Desk only creates records in Zoho; it never edits or deletes them.</p></div>
        <div className="acts">{hasStep('finance') && <button className="btn ghost" disabled={a.busy} onClick={() => a.run(() => post('/zoho/sync'), (r: any) => { reloadAll(); return r.message })}>Sync now</button>}</div></div>
      <Errors list={a.errors} />
      <div className="tiles">{tile('prop', 'Properties with a reporting tag', 'prop')}{tile('acc', 'Sub-categories with an account', 'acc')}{tile('bt', 'Bill types with an account', 'acc')}{tile('tax', 'GST rates with a tax code', 'tax')}{tile('ven', 'Vendors linked', 'ven')}</div>
      <Tabs value={tab} onChange={setTab} items={[['conn', 'Connection'], ['prop', 'Properties'], ['acc', 'Accounts'], ['tax', 'Taxes'], ['ven', 'Vendors'], ['queue', 'Push queue'], ['journal', 'Warehouse journal'], ['map', 'Field map']]} />
      {tab === 'conn' && <Connection c={conn.data} lists={lists.data} onDone={reloadAll} />}
      {tab === 'prop' && m && lists.data && <Properties m={m} lists={lists.data} onDone={reloadAll} />}
      {tab === 'acc' && m && lists.data && <Accounts m={m} lists={lists.data} onDone={reloadAll} />}
      {tab === 'tax' && m && lists.data && <Taxes m={m} lists={lists.data} onDone={reloadAll} />}
      {tab === 'ven' && m && lists.data && <Vendors m={m} lists={lists.data} onDone={reloadAll} />}
      {tab === 'queue' && <Queue />}
      {tab === 'journal' && <Journals />}
      {tab === 'map' && <FieldMap />}
    </>
  )
}

function useMap(onDone: () => void) {
  const toast = useToast()
  return async (kind: string, key: string, value: string) => {
    try { await put('/zoho/mappings', { kind, key, value }); toast('Mapping saved'); onDone() } catch (e: any) { toast(e.message, true) }
  }
}

function Connection({ c, lists, onDone }: { c: any; lists: Lists | null; onDone: () => void }) {
  const { me } = useSession()
  const [edit, setEdit] = useState(false)
  const a = useAction()
  if (!c) return <p className="sub">Loading…</p>
  const admin = !!me?.is_admin
  return (
    <div className="grid2">
      <section className="card"><h2>Connection {c.connected ? <Pill tone="ok">Connected</Pill> : <Pill tone="bad">Not connected</Pill>}</h2>
        <dl className="kv">
          <dt>Organisation</dt><dd>{c.org_name || '—'} · {c.org_id}</dd><dt>Plan</dt><dd>{c.plan || '—'}</dd>
          <dt>API address</dt><dd>{c.api_url}</dd><dt>Sign-in address</dt><dd>{c.accounts_url}</dd>
          <dt>Client ID</dt><dd>{c.client_id_masked}</dd><dt>Client secret</dt><dd>{c.has_secret ? '•••••••• saved' : 'Not set'}</dd><dt>Refresh token</dt><dd>{c.has_refresh_token ? '•••••••• saved' : 'Not set'}</dd>
          <dt>Last sync</dt><dd>{dateTime(c.last_sync_at)} (every hour)</dd><dt>Payment read-back</dt><dd>{dateTime(c.last_payment_read_at)} (every two hours)</dd>
          <dt>Last test</dt><dd>{dateTime(c.tested_at)}</dd><dt>Bill unique key</dt><dd>{c.bill_key_field}</dd>
          <dt>Push</dt><dd>{c.push_enabled ? <Pill tone="ok">Switched on</Pill> : <Pill tone="warn">Switched off on this server</Pill>}</dd>
        </dl>
        {c.last_error && <p className="note bad">{c.last_error}</p>}
        {!c.push_enabled && <p className="note warn">Pushing is off (ZOHO_PUSH_ENABLED=false). Sync, mappings and previews work. Switch it on after testing in a Zoho test organisation.</p>}
        <label className="field"><span>Reporting tag for new options (optional)</span>
          <select value={c.property_tag_id} disabled={!admin} onChange={(e) => a.run(() => put('/zoho/connection', { property_tag_id: e.target.value }), () => { onDone(); return 'Saved' })}>
            <option value="">Not chosen</option>{lists?.tags.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}</select>
          <span className="hint">{lists?.tags.length ? 'Properties can map to an option of any reporting tag on the Properties tab. This tag is used only when a missing option is created from Purchase Desk.' : 'No reporting tag exists in this Zoho organisation yet. Create one in Zoho (Settings › Reporting Tags), then Sync now.'}</span></label>
        <Errors list={a.errors} />
        {admin && <div className="btns">
          <button className="btn ghost" onClick={() => setEdit(true)}>Change credentials</button>
          <button className="btn ghost" disabled={a.busy} onClick={() => a.run(() => post('/zoho/test'), (r: any) => { onDone(); return r.message })}>Test connection</button>
          <button className={'btn ' + (c.connected ? 'no' : '')} disabled={a.busy} onClick={() => a.run(() => post('/zoho/connected', { connected: !c.connected }), () => { onDone(); return c.connected ? 'Disconnected' : 'Connected' })}>{c.connected ? 'Disconnect' : 'Connect'}</button></div>}
      </section>
      <section className="card"><h2>What is read from Zoho</h2>
        <div className="scroll"><table><thead><tr><th>List</th><th className="r">Records</th><th>From here to Zoho</th></tr></thead><tbody>
          <tr><td>Chart of accounts</td><td className="r">{c.counts?.Accounts}</td><td>Create account (Accounts tab)</td></tr>
          <tr><td>Taxes and tax groups</td><td className="r">{c.counts?.Taxes}</td><td>Not created from here</td></tr>
          <tr><td>Property tag options</td><td className="r">{c.counts?.Options}</td><td>Create option for a property</td></tr>
          <tr><td>Locations</td><td className="r">{c.counts?.Locations}</td><td>Optional; not created from here</td></tr>
          <tr><td>Vendor contacts</td><td className="r">{c.counts?.Contacts}</td><td>Create vendor (Vendors tab)</td></tr>
          <tr><td>Bills pushed</td><td className="r">{c.counts?.Bills}</td><td>Payment status read back</td></tr>
        </tbody></table></div></section>
      {edit && <CredModal c={c} onClose={() => setEdit(false)} onDone={() => { setEdit(false); onDone() }} />}
    </div>
  )
}

function CredModal({ c, onClose, onDone }: { c: any; onClose: () => void; onDone: () => void }) {
  const [f, setF] = useState({ accounts_url: c.accounts_url, api_url: c.api_url, org_id: c.org_id, client_id: '', client_secret: '', refresh_token: '' })
  const a = useAction()
  return (
    <Modal title="Zoho Books connection" onClose={onClose} wide>
      <p className="note">Zoho Books uses four values from the Zoho API Console: the organisation ID, a client ID, a client secret and a refresh token. Save runs a test call and refuses values that fail. Secrets are stored encrypted and never shown again.</p>
      <div className="grid2">
        <Field label="Sign-in address"><input value={f.accounts_url} onChange={(e) => setF({ ...f, accounts_url: e.target.value })} /></Field>
        <Field label="API address"><input value={f.api_url} onChange={(e) => setF({ ...f, api_url: e.target.value })} /></Field>
        <Field label="Organisation ID"><input value={f.org_id} onChange={(e) => setF({ ...f, org_id: e.target.value })} /></Field>
        <Field label="Client ID" hint="Leave blank to keep the saved one."><input value={f.client_id} onChange={(e) => setF({ ...f, client_id: e.target.value })} autoComplete="off" /></Field>
        <Field label="Client secret" hint="Leave blank to keep the saved one."><input type="password" value={f.client_secret} onChange={(e) => setF({ ...f, client_secret: e.target.value })} autoComplete="new-password" /></Field>
        <Field label="Refresh token" hint="Leave blank to keep the saved one."><input type="password" value={f.refresh_token} onChange={(e) => setF({ ...f, refresh_token: e.target.value })} autoComplete="new-password" /></Field>
      </div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => put('/zoho/connection', f), (r: any) => { onDone(); return r.message })}>Save and test</button></div>
    </Modal>
  )
}

function Properties({ m, lists, onDone }: { m: Maps; lists: Lists; onDone: () => void }) {
  const save = useMap(onDone)
  const a = useAction()
  const [gap, setGap] = useState(false)
  const [upload, setUpload] = useState(false)
  const conn = useApi<any>('/zoho/connection')
  // A property may map to an option of any reporting tag, for example one tag per region; the bill carries that option's tag.
  const tags = lists.tags.filter((t) => t.active && lists.tag_options.some((o) => o.tag_id === t.id && o.active))
  const canCreate = !!conn.data?.property_tag_id && !!conn.data?.push_enabled
  // With a Zoho location chosen, only the reporting tag of that location's region is offered (Delhi location: Delhi options).
  // Without a location, or when no tag matches the location, every tag is offered. The current option always stays visible.
  const regionTag = (p: { location_id: string }) => lists.locations.find((l) => l.id === p.location_id)?.tag_id || ''
  const tagsFor = (p: { location_id: string; tag_option_id: string }) => {
    const only = regionTag(p)
    const cur = lists.tag_options.find((o) => o.id === p.tag_option_id)?.tag_id
    return only ? tags.filter((t) => t.id === only || t.id === cur) : tags
  }
  const rows = m.properties.filter((p) => !gap || !p.tag_option_id)
  return (
    <>
      <p className="note">Each property is identified by a reporting tag option, from any tag. The option and its tag are sent on the bill and on every line, so expenses report hotel by hotel in Zoho. A property without an option cannot have its bills pushed.</p>
      <div className="tools"><label className="ck"><input type="checkbox" checked={gap} onChange={(e) => setGap(e.target.checked)} />Only properties without a tag</label>
        {m.can_edit && <><button className="btn ghost" onClick={() => a.run(() => post('/zoho/properties/match'), (r: any) => { onDone(); return r.message })}>Match by name</button>
          <button className="btn ghost" onClick={() => setUpload(true)}>Upload mapping</button></>}</div>
      <Errors list={a.errors} />
      <section className="card flush"><div className="scroll"><table><thead><tr><th>Property</th><th>Cluster</th><th>State</th><th>Reporting tag option</th><th>Zoho location (optional)</th></tr></thead>
        <tbody>{rows.map((p) => <tr key={p.property_id}><td className="id">{p.name}<br /><span className="hint">{p.code}</span></td><td>{p.cluster}</td><td>{p.state_code}</td>
          <td><SearchSelect disabled={!m.can_edit} value={p.tag_option_id} emptyLabel="Not mapped" placeholder="Search hotel or region" ariaLabel={'Reporting tag option for ' + p.name}
            items={tagsFor(p).flatMap((t) => lists.tag_options.filter((o) => o.tag_id === t.id && (o.active || o.id === p.tag_option_id)).map((o) => ({ value: o.id, label: o.name, group: t.name })))}
            onChange={(v) => save('property_tag', String(p.property_id), v)} />
            {!p.tag_option_id && <> <Pill tone="bad">Bills held</Pill>{m.can_edit && canCreate && <> <button className="link" onClick={() => a.run(() => post(`/zoho/properties/${p.property_id}/create-option`), (r: any) => { onDone(); return r.message })}>Create in Zoho</button></>}</>}
            {p.shared_with > 0 && <> <Pill tone="warn">Shared with {p.shared_with} other</Pill></>}</td>
          <td><select disabled={!m.can_edit} value={p.location_id} onChange={(e) => save('property_location', String(p.property_id), e.target.value)}><option value="">None</option>{lists.locations.filter((l) => l.active).map((l) => <option key={l.id} value={l.id}>{l.name}{l.extra ? ` (${l.extra})` : ''}</option>)}</select></td></tr>)}</tbody></table></div></section>
      {upload && <UploadModal kind="zoho_tags" title="Upload property to reporting tag mapping" onClose={() => setUpload(false)} onDone={onDone} />}
    </>
  )
}

function AccountSelect({ value, accounts, types, disabled, onChange }: { value: string; accounts: Opt[]; types?: string[]; disabled: boolean; onChange: (v: string) => void }) {
  const list = accounts.filter((x) => x.active && (!types || types.includes(x.type)))
  return <SearchSelect value={value} disabled={disabled} onChange={onChange} emptyLabel="Not mapped" placeholder="Search account name or code"
    items={list.map((x) => ({ value: x.id, label: (x.code ? x.code + ' · ' : '') + x.name, hint: x.type.replace(/_/g, ' ') }))} />
}

function Accounts({ m, lists, onDone }: { m: Maps; lists: Lists; onDone: () => void }) {
  const save = useMap(onDone)
  const [create, setCreate] = useState(false)
  return (
    <>
      <p className="note">Each bill line posts to one Zoho account. Goods use the account of the item's sub-category; utility and AMC bills use the bill type's account. Nothing posts to a guessed account: an unmapped one holds the bill.</p>
      {m.can_edit && <div className="tools"><button className="btn ghost" onClick={() => setCreate(true)}>+ Create account in Zoho</button></div>}
      <div className="cols">
        <section className="card flush"><h2>Sub-categories</h2><div className="scroll"><table><thead><tr><th>Sub-category</th><th>Category</th><th>Zoho account</th></tr></thead>
          <tbody>{m.sub_categories.map((s) => <tr key={s.ref}><td className="id">{s.name}</td><td>{s.group}</td><td><AccountSelect value={s.account_id} accounts={lists.accounts} types={EXP} disabled={!m.can_edit} onChange={(v) => save('subcat', s.ref, v)} />
            {s.account_id && !s.account_active && <Pill tone="bad">Inactive in Zoho</Pill>}</td></tr>)}</tbody></table></div></section>
        <div className="stack">
          <section className="card flush"><h2>Utility and AMC bill types</h2><div className="scroll"><table><thead><tr><th>Bill type</th><th>Zoho account</th></tr></thead>
            <tbody>{m.bill_types.map((s) => <tr key={s.ref}><td className="id">{s.name}</td><td><AccountSelect value={s.account_id} accounts={lists.accounts} types={EXP} disabled={!m.can_edit} onChange={(v) => save('billtype', s.ref, v)} /></td></tr>)}</tbody></table></div></section>
          <section className="card"><h2>Special cases</h2>{m.special.map((s) => <label key={s.ref} className="field"><span>{s.name}</span>
            <AccountSelect value={s.account_id} accounts={lists.accounts} types={s.ref === 'loss' ? EXP : undefined} disabled={!m.can_edit} onChange={(v) => save('special', s.ref, v)} />
            {s.ref === 'warehouse' && <span className="hint">Stock is an asset until issued, so warehouse purchases post to a stock account.</span>}</label>)}</section>
        </div>
      </div>
      {create && <CreateAccount onClose={() => setCreate(false)} onDone={() => { setCreate(false); onDone() }} />}
    </>
  )
}

function CreateAccount({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [f, setF] = useState({ Name: '', Code: '', Type: 'expense' })
  const a = useAction()
  return (
    <Modal title="Create account in Zoho" onClose={onClose}>
      <p className="sub">The account is created in the Zoho chart of accounts and then appears here to be mapped. Use this only when no suitable account exists.</p>
      <div className="grid3">
        <Field label="Account name"><input value={f.Name} onChange={(e) => setF({ ...f, Name: e.target.value })} /></Field>
        <Field label="Account code (optional)"><input value={f.Code} onChange={(e) => setF({ ...f, Code: e.target.value })} /></Field>
        <Field label="Type"><select value={f.Type} onChange={(e) => setF({ ...f, Type: e.target.value })}><option value="expense">Expense</option><option value="cost_of_goods_sold">Cost of goods sold</option><option value="other_expense">Other expense</option><option value="other_current_asset">Other current asset (stock)</option></select></Field>
      </div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post('/zoho/accounts', f), (r: any) => { onDone(); return r.message })}>Create in Zoho</button></div>
    </Modal>
  )
}

function Taxes({ m, lists, onDone }: { m: Maps; lists: Lists; onDone: () => void }) {
  const save = useMap(onDone)
  return (
    <>
      <p className="note">Zoho needs a tax code on every taxable line. <b>Within the state</b> (vendor and property in the same state) uses a CGST + SGST group. <b>From another state</b> uses IGST. An unregistered vendor and a 0% item are sent with no tax.</p>
      <section className="card flush"><div className="scroll"><table><thead><tr><th>GST rate</th><th>Vendor within the state</th><th>Vendor from another state</th></tr></thead>
        <tbody>{[5, 12, 18, 28].map((r) => <tr key={r}><td className="id">{r}%</td>{(['intra', 'inter'] as const).map((k) => <td key={k}>
          <select disabled={!m.can_edit} value={m.taxes[`${r}.00|${k}`] || m.taxes[`${r}|${k}`] || ''} onChange={(e) => save('tax', `${r}|${k}`, e.target.value)}><option value="">Not mapped</option>
            {lists.taxes.filter((t) => t.active && t.pct === r && t.specification === k).map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}</select></td>)}</tr>)}</tbody></table></div></section>
    </>
  )
}

function Vendors({ m, lists, onDone }: { m: Maps; lists: Lists; onDone: () => void }) {
  const a = useAction()
  const [pick, setPick] = useState<Record<number, string>>({})
  const [q, setQ] = useState('')
  const taken = new Set(m.vendors.map((v) => v.contact_id).filter(Boolean))
  const free = lists.contacts.filter((c) => c.active && !taken.has(c.id))
  const rows = m.vendors.filter((v) => !q || (v.name + v.gstin + v.code).toLowerCase().includes(q.toLowerCase()))
  const status = (v: Maps['vendors'][number]) => v.contact_id ? <Pill tone={v.contact_active ? 'ok' : 'bad'}>{({ gstin: 'Linked by GSTIN', name: 'Linked by name', manual: 'Linked by Finance', created: 'Created in Zoho' } as Record<string, string>)[v.how]}{v.contact_active ? '' : ', inactive in Zoho'}</Pill>
    : v.suggest_id ? <Pill tone="warn">Possible match, confirm</Pill> : <Pill tone="bad">Not in Zoho</Pill>
  return (
    <>
      <p className="note">A vendor is linked automatically when its GSTIN is the same in Zoho. A name that only looks similar is never linked by itself: Finance confirms it, because paying the wrong party is the costliest error here.</p>
      <div className="tools"><input type="search" placeholder="Search vendor" value={q} onChange={(e) => setQ(e.target.value)} /></div>
      <Errors list={a.errors} />
      <section className="card flush"><div className="scroll"><table><thead><tr><th>Vendor</th><th>GSTIN</th><th>State</th><th>Zoho vendor</th><th>Status</th><th></th></tr></thead>
        <tbody>{rows.map((v) => <tr key={v.vendor_id}><td className="id">{v.name}<br /><span className="hint">{v.code} · {v.vendor_type}</span></td><td>{v.gstin || 'Unregistered'}</td><td>{v.state_code || <Pill tone="bad">Missing</Pill>}</td>
          <td>{v.contact_id ? <>{v.contact_name}<br /><span className="hint">{v.contact_id}{v.contact_gst ? ' · ' + v.contact_gst : ''}</span></>
            : v.suggest_id ? v.suggest_name
            : m.can_edit ? <SearchSelect value={pick[v.vendor_id] || ''} onChange={(val) => setPick({ ...pick, [v.vendor_id]: val })} emptyLabel="Pick an existing Zoho vendor…" placeholder="Search name or GSTIN" ariaLabel={'Zoho vendor for ' + v.name}
              items={free.map((c) => ({ value: c.id, label: c.name, hint: [c.extra, c.specification].filter(Boolean).join(' · ') }))} /> : '—'}</td>
          <td>{status(v)}</td>
          <td className="r">{m.can_edit && <span className="tools" style={{ flexWrap: 'nowrap', justifyContent: 'flex-end' }}>
            {v.contact_id ? <button className="link" onClick={() => a.run(() => post(`/zoho/vendors/${v.vendor_id}/unlink`), (r: any) => { onDone(); return r.message })}>Unlink</button> : <>
              {(v.suggest_id || pick[v.vendor_id]) && <button className="link" onClick={() => a.run(() => post(`/zoho/vendors/${v.vendor_id}/link`, { contact_id: pick[v.vendor_id] || v.suggest_id }), (r: any) => { onDone(); return r.message })}>Confirm link</button>}
              <button className="link" disabled={!v.bank_verified} title={v.bank_verified ? '' : 'Bank details must be verified first'} onClick={() => a.run(() => post(`/zoho/vendors/${v.vendor_id}/create`), (r: any) => { onDone(); return r.message })}>Create in Zoho</button></>}
          </span>}</td></tr>)}</tbody></table></div></section>
    </>
  )
}

function Queue() {
  const { data, reload } = useApi<{ invoice_id: number; document_id: number; document_number: string; po_number: string; invoice_number: string; vendor: string; property: string; amount: number; status: string; zoho_bill_id: string; zoho_status: string; zoho_error: string; held: string[] }[]>('/zoho/queue')
  const a = useAction()
  return (
    <>
      <div className="tools"><button className="btn ghost" disabled={a.busy} onClick={() => a.run(() => post('/zoho/read-payments'), (r: any) => { reload(); return r.message })}>Read payment status from Zoho</button>
        <Link className="btn" to="/payments">Push from Payments</Link></div>
      <Errors list={a.errors} />
      <section className="card flush"><div className="scroll"><table><thead><tr><th>Invoice</th><th>Document</th><th>Vendor</th><th>Property</th><th className="r">Amount</th><th>Zoho</th></tr></thead>
        <tbody>{(data || []).map((x) => <tr key={x.invoice_id}><td className="id">{x.invoice_number}</td><td><Link className="link" to={'/documents/' + x.document_id}>{x.po_number || x.document_number}</Link></td><td>{x.vendor}</td><td>{x.property}</td><td className="r">{inr(x.amount)}</td>
          <td>{x.zoho_bill_id ? <><Pill tone="ok">{x.status === 'PAID' ? 'Paid in Zoho' : 'Pushed'}</Pill> {x.zoho_bill_id}{x.zoho_status ? ' · ' + x.zoho_status : ''}</>
            : x.held.length ? <><Pill tone="bad">Held</Pill><br /><span className="hint">{x.held.join('; ')}</span></> : <Pill tone="ok">Ready</Pill>}
            {x.zoho_error && <><br /><span className="hint">{x.zoho_error}</span></>}</td></tr>)}</tbody></table></div>
        {data && !data.length && <p className="empty">No bills waiting.</p>}</section>
    </>
  )
}

interface Journal { property_id: number; property: string; month: string; transfers: number; expense: number; pilferage: number; total: number; lines: { account: string; note: string; debit: number; credit: number }[]; problems: string[]; posted_number: string; posted_amount: number }

function Journals() {
  const [month, setMonth] = useState(new Date().toISOString().slice(0, 7))
  const { data, reload } = useApi<Journal[]>(`/zoho/journals?month=${month}`)
  const [show, setShow] = useState<Journal | null>(null)
  const a = useAction()
  const { hasStep } = useSession()
  return (
    <>
      <p className="note">Goods issued from the Central Warehouse have no vendor bill, so each property's expense reaches Zoho through one journal a month: expense debited by sub-category for what the property received, pilferage debited to the loss account, and warehouse stock credited. Post after the month ends.</p>
      <div className="tools"><input type="month" value={month} onChange={(e) => setMonth(e.target.value)} aria-label="Month" style={{ width: 'auto' }} /></div>
      <Errors list={a.errors} />
      <section className="card flush"><div className="scroll"><table><thead><tr><th>Property</th><th className="r">Transfers received</th><th className="r">Expense</th><th className="r">Pilferage</th><th className="r">Journal total</th><th>Status</th><th></th></tr></thead>
        <tbody>{(data || []).map((j) => <tr key={j.property_id}><td className="id">{j.property}</td><td className="r">{j.transfers}</td><td className="r">{inr(j.expense)}</td><td className="r">{j.pilferage ? inr(j.pilferage) : '—'}</td><td className="r">{inr(j.total)}</td>
          <td>{j.posted_number ? <><Pill tone="ok">Posted</Pill> {j.posted_number}{j.posted_amount !== j.total && <><br /><span className="hint">{inr(j.total - j.posted_amount)} received since</span></>}</> : j.problems.length ? <><Pill tone="bad">Held</Pill><br /><span className="hint">{j.problems.join('; ')}</span></> : <Pill tone="ok">Ready</Pill>}</td>
          <td className="r"><button className="link" onClick={() => setShow(j)}>Preview</button>{hasStep('finance') && !j.posted_number && !j.problems.length && <> · <button className="link" onClick={() => a.run(() => post('/zoho/journals', { property_id: j.property_id, month }), (r: any) => { reload(); return r.message })}>Post</button></>}</td></tr>)}</tbody></table></div>
        {data && !data.length && <p className="empty">No warehouse transfers received this month.</p>}</section>
      {show && <Modal title={`Warehouse journal for ${show.property} · ${show.month}`} onClose={() => setShow(null)} wide>
        {show.problems.length > 0 && <p className="note bad"><b>Cannot be posted yet:</b> {show.problems.join('; ')}.</p>}
        <div className="scroll"><table><thead><tr><th>Account</th><th>Note</th><th className="r">Debit</th><th className="r">Credit</th></tr></thead>
          <tbody>{show.lines.map((l, i) => <tr key={i}><td className="id">{l.account || '(not mapped)'}</td><td>{l.note}</td><td className="r">{l.debit ? inr(l.debit) : ''}</td><td className="r">{l.credit ? inr(l.credit) : ''}</td></tr>)}</tbody></table></div>
        <div className="btns"><button className="btn ghost" onClick={() => setShow(null)}>Close</button></div></Modal>}
    </>
  )
}

function FieldMap() {
  const rows: [string, string, string][] = [
    ['vendor_id', 'Vendor link', 'Blocked if the vendor is not linked'], ['bill_number', 'Vendor invoice number', 'As typed at goods receipt or on the bill'],
    ['reference_number', 'PO number, or bill request number', 'For tracing back from Zoho'], ['date', 'Invoice date', 'Captured at goods receipt and on bills'],
    ['due_date, payment_terms', 'Vendor payment terms', 'Invoice date plus Net days'], ['gst_treatment', 'Vendor GST status', 'business_gst or business_none'],
    ['gst_no', 'Vendor GSTIN', 'Registered vendors only'], ['source_of_supply', 'Vendor state', 'From the first two digits of the GSTIN'],
    ['destination_of_supply', 'Property state', 'From the property city'], ['tags', 'Property reporting tag', 'On the bill and every line; blocked if missing'],
    ['location_id', 'Property location, if used', 'Optional'], ['line_items.account_id', 'Sub-category or bill type account', 'Warehouse and pre-opening use their special accounts'],
    ['line_items.name, description', 'Brand and item; SKU code, pack size, PO', 'One line per item received'], ['line_items.quantity', 'Quantity received', 'Not the quantity ordered'],
    ['line_items.rate', 'PO rate, before GST', 'is_inclusive_tax is false'], ['line_items.tax_id', 'GST rate and state rule', 'CGST + SGST within the state, IGST from another'],
    ['line_items.hsn_or_sac', 'HSN code on the SKU', 'Warning if missing'], ['adjustment', 'Invoice amount minus computed total', 'Only a difference Accounts accepted at the match'],
    ['custom_fields (unique key)', 'Purchase Desk number and goods receipt', 'A retry updates the same bill'], ['attachment', 'Invoice file', 'Second call'],
  ]
  return (
    <section className="card flush"><div className="scroll"><table><thead><tr><th>Zoho field</th><th>Filled from</th><th>Rule</th></tr></thead>
      <tbody>{rows.map((r) => <tr key={r[0]}><td className="id">{r[0]}</td><td>{r[1]}</td><td>{r[2]}</td></tr>)}</tbody></table></div></section>
  )
}
