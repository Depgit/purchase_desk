// The approvals workbench: requests waiting for the signed-in approver, grouped by property, city, cluster,
// category, sub-category or vendor. Tick one, many or a whole group, and approve, reject, return or send to the Cluster Manager.
import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { post } from '../api/client'
import { useLookups } from '../api/lookups'
import type { Doc } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { day, inr, month, qty } from '../format/format'
import { Pill } from '../ui/Pill'
import { Seg } from '../ui/Tabs'
import { useAction } from '../ui/useAction'
import { ReasonModal, type ReasonKind } from './ReasonModal'
import { useScreen } from '../screenaccess/screen'

type GroupBy = 'property' | 'city' | 'cluster' | 'category' | 'sub' | 'vendor' | 'none'
const GB: [GroupBy, string][] = [['property', 'Property'], ['city', 'City'], ['cluster', 'Cluster'], ['category', 'Category'], ['sub', 'Sub-category'], ['vendor', 'Vendor'], ['none', 'One list']]
type Edits = Record<number, Record<number, number>>

function keyOf(d: Doc, g: GroupBy): [string, string] {
  const cat = d.kind === 'bill' ? 'Utilities & AMC' : d.lines[0]?.category || ''
  const sub = d.kind === 'bill' ? d.bill_type : d.lines[0]?.sub_category || ''
  switch (g) {
    case 'property': return [String(d.property_id), d.property]
    case 'city': return [d.city, d.city]
    case 'cluster': return [String(d.cluster_id), d.cluster]
    case 'category': return [cat, cat]
    case 'sub': return [sub, sub]
    case 'vendor': return [String(d.vendor_id), d.vendor]
    default: return ['all', 'All requests']
  }
}

function withEdits(d: Doc, e?: Record<number, number>) {
  if (!e) return d.amount
  return d.lines.reduce((t, l) => t + (e[l.id] ?? l.qty) * l.rate * (1 + l.gst_pct / 100), 0)
}

