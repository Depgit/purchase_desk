// Raise a purchase request: pick a property and items. The vendor and rate for each item come from the vendor mapping;
// items from several vendors become one request per vendor. The approval route and budget position show before submit.
// The same form clones one of the user's own requests (?clone=<id>: a new request, prefilled) and edits a request
// (/requests/<id>/edit) before anyone approves it or after it is returned.
import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { post, put } from '../api/client'
import { useLookups } from '../api/lookups'
import { useApi } from '../api/useApi'
import type { Doc } from '../api/types'
import { useSession } from '../auth/session'
import { UploadModal } from '../bulkupload/UploadModal'
import { inr, lineValue } from '../format/format'
import { Errors } from '../ui/Errors'
import { Pill } from '../ui/Pill'
import { SearchSelect } from '../ui/SearchSelect'
import { useAction } from '../ui/useAction'

interface Item {
  sku_id: number; sku_code: string; item_name: string; brand: string; pack_size: string; sub_category_id: number; sub_category: string
  category: string; sku_gst: number; vendor_id: number | null; vendor_name: string; is_warehouse: boolean; rate: number | null; rate_gst: number | null
  estimate_allowed: boolean; orderable: boolean; problem: string
}
interface Form {
  property_id: number; property: string; property_kind: string; items: Item[]
  budget: { sub_category_id: number; sub_category: string; category: string; month_budget: number; month_spent: number; week_budget: number; week_spent: number }[]
  preop_members: { id: number; name: string }[]; event_reasons: string[]
  settings: { evt_l2: boolean; l2_above: number; cc_all: boolean; wh_approval: boolean }
}
interface Draft { sku_id: number; qty: number; est_rate: number }

const KIND: Record<string, string> = { warehouse: 'Warehouse', preopening: 'Pre-opening', corporate: 'Corporate office' }

