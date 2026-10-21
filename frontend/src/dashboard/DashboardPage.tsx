// Each person's dashboard: four standard views plus one they arrange themselves, with "Needs attention" on top.
// Tiles open the matching section. The chosen view and "My view" are saved per user.
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { get, put } from '../api/client'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { dateTime, day, inr, qty } from '../format/format'
import { Errors } from '../ui/Errors'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { Seg } from '../ui/Tabs'

interface Tile { value: string; sub: string; alert: boolean; section: string }
interface Data { default_view: string; tiles: Record<string, Tile>; sections: Record<string, any>; alerts: { urgent: boolean; text: string; link: string }[] }
interface Layout { k: string[]; s: string[] }
interface Pref { view?: string; my?: Layout }

const TILE: Record<string, string> = { mine: 'My tasks', bud: 'Budget used, month to date', over: 'Over budget, waiting', new: 'New requests, 24 hours', l2: 'With Cluster Manager', cc: 'With Cost Controller',
  dlv: 'Deliveries due', pay: 'To be paid', msme: 'MSME due in 7 days', zoho: 'Held from Zoho', bills: 'Utility and AMC waiting', corp: 'Corporate open', wh: 'Warehouse stock low', rates: 'Rates expiring' }
const SECTION: Record<string, string> = { mine: 'My tasks', bud: 'Budget against spend', top: 'Properties over pace', new: 'New requests', l2: 'Cluster Manager', cc: 'Cost Controller',
  dlv: 'Deliveries due', bills: 'Utility and AMC', pay: 'Payables', zoho: 'Zoho push', corp: 'Corporate', wh: 'Warehouse', rates: 'Expiring rates', upd: 'Updates', age: 'Waiting longest' }
const VIEWS: Record<string, { name: string } & Layout> = {
  ops: { name: 'Property operations', k: ['mine', 'dlv', 'bud', 'new'], s: ['mine', 'dlv', 'upd', 'bud'] },
  cc: { name: 'Cost control', k: ['bud', 'cc', 'over', 'l2', 'new', 'rates'], s: ['cc', 'bud', 'top', 'new', 'l2', 'rates'] },
  fin: { name: 'Finance', k: ['pay', 'msme', 'zoho', 'bills', 'corp'], s: ['pay', 'zoho', 'bills', 'corp', 'upd'] },
  mgmt: { name: 'Management', k: ['bud', 'over', 'cc', 'pay', 'corp', 'wh'], s: ['bud', 'top', 'cc', 'pay', 'corp', 'wh'] },
}
const MY_START: Record<string, Layout> = { whl2: { k: ['mine', 'wh', 'dlv', 'new'], s: ['mine', 'wh', 'dlv', 'upd'] }, preop: { k: ['mine', 'wh', 'new'], s: ['mine', 'wh', 'new', 'upd'] },
  creq: { k: ['mine', 'corp', 'dlv'], s: ['mine', 'corp', 'upd'] }, dh: { k: ['mine', 'corp', 'bud'], s: ['mine', 'corp', 'upd', 'bud'] } }