export function ApprovalsPage() {
  const { hasStep, me } = useSession()
  const sc = useScreen('approvals')
  const l = useLookups()
  const { data, reload, loading } = useApi<Doc[]>('/approvals')
  const [g, setG] = useState<GroupBy>('property')
  const [cur, setCur] = useState('')
  const [sel, setSel] = useState<Set<number>>(new Set())
  const [open, setOpen] = useState<Set<number>>(new Set())
  const [edits, setEdits] = useState<Edits>({})
  const [f, setF] = useState({ q: '', city: '', cluster: '', cat: '', budget: '', type: '', raised: '' })
  const [reason, setReason] = useState<{ kind: ReasonKind; ids: number[] } | null>(null)
  const a = useAction()

  const rows = useMemo(() => {
    let r = data || []
    const now = new Date(); const t0 = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime(); const DAY = 864e5
    if (f.q) { const s = f.q.toLowerCase(); r = r.filter((d) => [d.number, d.property, d.vendor, ...d.lines.map((x) => x.item_name), d.bill_type].join(' ').toLowerCase().includes(s)) }
    if (f.city) r = r.filter((d) => d.city === f.city)
    if (f.cluster) r = r.filter((d) => String(d.cluster_id) === f.cluster)
    if (f.cat) r = r.filter((d) => (d.kind === 'bill' ? 'Utilities & AMC' : d.lines[0]?.category) === f.cat)
    if (f.budget) r = r.filter((d) => f.budget === 'in' ? !d.flagged : f.budget === 'mo' ? d.over_month > 0 : d.over_week > 0 && d.over_month === 0)
    if (f.type) r = r.filter((d) => f.type === 'evt' ? !!d.event_reason : f.type === 'cm' ? !!d.cm_approved_at : f.type === 'rt' ? !!d.routed_reason : f.type === 'bill' ? d.kind === 'bill' : d.lines.length > 1)
    if (f.raised) {
      const ts = (d: Doc) => new Date(d.created_at).getTime()
      r = r.filter((d) => f.raised === 'today' ? ts(d) >= t0 : f.raised === 'yday' ? ts(d) >= t0 - DAY && ts(d) < t0 : f.raised === 'old' ? ts(d) < t0 - DAY : ts(d) >= t0 - 6 * DAY)
    }
    return r
  }, [data, f])

  const groups = useMemo(() => {
    const m = new Map<string, { key: string; name: string; docs: Doc[]; sum: number; flagged: number }>()
    for (const d of rows) {
      const [k, n] = keyOf(d, g)
      if (!m.has(k)) m.set(k, { key: k, name: n, docs: [], sum: 0, flagged: 0 })
      const x = m.get(k)!
      x.docs.push(d); x.sum += withEdits(d, edits[d.id]); if (d.flagged) x.flagged++
    }
    return [...m.values()].sort((a, b) => a.name.localeCompare(b.name))
  }, [rows, g, edits])

  const current = groups.find((x) => x.key === cur) || groups[0]
  const shown = g === 'none' ? rows : current?.docs || []
  const ok = shown.filter((d) => !d.flagged), flagged = shown.filter((d) => d.flagged)
  const selDocs = (data || []).filter((d) => sel.has(d.id))

  if (hasStep('l2', 'cc', 'preop') && !sc.edit) return <><h1>Approvals</h1><p className="note">Your role can only view Approvals, so it cannot approve here. Requests waiting for approval show under Purchase Requests.</p></>
  if (!hasStep('l2', 'cc', 'preop')) return <><h1>Approvals</h1><p className="note">Approval rights belong to the Cluster Manager, the Cost Controller and the Pre-opening Team.</p></>

  async function bulk(action: string, ids: number[], reasonText = '', note = '') {
    const quantities: Edits = {}
    ids.forEach((id) => { if (edits[id]) quantities[id] = edits[id] })
    const r = await a.run(() => post<{ done: number; failed: number; results: { id: number; ok: boolean; message: string }[] }>('/approvals/bulk', { action, ids, quantities, reason: reasonText, note }),
      (r) => r.failed ? `${r.done} done. ${r.failed} not done: ${r.results.filter((x) => !x.ok).map((x) => x.message).join('; ')}` : `${r.done} ${action === 'approve' ? 'approved' : action === 'route' ? 'sent to the Cluster Manager' : action === 'reject' ? 'rejected' : 'returned'}`)
    if (r) { setSel(new Set()); setReason(null); setEdits({}); reload() }
  }
  const setQty = (d: Doc, lineId: number, v: number) => setEdits({ ...edits, [d.id]: { ...(edits[d.id] || {}), [lineId]: v } })
  const fsel = (k: keyof typeof f, opts: [string, string][], label: string) =>
    <select aria-label={label} value={f[k]} onChange={(e) => setF({ ...f, [k]: e.target.value })}>{opts.map(([v, t]) => <option key={v} value={v}>{t}</option>)}</select>

  const table = (docs: Doc[]) => (
    <div className="scroll"><table>
      <thead><tr><th></th><th>Request</th>{g !== 'property' && <th>Property</th>}<th>Item</th><th className="r">Quantity</th><th className="r">Amount</th><th>Budget</th><th></th></tr></thead>
      <tbody>{docs.map((d) => {
        const one = d.kind === 'purchase' && d.lines.length === 1
        const e = edits[d.id]
        return [
          <tr key={d.id}>
            <td><input type="checkbox" checked={sel.has(d.id)} onChange={() => { const s = new Set(sel); s.has(d.id) ? s.delete(d.id) : s.add(d.id); setSel(s) }} aria-label={'Select ' + d.number} /></td>
            <td className="nowrap"><Link className="link" to={'/documents/' + d.id}>{d.number}</Link><br /><span className="hint">{day(d.created_at)} · {d.age_days} d</span>
              {d.event_reason && <><br /><Pill>Event: {d.event_reason}</Pill></>}{d.cm_approved_at && <><br /><Pill tone="ok">Cluster Manager approved</Pill></>}
              {d.routed_reason && <><br /><Pill tone="warn">Sent by Cost Controller</Pill></>}{d.property_kind !== 'live' && <><br /><Pill tone="warn">{d.property_kind}</Pill></>}</td>
            {g !== 'property' && <td>{d.property}</td>}
            <td className="wide">{d.kind === 'bill' ? <>{d.bill_type}<br /><span className="hint">{month(d.period_start)}{d.months > 1 ? ` · ${d.months} months` : ''} · {d.vendor}{d.previous_bill != null ? ` · previous ${inr(d.previous_bill)}` : ''}</span></>
              : one ? <>{d.lines[0].item_name}<br /><span className="hint">{d.lines[0].sub_category} · {d.vendor}</span></>
              : <><b>{d.lines.length} items</b><br /><span className="hint">{[...new Set(d.lines.map((x) => x.sub_category))].join(', ')} · {d.vendor}</span></>}</td>
            <td className="r">{one ? <><input className="qty" type="number" min="0" value={e?.[d.lines[0].id] ?? d.lines[0].qty} onChange={(ev) => setQty(d, d.lines[0].id, +ev.target.value)} aria-label={'Quantity ' + d.number} />
              <br /><span className="hint">{d.lines[0].pack_size}{e ? ' · was ' + qty(d.lines[0].qty) : ''}</span></>
              : d.kind === 'bill' ? '—' : <button className="btn ghost small" onClick={() => { const s = new Set(open); s.has(d.id) ? s.delete(d.id) : s.add(d.id); setOpen(s) }}>{open.has(d.id) ? '▾ Hide items' : '▸ Review items'}</button>}</td>
            <td className="r">{inr(withEdits(d, e))}</td>
            <td>{d.kind === 'bill' ? (d.bill_flags.length ? d.bill_flags.map((x) => <Pill key={x} tone="warn">{x}</Pill>) : <Pill>Bill</Pill>)
              : d.property_kind !== 'live' ? <Pill tone="off">No monthly budget</Pill>
              : d.over_month > 0 ? <Pill tone="bad">Over month by {inr(d.over_month)}</Pill> : d.over_week > 0 ? <Pill tone="warn">Over week by {inr(d.over_week)}</Pill> : <Pill tone="ok">Within</Pill>}</td>
            <td><div className="tools" style={{ justifyContent: 'flex-end', flexWrap: 'nowrap' }}>
              <button className="link" onClick={() => setReason({ kind: 'reject', ids: [d.id] })}>Reject</button>
              <button className="link" onClick={() => setReason({ kind: 'return', ids: [d.id] })}>Return</button>
              {d.owner === 'cc' && d.kind === 'purchase' && <button className="link" onClick={() => setReason({ kind: 'route', ids: [d.id] })}>To CM</button>}
              <button className="btn ghost small" disabled={a.busy} onClick={() => bulk('approve', [d.id])}>Approve</button></div></td>
          </tr>,
          open.has(d.id) && !one && d.kind === 'purchase' ? <tr key={d.id + 'x'}><td></td><td colSpan={7} style={{ paddingTop: 0 }}>
            <div className="xpbox"><table><thead><tr><th>Item</th><th>Sub-category</th><th className="r">Quantity</th><th>Unit</th><th className="r">Rate</th><th className="r">Amount</th></tr></thead>
              <tbody>{d.lines.map((x) => { const qv = e?.[x.id] ?? x.qty; return <tr key={x.id} className={qv === 0 ? 'dim' : ''}><td>{x.item_name} <span className="hint">{x.sku_code}</span></td><td>{x.sub_category}</td>
                <td className="r"><input className="qty" type="number" min="0" value={qv} onChange={(ev) => setQty(d, x.id, +ev.target.value)} aria-label={'Quantity ' + x.item_name} />{qv !== x.qty && <><br /><span className="hint">was {qty(x.qty)}</span></>}</td>
                <td>{x.pack_size}</td><td className="r">{inr(x.rate)}</td><td className="r">{inr(qv * x.rate * (1 + x.gst_pct / 100))}</td></tr> })}</tbody></table>
              <p className="hint">Set a quantity to 0 to drop an item. The requester sees every change.</p></div></td></tr> : null,
        ]
      })}</tbody>
    </table></div>
  )

  return (
    <>
      <div className="head"><div><h1>Approvals</h1><p className="sub">{(data || []).length} waiting {me?.is_admin ? 'with every approver (you can act on all as System Admin)' : 'for you'}, {inr((data || []).reduce((t, d) => t + d.amount, 0))} in total{rows.length !== (data || []).length ? ` · ${rows.length} match the filters` : ''}.</p></div></div>
      <div className="tools"><Seg label="Group by" value={g} onChange={(v) => { setG(v); setCur(''); setSel(new Set()) }} items={GB} /></div>
      <div className="tools">
        {fsel('city', [['', 'All cities'], ...(l?.cities || []).map((c) => [c.name, c.name] as [string, string])], 'City')}
        {fsel('cluster', [['', 'All clusters'], ...(l?.clusters || []).map((c) => [String(c.id), c.name] as [string, string])], 'Cluster')}
        {fsel('cat', [['', 'All categories'], ...(l?.categories || []).map((c) => [c.name, c.name] as [string, string]), ['Utilities & AMC', 'Utilities & AMC']], 'Category')}
        {fsel('budget', [['', 'Any budget status'], ['in', 'Within budget'], ['wk', 'Over week budget'], ['mo', 'Over month budget']], 'Budget')}
        {fsel('type', [['', 'All requests'], ['multi', 'More than one item'], ['evt', 'Event requests'], ['cm', 'Cluster Manager approved'], ['rt', 'Sent by Cost Controller'], ['bill', 'Utility and AMC bills']], 'Type')}
        {fsel('raised', [['', 'Raised any day'], ['today', 'Raised today'], ['yday', 'Raised yesterday'], ['old', 'Waiting more than a day'], ['d7', 'Last 7 days']], 'Raised')}
        <input type="search" placeholder="Search item, vendor, request" value={f.q} onChange={(e) => setF({ ...f, q: e.target.value })} aria-label="Search" />
      </div>
      {loading && !data ? <p className="sub">Loading…</p> : !shown.length ? <section className="card"><p>Nothing is waiting{rows.length !== (data || []).length ? ' with these filters' : ''}.</p></section> : (
        <div className={g === 'none' ? '' : 'appr'}>
          {g !== 'none' && <section className="card flush groups">{groups.map((x) => (
            <div key={x.key} className={'group' + (x.key === current?.key ? ' on' : '')}>
              <input type="checkbox" aria-label={'Select all in ' + x.name} checked={x.docs.every((d) => sel.has(d.id))} onChange={() => {
                const s = new Set(sel); const all = x.docs.every((d) => s.has(d.id)); x.docs.forEach((d) => all ? s.delete(d.id) : s.add(d.id)); setSel(s)
              }} />
              <button onClick={() => setCur(x.key)}><span>{x.name}<small>{x.docs.length} · {inr(x.sum)}</small></span>{x.flagged ? <Pill tone="bad">{x.flagged} over</Pill> : <Pill tone="ok">All within</Pill>}</button>
            </div>))}</section>}
          <section className="card flush">
            <div className="shead" style={{ background: 'none', borderTop: 0 }}><div style={{ flex: 1 }}><h2>{g === 'none' ? 'All requests' : current?.name}</h2>
              <p className="sub">{shown.length} requests · {inr(shown.reduce((t, d) => t + withEdits(d, edits[d.id]), 0))}</p></div></div>
            {ok.length > 0 && <><div className="shead"><b>Within budget · {ok.length}</b><button className="btn" disabled={a.busy} onClick={() => bulk('approve', ok.map((d) => d.id))}>Approve all {ok.length}</button></div>{table(ok)}</>}
            {flagged.length > 0 && <><div className="shead"><b>Needs a closer look · {flagged.length}</b></div>{table(flagged)}</>}
            <p className="hint" style={{ padding: '10px 14px' }}>Change a quantity before approving and the requester is told. "To CM" sends a request to the Cluster Manager; if approved there it goes straight to the PO stage.</p>
          </section>
        </div>
      )}
      {a.errors.length > 0 && <p className="note bad">{a.errors.join(' ')}</p>}
      {selDocs.length > 0 && (
        <div className="bulk"><b>{selDocs.length} selected · {inr(selDocs.reduce((t, d) => t + withEdits(d, edits[d.id]), 0))}{selDocs.some((d) => d.flagged) ? ` · ${selDocs.filter((d) => d.flagged).length} over budget` : ''}</b>
          <button className="btn ghost" onClick={() => setSel(new Set())}>Clear</button>
          <button className="btn no" onClick={() => setReason({ kind: 'reject', ids: [...sel] })}>Reject</button>
          <button className="btn amber" onClick={() => setReason({ kind: 'return', ids: [...sel] })}>Return</button>
          {hasStep('cc') && <button className="btn amber" onClick={() => setReason({ kind: 'route', ids: selDocs.filter((d) => d.owner === 'cc' && d.kind === 'purchase').map((d) => d.id) })}>Send to Cluster Manager</button>}
          <button className="btn" disabled={a.busy} onClick={() => bulk('approve', [...sel])}>Approve {selDocs.length}</button>
        </div>
      )}
      {reason && <ReasonModal kind={reason.kind} count={reason.ids.length} busy={a.busy} onClose={() => setReason(null)} onConfirm={(r, n) => bulk(reason.kind, reason.ids, r, n)} />}
    </>
  )
}