export function NewRequestPage() {
  const nav = useNavigate()
  const { id: editId } = useParams()
  const [sp] = useSearchParams()
  const cloneId = sp.get('clone')
  const src = useApi<{ document: Doc; actions: string[] }>(editId || cloneId ? `/documents/${editId || cloneId}` : null)
  const srcDoc = src.data?.document
  const pending = useRef<Doc | null>(null)
  const [skipped, setSkipped] = useState<string[]>([])
  const l = useLookups()
  const { me, hasStep } = useSession()
  // Live properties: property staff, managers and Purchase. Pre-opening: the Warehouse Manager or anyone assigned to it.
  // The warehouse: the Warehouse Manager only. Always limited to the user's raise scope; the server checks the same.
  const props = useMemo(() => (l?.properties || []).filter((p) => {
    if (!p.active || p.kind === 'corporate') return false
    if (me?.is_admin) return true
    const kindOK = p.kind === 'warehouse' ? hasStep('wh') : p.kind === 'preopening' ? hasStep('wh', 'staff', 'l1', 'purchase') : hasStep('staff', 'l1', 'purchase')
    const inScope = me?.roles.some((g) => g.rights.includes('raise') && (g.scope === 'all' || (g.scope === 'clusters' && g.cluster_ids.includes(p.cluster_id)) || (g.scope === 'properties' && g.property_ids.includes(p.id))))
    return kindOK && inScope
  }), [l, me, hasStep])
  const [pid, setPid] = useState<number>(0)
  useEffect(() => { if (!pid && props.length) setPid(props[0].id) }, [props, pid])
  const { data: form, error } = useApi<Form>(pid ? `/requests/form?property_id=${pid}` : null)
  const [lines, setLines] = useState<Draft[]>([])
  const [sku, setSku] = useState<number>(0)
  const [q, setQ] = useState('1')
  const [est, setEst] = useState('')
  const [evt, setEvt] = useState(false)
  const [evtWhy, setEvtWhy] = useState('')
  const [evtNote, setEvtNote] = useState('')
  const [preop, setPreop] = useState(0)
  const [upload, setUpload] = useState(false)
  const a = useAction()
  useEffect(() => { setLines([]); setSku(0) }, [pid])
  useEffect(() => { if (form?.preop_members.length && !preop) setPreop(form.preop_members[0].id) }, [form, preop])
  // Prefill from the request being cloned or edited: switch to its property, then copy its items once that property's
  // item list has loaded. Items no longer orderable there are left out and named.
  useEffect(() => { if (srcDoc) { pending.current = srcDoc; setPid(srcDoc.property_id) } }, [srcDoc])
  useEffect(() => {
    const d = pending.current
    if (!d || !form || form.property_id !== d.property_id) return
    pending.current = null
    const avail = new Map(form.items.map((i) => [i.sku_id, i]))
    const keep: Draft[] = []
    const miss: string[] = []
    for (const x of d.lines.filter((x) => x.qty > 0)) {
      const i = avail.get(x.sku_id)
      if (i?.orderable) keep.push({ sku_id: x.sku_id, qty: x.qty, est_rate: i.rate == null ? x.rate : 0 })
      else miss.push(x.item_name + (i ? ` (${i.problem.toLowerCase()})` : ''))
    }
    setLines(keep); setSkipped(miss)
    setEvt(!!d.event_reason); setEvtWhy(d.event_reason); setEvtNote(d.event_note)
    if (d.preop_user_id) setPreop(d.preop_user_id)
  }, [form, srcDoc])

  const items = form?.items || []
  const byId = useMemo(() => new Map(items.map((i) => [i.sku_id, i])), [items])
  const cur = byId.get(sku)
  const needEst = cur && cur.orderable && cur.rate == null
  const rateOf = (d: Draft) => { const i = byId.get(d.sku_id)!; return i.rate ?? d.est_rate }
  const gstOf = (d: Draft) => { const i = byId.get(d.sku_id)!; return i.rate_gst ?? i.sku_gst }
  const groups = useMemo(() => {
    const g = new Map<number, { vendor: string; wh: boolean; lines: Draft[] }>()
    for (const d of lines) {
      const i = byId.get(d.sku_id)
      if (!i || !i.vendor_id) continue
      if (!g.has(i.vendor_id)) g.set(i.vendor_id, { vendor: i.vendor_name, wh: i.is_warehouse, lines: [] })
      g.get(i.vendor_id)!.lines.push(d)
    }
    return [...g.values()]
  }, [lines, byId])
  const total = lines.reduce((t, d) => t + (byId.get(d.sku_id) ? lineValue(d.qty, rateOf(d), gstOf(d)) : 0), 0)
  const kindProp = form && form.property_kind !== 'live'

  function route(g: { wh: boolean; lines: Draft[] }) {
    if (!form) return ''
    if (kindProp) return (form.preop_members.find((m) => m.id === preop)?.name || 'Pre-opening Team') + ' (Pre-opening Team)'
    const s = form.settings
    if (g.wh && !s.wh_approval) return 'No approval needed'
    const amt = g.lines.reduce((t, d) => t + lineValue(d.qty, rateOf(d), gstOf(d)), 0)
    const steps: string[] = []
    if ((evt && s.evt_l2) || (s.l2_above > 0 && amt > s.l2_above)) steps.push('Cluster Manager')
    if (s.cc_all || !steps.length) steps.push('Cost Controller')
    return steps.join(' → ')
  }
  function add() {
    if (!cur || !cur.orderable) return
    const n = +q
    if (!(n > 0)) return a.setErrors(['Enter a quantity above 0.'])
    if (needEst && !(+est > 0)) return a.setErrors([`Enter an estimated rate. ${cur.vendor_name} has no rate for this item yet.`])
    const ex = lines.find((d) => d.sku_id === sku)
    setLines(ex ? lines.map((d) => d.sku_id === sku ? { ...d, qty: d.qty + n } : d) : [...lines, { sku_id: sku, qty: n, est_rate: +est || 0 }])
    setSku(0); setQ('1'); setEst(''); a.setErrors([])
  }
  const cats = [...new Set(items.map((i) => i.category))]
  if (src.error) return <p className="note bad">{src.error.message}</p>
  if (srcDoc && editId && !src.data!.actions.includes('edit')) return <><h1>Edit {srcDoc.number}</h1>
    <p className="note warn">{srcDoc.number} can no longer be edited: only the person who raised it can, and only until an approver acts on it. <Link to={'/documents/' + srcDoc.id}>Open {srcDoc.number}</Link></p></>
  if (srcDoc && cloneId && srcDoc.requester_id !== me?.id) return <><h1>Clone {srcDoc.number}</h1><p className="note warn">You can clone only requests you raised yourself.</p></>
  if (l && !props.length) return <><h1>New purchase request</h1><p className="note">You cannot raise purchase requests for any property.{hasStep('creq', 'dh') ? ' Corporate purchases are raised under Corporate.' : ' Ask the System Admin for the Raise right on your role and property access.'}</p></>
  return (
    <>
      <div className="head"><div><button className="link" onClick={() => nav(editId ? '/documents/' + editId : '/requests')}>‹ {editId && srcDoc ? srcDoc.number : 'Purchase Requests'}</button>
        <h1>{editId ? `Edit ${srcDoc?.number || 'request'}` : 'New purchase request'}</h1>
        {cloneId && srcDoc && <p className="sub">A copy of {srcDoc.number}. Change what you need and submit; {srcDoc.number} itself is not changed.</p>}
        {editId && srcDoc && <p className="sub">Change anything, then save. It goes back to the start of its approval route.</p>}</div></div>
      {skipped.length > 0 && <p className="note warn"><b>Not copied, {skipped.length === 1 ? 'this item cannot' : 'these items cannot'} be ordered here now:</b> {skipped.join(', ')}.</p>}
      <div className="cols">
        <div className="stack">
          <section className="card"><div className="grid2">
            <label className="field"><span>Property</span>
              <SearchSelect value={String(pid)} onChange={(v) => setPid(+v)} placeholder="Search property or code" ariaLabel="Property"
                items={props.map((p) => ({ value: String(p.id), label: p.name, hint: [p.code, KIND[p.kind]].filter(Boolean).join(' · ') }))} /></label>
            {kindProp && <label className="field"><span>Pre-opening approver</span>
              <select value={preop} onChange={(e) => setPreop(+e.target.value)}>{form!.preop_members.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</select>
              <span className="hint">The request goes to this member of the Pre-opening Team.</span></label>}
            <div className="field"><span>Raised by</span><p style={{ paddingTop: 8 }}>{me?.name} · {me?.department}</p></div>
          </div>{error && <p className="note bad">{error.message}</p>}
            {props.some((p) => p.id === pid && !p.doc_code) && <p className="note warn">This property has no document code yet, so a request cannot be numbered. Ask the System Admin to set its 2-letter code under Settings → Properties.</p>}</section>
          <section className="card">
            <div className="head"><h2>Add items</h2><div className="acts"><button className="btn ghost" onClick={() => setUpload(true)}>Upload item list</button></div></div>
              <label className="field"><span>Item</span>
                <SearchSelect value={sku ? String(sku) : ''} onChange={(v) => { setSku(+v); setEst('') }} emptyLabel="Choose item" placeholder="Search item, brand, SKU code or sub-category" ariaLabel="Item"
                  items={cats.flatMap((c) => items.filter((i) => i.category === c).map((i) => ({ value: String(i.sku_id), label: `${i.item_name} (${i.pack_size})${i.brand ? ' · ' + i.brand : ''}`, group: c,
                    hint: [i.sku_code, i.sub_category, i.orderable ? '' : i.problem].filter(Boolean).join(' · '), disabled: !i.orderable })))} /></label>
            <div className="addrow">
              <label className="field"><span>Vendor (from mapping)</span><input disabled value={cur ? (cur.orderable ? `${cur.vendor_name} · ${cur.rate != null ? inr(cur.rate) : 'rate to confirm'}` : cur.problem) : 'Choose item first'} /></label>
              <label className="field"><span>Quantity</span><input type="number" min="1" inputMode="decimal" value={q} onChange={(e) => setQ(e.target.value)} /></label>
              {needEst ? <label className="field"><span>Estimated rate (₹)</span><input type="number" min="0" value={est} onChange={(e) => setEst(e.target.value)} /></label> : <span />}
              <button className="btn ghost" onClick={add} disabled={!cur?.orderable}>Add item</button>
            </div>
            {cur && <p className="hint">{cur.category} › {cur.sub_category} · {cur.sku_code} · GST {cur.rate_gst ?? cur.sku_gst}%. Each item has one mapped vendor at this property.{cur.is_warehouse ? ' This item comes from the Central Warehouse.' : ''}</p>}
            <Errors list={a.errors} />
          </section>
          <section className="card flush"><h2>Items in this request</h2>
            {lines.length ? <div className="scroll"><table>
              <thead><tr><th>Item</th><th>Vendor</th><th className="r">Qty</th><th className="r">Rate</th><th className="r">GST</th><th className="r">Amount</th><th></th></tr></thead>
              <tbody>{lines.map((d) => { const i = byId.get(d.sku_id); if (!i) return null; return (
                <tr key={d.sku_id}><td>{i.item_name}<br /><span className="hint">{i.sku_code} · {i.pack_size}</span></td><td>{i.vendor_name}</td>
                  <td className="r"><input className="qty" type="number" min="1" value={d.qty} onChange={(e) => setLines(lines.map((x) => x.sku_id === d.sku_id ? { ...x, qty: +e.target.value } : x))} aria-label={'Quantity ' + i.item_name} /></td>
                  <td className="r">{inr(rateOf(d))}{i.rate == null && <> <Pill tone="warn">est.</Pill></>}</td><td className="r">{gstOf(d)}%</td>
                  <td className="r">{inr(lineValue(d.qty, rateOf(d), gstOf(d)))}</td>
                  <td className="r"><button className="link" onClick={() => setLines(lines.filter((x) => x.sku_id !== d.sku_id))}>Remove</button></td></tr>) })}
                <tr className="sum"><td colSpan={5}>Total incl. GST</td><td className="r">{inr(total)}</td><td></td></tr>
              </tbody></table></div> : <p className="empty">No items added yet.</p>}
          </section>
        </div>
        <div className="stack">
          {!kindProp && <section className="card"><h2>Ordering extra for an event?</h2>
            <label className="ck"><input type="checkbox" checked={evt} onChange={(e) => setEvt(e.target.checked)} />Yes, ask the Cluster Manager to approve the larger quantity</label>
            {evt && <>
              <label className="field"><span>Reason</span><select value={evtWhy} onChange={(e) => setEvtWhy(e.target.value)}><option value="">Choose</option>{form?.event_reasons.map((r) => <option key={r}>{r}</option>)}</select></label>
              <label className="field"><span>Event details</span><input value={evtNote} onChange={(e) => setEvtNote(e.target.value)} placeholder="e.g. 120 pax banquet lunch on 8 Oct" /></label>
            </>}
          </section>}
          <section className="card"><h2>Approval route</h2>
            {groups.length ? groups.map((g) => <div key={g.vendor}><b>{g.vendor}</b> · {inr(g.lines.reduce((t, d) => t + lineValue(d.qty, rateOf(d), gstOf(d)), 0))}<p className="sub">{route(g)}</p></div>) : <p className="sub">Add items to see who will approve.</p>}
            {groups.length > 1 && <p className="hint">One request is created per vendor.</p>}
            <button className="btn" disabled={!lines.length || a.busy} onClick={() => {
              const body = { property_id: pid, lines, event_reason: evt ? evtWhy : '', event_note: evt ? evtNote : '', preop_user_id: kindProp ? preop : 0 }
              a.run(() => editId ? put(`/requests/${editId}`, body) : post('/requests', body), (r: any) => { nav(editId ? '/documents/' + editId : '/requests'); return r.message })
            }}>{editId ? 'Save and send for approval' : 'Submit request'}</button>
          </section>
          {form && form.budget.length > 0 && <section className="card"><h2>Budget left at {form.property}</h2>
            {form.budget.filter((b) => lines.some((d) => byId.get(d.sku_id)?.sub_category_id === b.sub_category_id) || lines.length === 0).slice(0, 12).map((b) => {
              const mine = lines.filter((d) => byId.get(d.sku_id)?.sub_category_id === b.sub_category_id).reduce((t, d) => t + lineValue(d.qty, rateOf(d), gstOf(d)), 0)
              const used = b.month_spent + mine
              const pc = b.month_budget ? Math.min(100, used / b.month_budget * 100) : 100
              return <div key={b.sub_category_id}>
                <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 14, fontWeight: 600 }}><span>{b.sub_category}</span><span className="num">{inr(used)} / {inr(b.month_budget)}</span></div>
                <div className="bar"><i className={used > b.month_budget ? 'over' : pc > 85 ? 'hi' : ''} style={{ width: pc + '%' }} /></div>
                {mine > 0 && b.week_spent + mine > b.week_budget && <span className="hint">Takes this week over its budget of {inr(b.week_budget)}. Approvers will see a flag; it does not block.</span>}
              </div>
            })}</section>}
        </div>
      </div>
      {upload && <UploadModal kind="request_lines" title="Upload item list for this request" query={`?property_id=${pid}`} onClose={() => setUpload(false)}
        onRows={(objs) => {
          const add: Draft[] = objs.map((o: any) => ({ sku_id: o.sku_id, qty: o.qty, est_rate: o.est_rate }))
          setLines((cur) => { const m = new Map(cur.map((d) => [d.sku_id, d])); add.forEach((d) => m.set(d.sku_id, m.has(d.sku_id) ? { ...m.get(d.sku_id)!, qty: m.get(d.sku_id)!.qty + d.qty } : d)); return [...m.values()] })
          setUpload(false)
        }} />}
    </>
  )
}