export function DashboardPage() {
  const nav = useNavigate()
  const { me } = useSession()
  const { data, error } = useApi<Data>('/dashboard')
  const [pref, setPref] = useState<Pref | null>(null)
  const [view, setView] = useState('')
  const [tab, setTab] = useState('')
  const [allAlerts, setAllAlerts] = useState(false)
  const [custom, setCustom] = useState(false)
  useEffect(() => { get<Pref>('/prefs/dashboard').then((p) => setPref(p || {})).catch(() => setPref({})) }, [])
  if (error) return <p className="note bad">{error.message}</p>
  if (!data || !pref) return <p className="sub">Loading…</p>
  const step = me?.roles.find((r) => MY_START[r.step])?.step
  const roleDefault = data.default_view
  const myStart: Layout = (step && MY_START[step]) || VIEWS[roleDefault] || VIEWS.mgmt
  const my = pref.my || myStart
  const defView = pref.view || roleDefault
  const v = view || defView
  const layout = v === 'my' ? my : VIEWS[v] || VIEWS.mgmt
  const sec = layout.s.includes(tab) ? tab : layout.s[0]
  const savePref = (p: Pref) => { setPref(p); put('/prefs/dashboard', p).catch(() => {}) }
  const alerts = allAlerts ? data.alerts : data.alerts.slice(0, 4)
  return (
    <>
      <div className="head"><div><h1>Namaste, {me?.name.split(' ')[0]}</h1>
        <p className="sub">{me?.is_admin ? 'System Admin' : me?.roles.map((r) => r.name).join(' + ')} · {new Date().toLocaleDateString('en-IN', { weekday: 'long', day: 'numeric', month: 'long' })}</p></div></div>
      <div className="tools">
        <Seg label="Dashboard view" value={v} onChange={(x) => { setView(x); setTab('') }} items={[...Object.entries(VIEWS).map(([k, x]) => [k, x.name] as [string, string]), ['my', 'My view']]} />
        <span className="tools">{v === defView ? <span className="hint">Your default</span> : <button className="link" onClick={() => savePref({ ...pref, view: v })}>Make this my default</button>}
          <button className="btn ghost" onClick={() => setCustom(true)}>Customize my view</button></span>
      </div>
      {data.alerts.length ? <section className="card"><h2>Needs attention ({data.alerts.length})</h2>
        {alerts.map((a, i) => <p key={i} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 10 }}>
          <span><Pill tone={a.urgent ? 'bad' : 'warn'}>{a.urgent ? 'Urgent' : 'Soon'}</Pill> {a.text}</span><button className="link" onClick={() => nav(a.link)}>Open ›</button></p>)}
        {data.alerts.length > 4 && <p><button className="link" onClick={() => setAllAlerts(!allAlerts)}>{allAlerts ? 'Show fewer' : `Show all ${data.alerts.length}`}</button></p>}</section>
        : <p className="note ok">Nothing critical needs your attention right now.</p>}
      <div className="tiles">{layout.k.map((k) => { const t = data.tiles[k]; if (!t) return null
        return <button key={k} className={'tile' + (sec === t.section ? ' on' : '')} onClick={() => { if (!layout.s.includes(t.section)) setView(Object.keys(VIEWS).find((x) => VIEWS[x].s.includes(t.section)) || v); setTab(t.section) }}>
          <span>{TILE[k]}</span><b style={t.alert ? { color: 'var(--bad)' } : undefined}>{t.value}</b><small>{t.sub}</small></button> })}</div>
      <div className="tools"><Seg label="Section" value={sec} onChange={setTab} items={layout.s.map((k) => [k, SECTION[k]] as [string, string])} /></div>
      <section className="card flush"><Section k={sec} data={data.sections[sec]} /></section>
      {custom && <Customize start={my} roleStart={myStart} onClose={() => setCustom(false)} onSave={(l) => { savePref({ view: 'my', my: l }); setView('my'); setTab(''); setCustom(false) }} />}
    </>
  )
}

function Section({ k, data }: { k: string; data: any }) {
  const nav = useNavigate()
  const rows: any[] = data || []
  const none = (t: string) => <p className="empty">{t}</p>
  if (['mine', 'new', 'l2', 'cc', 'dlv', 'bills', 'corp', 'age'].includes(k)) {
    if (!rows.length) return none('Nothing here right now.')
    return <div className="scroll"><table><thead><tr><th>Document</th><th>Property</th><th>Details</th><th className="r">Amount</th><th>Status</th><th className="r">Days</th></tr></thead>
      <tbody>{rows.map((r, i) => <tr key={i} className="click" onClick={() => nav('/documents/' + r.id)}><td className="id">{r.number}</td><td>{r.property}</td>
        <td>{r.details}{r.flag && <><br /><Pill tone={r.flag.startsWith('Over month') ? 'bad' : 'warn'}>{r.flag}</Pill></>}</td><td className="r">{inr(r.amount)}</td><td>{r.status}</td><td className="r">{r.days}</td></tr>)}</tbody></table></div>
  }
  if (k === 'bud') return <div className="scroll"><table><thead><tr><th>Category</th><th className="r">Budget to date</th><th className="r">Approved spend</th><th>Used</th><th>Pace</th><th className="r">Last month, same days</th><th className="r">Month budget</th></tr></thead>
    <tbody>{rows.map((c) => { const pc = c.budget_to_date ? c.spent / c.budget_to_date : 0; return <tr key={c.category}><td>{c.category}</td><td className="r">{inr(c.budget_to_date)}</td><td className="r">{inr(c.spent)}</td>
      <td style={{ minWidth: 130 }}><div className="bar"><i className={pc > 1.05 ? 'over' : pc > 0.95 ? 'hi' : ''} style={{ width: Math.min(100, pc * 100) + '%' }} /></div><span className="hint">{Math.round(pc * 100)}%</span></td>
      <td><Pill tone={c.tone}>{c.pace}</Pill></td><td className="r">{inr(c.last_month_same_days)}</td><td className="r">{inr(c.month_budget)}</td></tr> })}</tbody></table></div>
  if (k === 'top') return rows.length ? <div className="scroll"><table><thead><tr><th>Property</th><th>Cluster</th><th className="r">Budget to date</th><th className="r">Approved spend</th><th>Pace</th></tr></thead>
    <tbody>{rows.map((r) => <tr key={r.property_id}><td className="id">{r.property}</td><td>{r.cluster}</td><td className="r">{inr(r.budget_to_date)}</td><td className="r">{inr(r.spent)}</td><td><Pill tone="bad">{r.pace}</Pill></td></tr>)}</tbody></table></div>
    : none('Every property is within 5% of its budget pace.')
  if (k === 'pay' || k === 'zoho') return rows.length ? <div className="scroll"><table><thead><tr><th>Invoice</th><th>Vendor</th><th>Property</th><th className="r">Amount</th><th>Pay by</th><th>{k === 'zoho' ? 'What is missing' : 'Status'}</th></tr></thead>
    <tbody>{rows.map((r) => <tr key={r.invoice_id} className="click" onClick={() => nav('/invoices/' + r.invoice_id)}><td className="id">{r.invoice}</td><td>{r.vendor}{r.msme && <> <Pill tone="warn">MSME</Pill></>}</td><td>{r.property}</td>
      <td className="r">{inr(r.amount)}</td><td>{r.pay_by && r.pay_by < new Date().toISOString().slice(0, 10) ? <Pill tone="bad">Overdue {day(r.pay_by)}</Pill> : day(r.pay_by)}</td><td>{k === 'zoho' ? r.held : r.status.replace('_', ' ').toLowerCase()}</td></tr>)}</tbody></table></div>
    : none(k === 'zoho' ? 'Nothing is held back from Zoho.' : 'Nothing to pay.')
  if (k === 'wh') return rows.length ? <div className="scroll"><table><thead><tr><th>SKU</th><th>Item</th><th className="r">In stock</th><th className="r">Reorder level</th></tr></thead>
    <tbody>{rows.map((r) => <tr key={r.sku_id}><td className="id">{r.sku_code}</td><td>{r.item}</td><td className="r">{qty(r.closing)}</td><td className="r">{qty(r.min_level)}</td></tr>)}</tbody></table></div>
    : none('All warehouse items are above their reorder level.')
  if (k === 'rates') return rows.length ? <div className="scroll"><table><thead><tr><th>Vendor</th><th>SKU</th><th>Item</th><th className="r">Rate</th><th>Valid to</th><th className="r">Days left</th></tr></thead>
    <tbody>{rows.map((r, i) => <tr key={i}><td>{r.vendor}</td><td className="id">{r.sku}</td><td>{r.item}</td><td className="r">{inr(r.rate)}</td><td>{day(r.valid_to)}</td><td className="r">{r.days_left < 0 ? <Pill tone="bad">Expired</Pill> : r.days_left}</td></tr>)}</tbody></table></div>
    : none('No mapped vendor rate expires in the next 15 days.')
  if (k === 'upd') return rows.length ? <ul className="log" style={{ padding: 16 }}>{rows.map((u, i) => <li key={i}><b><button className="link" onClick={() => nav('/documents/' + u.document_id)}>{u.number}</button> {u.action}</b>
    <span>{u.user} · {dateTime(u.at)}</span>{u.note && <><br />“{u.note}”</>}</li>)}</ul> : none('Nothing new in the last 7 days.')
  return none('')
}

function Customize({ start, roleStart, onClose, onSave }: { start: Layout; roleStart: Layout; onClose: () => void; onSave: (l: Layout) => void }) {
  const [l, setL] = useState<Layout>({ k: [...start.k], s: [...start.s] })
  const [err, setErr] = useState<string[]>([])
  const list = (g: 'k' | 's', names: Record<string, string>) => {
    const on = l[g], off = Object.keys(names).filter((x) => !on.includes(x))
    const move = (i: number, d: number) => { const a = [...on]; const j = i + d; if (j < 0 || j >= a.length) return;[a[i], a[j]] = [a[j], a[i]]; setL({ ...l, [g]: a }) }
    return <>{on.map((x, i) => <div key={x} className="colrow"><label className="ck"><input type="checkbox" checked onChange={() => setL({ ...l, [g]: on.filter((y) => y !== x) })} />{names[x]}</label>
      <span className="tools"><button className="btn ghost small" onClick={() => move(i, -1)} aria-label="Move up">▲</button><button className="btn ghost small" onClick={() => move(i, 1)} aria-label="Move down">▼</button></span></div>)}
      {off.map((x) => <div key={x} className="colrow" style={{ opacity: 0.7 }}><label className="ck"><input type="checkbox" checked={false} onChange={() => {
        if (g === 'k' && on.length >= 6) { setErr(['Up to 6 tiles. Untick one first.']); return }
        setErr([]); setL({ ...l, [g]: [...on, x] }) }} />{names[x]}</label></div>)}</>
  }
  return (
    <Modal title="Customize my view" onClose={onClose} wide>
      <p className="sub">Pick up to 6 tiles and any sections, and put them in your order. Saved to your user.</p>
      <div className="grid2"><section><h3>Tiles ({l.k.length} of 6)</h3>{list('k', TILE)}</section><section><h3>Sections ({l.s.length})</h3>{list('s', SECTION)}</section></div>
      <Errors list={err} />
      <div className="btns"><button className="btn ghost" onClick={() => setL({ k: [...roleStart.k], s: [...roleStart.s] })}>Reset to my role</button><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={!l.k.length || !l.s.length} onClick={() => onSave(l)}>Save my view</button></div>
    </Modal>
  )
}
