// Raise a purchase request: pick a property, then tap items to add them, as on a shop counter. Items are tiles by
// category (and "Often ordered" at this property); a tap adds one, + and − change the quantity. The vendor and rate of
// each item come from the vendor mapping; the cart on the right groups the items by vendor (one request per vendor),
// with the approval route and budget position before submit. Save draft keeps the cart for later; "Repeat last order"
// adds the property's latest submission. The same form clones one of the user's own requests (?clone=<id>) and edits
// a request (/requests/<id>/edit) before anyone approves it or after it is returned.
import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { get, post, put } from '../api/client'
import { useLookups } from '../api/lookups'
import { useApi } from '../api/useApi'
import type { Doc } from '../api/types'
import { useSession } from '../auth/session'
import { UploadModal } from '../bulkupload/UploadModal'
import { dateTime, day, inr, lineValue } from '../format/format'
import { DraftControls, DraftList, useDraft, type Draft as SavedDraft } from '../formdrafts/drafts'
import { Errors } from '../ui/Errors'
import { SearchSelect } from '../ui/SearchSelect'
import { useToast } from '../ui/Toast'
import { useAction } from '../ui/useAction'

interface Item {
  sku_id: number; sku_code: string; item_name: string; brand: string; pack_size: string; sub_category_id: number; sub_category: string
  category: string; sku_gst: number; vendor_id: number | null; vendor_name: string; is_warehouse: boolean; rate: number | null; rate_gst: number | null
  estimate_allowed: boolean; orderable: boolean; problem: string
  /** false: several vendors may supply it here; options lists them (the default first) and the requester picks one. */
  daily_use: boolean; options: Option[]
}
interface Option { vendor_id: number; vendor_name: string; rate: number | null; rate_gst: number | null; estimate_allowed: boolean; orderable: boolean; problem: string }
interface Form {
  property_id: number; property: string; property_kind: string; items: Item[]
  budget: { sub_category_id: number; sub_category: string; category: string; month_budget: number; month_spent: number; week_budget: number; week_spent: number }[]
  preop_members: { id: number; name: string }[]; event_reasons: string[]
  settings: { evt_l2: boolean; l2_above: number; cc_all: boolean; wh_approval: boolean }
  often: number[]; last: { numbers: string[]; date: string; lines: { sku_id: number; qty: number }[] } | null
}
interface Draft { sku_id: number; qty: number; est_rate: number; vendor_id?: number }
interface Fields { pid: number; lines: Draft[]; evt: boolean; evtWhy: string; evtNote: string; preop: number }

const KIND: Record<string, string> = { warehouse: 'Warehouse', preopening: 'Pre-opening', corporate: 'Corporate office' }
const COLOURS = ['#2F6FDB', '#1E9E6A', '#C77700', '#9B4DCA', '#D1495B', '#0E8A9E', '#5B6B8C']

export function NewRequestPage() {
  const nav = useNavigate()
  const toast = useToast()
  const { id: editId } = useParams()
  const [sp] = useSearchParams()
  const cloneId = sp.get('clone')
  const draftId = Number(sp.get('draft')) || 0
  // ?property=<id>&lines=<sku>:<qty>,... pre-fills a request (the warehouse's shortfall purchase).
  const prefill = useRef<{ pid: number; lines: Draft[] } | null>(sp.get('property') ? {
    pid: Number(sp.get('property')),
    lines: (sp.get('lines') || '').split(',').map((x) => x.split(':').map(Number)).filter(([s, q]) => s > 0 && q > 0).map(([s, q]) => ({ sku_id: s, qty: q, est_rate: 0 })),
  } : null)
  const src = useApi<{ document: Doc; actions: string[] }>(editId || cloneId ? `/documents/${editId || cloneId}` : null)
  const srcDoc = src.data?.document
  const pending = useRef<Doc | null>(null)
  const pendingDraft = useRef<Fields | null>(null)
  const [skipped, setSkipped] = useState<string[]>([])
  const l = useLookups()
  const { me, hasStep } = useSession()
  // Live properties: property staff, managers, property chefs and Purchase. Pre-opening: the Warehouse Manager or anyone
  // assigned to it. The warehouse: its Warehouse Staff (the Warehouse Manager approves). Always limited to the user's
  // raise scope; the server checks the same.
  const props = useMemo(() => (l?.properties || []).filter((p) => {
    if (!p.active || p.kind === 'corporate' || p.fin_status === 'pending') return false // a new property waits for Finance
    if (me?.is_admin) return true
    const kindOK = p.kind === 'warehouse' ? hasStep('whs', 'wh') : p.kind === 'preopening' ? hasStep('wh', 'staff', 'l1', 'chef', 'purchase') : hasStep('staff', 'l1', 'chef', 'purchase')
    const inScope = me?.roles.some((g) => g.rights.includes('raise') && (g.scope === 'all' || (g.scope === 'clusters' && g.cluster_ids.includes(p.cluster_id)) || (g.scope === 'properties' && g.property_ids.includes(p.id))))
    return kindOK && inScope
  }), [l, me, hasStep])
  const [pid, setPid] = useState<number>(0)
  useEffect(() => { if (!pid && props.length) setPid(props[0].id) }, [props, pid])
  const { data: form, error } = useApi<Form>(pid ? `/requests/form?property_id=${pid}` : null)
  const [lines, setLines] = useState<Draft[]>([])
  const [tab, setTab] = useState('')
  const [sub, setSub] = useState(0)
  const [find, setFind] = useState('')
  const [evt, setEvt] = useState(false)
  const [evtWhy, setEvtWhy] = useState('')
  const [evtNote, setEvtNote] = useState('')
  const [preop, setPreop] = useState(0)
  const [upload, setUpload] = useState(false)
  const a = useAction()
  const draft = useDraft('request')
  useEffect(() => { setLines([]); setTab(''); setSub(0) }, [pid])
  useEffect(() => { if (form?.preop_members.length && !preop) setPreop(form.preop_members[0].id) }, [form, preop])
  // Prefill from the request being cloned or edited: switch to its property, then copy its items once that property's
  // item list has loaded. Items no longer orderable there are left out and named.
  useEffect(() => { if (srcDoc) { pending.current = srcDoc; setPid(srcDoc.property_id) } }, [srcDoc])
  useEffect(() => { if (prefill.current) setPid(prefill.current.pid) }, [])
  // A saved draft (?draft=<id>) comes back the same way.
  useEffect(() => {
    if (!draftId) return
    get<SavedDraft<Fields>[]>('/drafts?form=request').then((list) => {
      const d = list.find((x) => x.id === draftId)
      if (!d) return
      draft.open(d)
      pendingDraft.current = d.fields
      setPid(d.fields.pid)
    }).catch(() => {})
  }, [draftId]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!form) return
    const avail = new Map(form.items.map((i) => [i.sku_id, i]))
    const d = pending.current
    if (d && form.property_id === d.property_id) {
      pending.current = null
      const keep: Draft[] = []
      const miss: string[] = []
      for (const x of d.lines.filter((x) => x.qty > 0)) {
        const i = avail.get(x.sku_id)
        const o = i && !i.daily_use ? i.options.find((v) => v.vendor_id === d.vendor_id && v.orderable) : undefined
        if (o) keep.push({ sku_id: x.sku_id, qty: x.qty, est_rate: o.rate == null ? x.rate : 0, vendor_id: o.vendor_id })
        else if (i?.orderable) keep.push({ sku_id: x.sku_id, qty: x.qty, est_rate: i.rate == null ? x.rate : 0 })
        else miss.push(x.item_name + (i ? ` (${i.problem.toLowerCase()})` : ''))
      }
      setLines(keep); setSkipped(miss)
      setEvt(!!d.event_reason); setEvtWhy(d.event_reason); setEvtNote(d.event_note)
      if (d.preop_user_id) setPreop(d.preop_user_id)
    }
    const pf = prefill.current
    if (pf && form.property_id === pf.pid) {
      prefill.current = null
      setLines(pf.lines.filter((x) => avail.get(x.sku_id)?.orderable))
      setSkipped(pf.lines.filter((x) => !avail.get(x.sku_id)?.orderable).map((x) => avail.get(x.sku_id)?.item_name || 'item ' + x.sku_id))
    }
    const f = pendingDraft.current
    if (f && form.property_id === f.pid) {
      pendingDraft.current = null
      setLines(f.lines.filter((x) => avail.get(x.sku_id)?.orderable))
      setEvt(f.evt); setEvtWhy(f.evtWhy); setEvtNote(f.evtNote)
      if (f.preop) setPreop(f.preop)
    }
  }, [form, srcDoc])

  const items = form?.items || []
  const byId = useMemo(() => new Map(items.map((i) => [i.sku_id, i])), [items])
  const qtyOf = (sku: number) => lines.find((d) => d.sku_id === sku)?.qty || 0
  const setQty = (sku: number, n: number) => setLines((cur) => {
    const q = Math.max(0, Math.round(n * 1000) / 1000)
    if (!q) return cur.filter((d) => d.sku_id !== sku)
    return cur.some((d) => d.sku_id === sku) ? cur.map((d) => d.sku_id === sku ? { ...d, qty: q } : d) : [...cur, { sku_id: sku, qty: q, est_rate: 0 }]
  })
  // The vendor a line goes to: the one picked for an item that is not for daily consumption, else the item's vendor.
  const pick = (d: Draft) => {
    const i = byId.get(d.sku_id)!
    const o = d.vendor_id ? i.options.find((v) => v.vendor_id === d.vendor_id) : undefined
    return o ? { vendor_id: o.vendor_id, vendor: o.vendor_name, rate: o.rate, gst: o.rate_gst ?? i.sku_gst, wh: false }
      : { vendor_id: i.vendor_id || 0, vendor: i.vendor_name, rate: i.rate, gst: i.rate_gst ?? i.sku_gst, wh: i.is_warehouse }
  }
  const rateOf = (d: Draft) => pick(d).rate ?? d.est_rate
  const gstOf = (d: Draft) => pick(d).gst
  const setVendor = (sku: number, vendor: number) => setLines((cur) => cur.map((d) => d.sku_id === sku ? { ...d, vendor_id: vendor, est_rate: 0 } : d))
  const groups = useMemo(() => {
    const g = new Map<number, { vendor: string; wh: boolean; lines: Draft[] }>()
    for (const d of lines) {
      if (!byId.get(d.sku_id)) continue
      const v = pick(d)
      if (!v.vendor_id) continue
      if (!g.has(v.vendor_id)) g.set(v.vendor_id, { vendor: v.vendor, wh: v.wh, lines: [] })
      g.get(v.vendor_id)!.lines.push(d)
    }
    return [...g.values()]
  }, [lines, byId]) // eslint-disable-line react-hooks/exhaustive-deps
  const total = lines.reduce((t, d) => t + (byId.get(d.sku_id) ? lineValue(d.qty, rateOf(d), gstOf(d)) : 0), 0)
  const kindProp = form && form.property_kind === 'preopening' // asks which Pre-opening Team member approves
  const whProp = form && form.property_kind === 'warehouse'

  // The menu: "Often ordered" first when there is history, then all items, then one tab per category.
  const cats = useMemo(() => [...new Set(items.map((i) => i.category))], [items])
  const often = useMemo(() => (form?.often || []).map((id) => byId.get(id)).filter((i): i is Item => !!i), [form, byId])
  const cur = tab || (often.length ? 'often' : 'all')
  const needle = find.trim().toLowerCase()
  const shown = needle ? items.filter((i) => [i.item_name, i.brand, i.sku_code, i.sub_category, i.category].join(' ').toLowerCase().includes(needle))
    : cur === 'often' ? often : cur === 'all' ? items : items.filter((i) => i.category === cur && (!sub || i.sub_category_id === sub))
  const subs = cur !== 'often' && cur !== 'all' ? [...new Map(items.filter((i) => i.category === cur).map((i) => [i.sub_category_id, i.sub_category])).entries()] : []

  function route(g: { wh: boolean; lines: Draft[] }) {
    if (!form) return ''
    if (whProp) return 'Warehouse Manager'
    if (kindProp) return (form.preop_members.find((m) => m.id === preop)?.name || 'Pre-opening Team') + ' (Pre-opening Team)'
    const s = form.settings
    if (g.wh && !s.wh_approval) return 'No approval needed'
    const amt = g.lines.reduce((t, d) => t + lineValue(d.qty, rateOf(d), gstOf(d)), 0)
    const steps: string[] = []
    if ((evt && s.evt_l2) || (s.l2_above > 0 && amt > s.l2_above)) steps.push('Cluster Manager')
    if (s.cc_all || !steps.length) steps.push('Cost Controller')
    return steps.join(' → ')
  }
  function repeatLast() {
    const last = form?.last
    if (!last) return
    const add = last.lines.filter((x) => byId.get(x.sku_id)?.orderable)
    if (!add.length) return toast(`None of the items on ${last.numbers.join(', ')} can be ordered here now.`)
    setLines((curLines) => {
      const m = new Map(curLines.map((d) => [d.sku_id, d]))
      for (const x of add) {
        m.set(x.sku_id, m.has(x.sku_id) ? { ...m.get(x.sku_id)!, qty: m.get(x.sku_id)!.qty + x.qty } : { sku_id: x.sku_id, qty: x.qty, est_rate: 0 })
      }
      return [...m.values()]
    })
    toast(`${add.length} item${add.length === 1 ? '' : 's'} added from ${last.numbers.join(', ')} (${day(last.date)}). Change the quantities before sending.`)
  }
  const fields = (): Fields => ({ pid, lines, evt, evtWhy, evtNote, preop })
  const saveDraft = () => draft.save(`${form?.property || 'Request'} · ${lines.length} item${lines.length === 1 ? '' : 's'} · ${inr(total)}`, fields(), {})
  const submit = () => {
    const noRate = lines.filter((d) => byId.get(d.sku_id) && pick(d).rate == null && !(d.est_rate > 0)).map((d) => byId.get(d.sku_id)!.item_name)
    if (noRate.length) return a.setErrors([`Enter an estimated rate for: ${noRate.join(', ')}. These vendors have no rate for them yet.`])
    const body = { property_id: pid, lines, event_reason: evt ? evtWhy : '', event_note: evt ? evtNote : '', preop_user_id: kindProp ? preop : 0 }
    a.run(() => editId ? put(`/requests/${editId}`, body) : post('/requests', body), (r: any) => { draft.clear(); nav(editId ? '/documents/' + editId : '/requests'); return r.message })
  }

  if (src.error) return <p className="note bad">{src.error.message}</p>
  if (srcDoc && editId && !src.data!.actions.includes('edit')) return <><h1>Edit {srcDoc.number}</h1>
    <p className="note warn">{srcDoc.number} can no longer be edited: only the person who raised it can, and only until an approver acts on it. <Link to={'/documents/' + srcDoc.id}>Open {srcDoc.number}</Link></p></>
  if (srcDoc && cloneId && srcDoc.requester_id !== me?.id) return <><h1>Clone {srcDoc.number}</h1><p className="note warn">You can clone only requests you raised yourself.</p></>
  if (l && !props.length) return <><h1>New purchase request</h1><p className="note">You cannot raise purchase requests for any property.{hasStep('creq', 'dh') ? ' Corporate purchases are raised under Corporate.' : ' Ask the System Admin for the Raise right on your role and property access.'}</p></>
  const colour = (c: string) => COLOURS[Math.max(0, cats.indexOf(c)) % COLOURS.length]
  const tile = (i: Item) => {
    const n = qtyOf(i.sku_id)
    return (
      <div key={i.sku_id} className={'mt' + (n ? ' on' : '') + (i.orderable ? '' : ' off')} style={{ ['--mc' as string]: colour(i.category) }}>
        <button className="mt-main" disabled={!i.orderable} onClick={() => setQty(i.sku_id, n + 1)} aria-label={'Add ' + i.item_name}>
          <b>{i.item_name}</b><span>{[i.brand, i.pack_size].filter(Boolean).join(' · ')}</span>
          <span>{!i.orderable ? i.problem : i.options.length > 1 ? `${i.options.length} vendors · pick in the cart` : i.vendor_name}</span>
          <span className="pr">{i.rate != null ? inr(i.rate) : i.orderable ? 'Rate to be entered' : ''}</span>
        </button>
        {n ? <div className="mt-q"><button onClick={() => setQty(i.sku_id, n - 1)} aria-label={'One less ' + i.item_name}>−</button>
          <input type="number" inputMode="decimal" min="0" value={n} onChange={(e) => setQty(i.sku_id, +e.target.value)} aria-label={'Quantity of ' + i.item_name} />
          <button onClick={() => setQty(i.sku_id, n + 1)} aria-label={'One more ' + i.item_name}>+</button></div>
          : i.orderable ? <button className="mt-add" onClick={() => setQty(i.sku_id, 1)}>+ Add</button> : null}
      </div>
    )
  }
  return (
    <>
      <div className="head"><div><button className="link" onClick={() => nav(editId ? '/documents/' + editId : '/requests')}>‹ {editId && srcDoc ? srcDoc.number : 'Purchase Requests'}</button>
        <h1>{editId ? `Edit ${srcDoc?.number || 'request'}` : draftId ? 'Draft purchase request' : 'New purchase request'}</h1>
        <p className="sub">{cloneId && srcDoc ? `A copy of ${srcDoc.number}. Change what you need and submit; ${srcDoc.number} itself is not changed.`
          : editId && srcDoc ? 'Change anything, then save. It goes back to the start of its approval route.'
          : 'Tap an item to add it; tap again or use + and − to change the quantity. Save a draft at any time and send when the list is complete.'}</p></div>
        <div className="acts">
          {form?.last && !editId && <button className="btn ghost" onClick={repeatLast} title={`${form.last.numbers.join(', ')} on ${day(form.last.date)}`}>Repeat last order</button>}
          <button className="btn ghost" onClick={() => setUpload(true)}>Upload item list</button></div></div>
      {!editId && !cloneId && !draftId && <DraftList form="request" label="Saved drafts" onOpen={(d) => nav(`/requests/new?draft=${d.id}`)} />}
      {draftId > 0 && draft.savedAt && <p className="note">Your draft from {dateTime(draft.savedAt)} is filled in. A draft is only saved, not sent: press Send for approval when the list is complete.</p>}
      {skipped.length > 0 && <p className="note warn"><b>Not copied, {skipped.length === 1 ? 'this item cannot' : 'these items cannot'} be ordered here now:</b> {skipped.join(', ')}.</p>}
      <div className="pos">
        <div className="stack">
          <section className="card"><div className="grid3">
            <label className="field"><span>Property</span>
              <SearchSelect value={String(pid)} onChange={(v) => setPid(+v)} placeholder="Search property or code" ariaLabel="Property"
                items={props.map((p) => ({ value: String(p.id), label: p.name, hint: [p.code, KIND[p.kind]].filter(Boolean).join(' · ') }))} /></label>
            {kindProp && <label className="field"><span>Pre-opening approver</span>
              <select value={preop} onChange={(e) => setPreop(+e.target.value)}>{form!.preop_members.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</select></label>}
            <label className="field"><span>Find an item</span><input type="search" value={find} onChange={(e) => setFind(e.target.value)} placeholder="Name, brand, code or sub-category" aria-label="Find an item" /></label>
          </div>{error && <p className="note bad">{error.message}</p>}
            {props.some((p) => p.id === pid && !p.doc_code) && <p className="note warn">This property has no document code yet, so a request cannot be numbered. Ask the System Admin to set its 2-letter code under Settings → Properties.</p>}</section>
          <div className="ctabs" role="tablist" aria-label="Category">
            {often.length > 0 && <button className={'ctab' + (cur === 'often' && !needle ? ' on' : '')} onClick={() => { setTab('often'); setSub(0); setFind('') }}>Often ordered<small>{often.length}</small></button>}
            <button className={'ctab' + (cur === 'all' && !needle ? ' on' : '')} onClick={() => { setTab('all'); setSub(0); setFind('') }}>All items<small>{items.length}</small></button>
            {cats.map((c) => <button key={c} className={'ctab' + (cur === c && !needle ? ' on' : '')} onClick={() => { setTab(c); setSub(0); setFind('') }}>{c}<small>{items.filter((i) => i.category === c).length}</small></button>)}
          </div>
          {!needle && subs.length > 1 && <div className="chips">{[[0, 'All'] as [number, string], ...subs].map(([id, n]) => <button key={id} className={'chip' + (sub === id ? ' on' : '')} onClick={() => setSub(id)}>{n}</button>)}</div>}
          {needle && <p className="hint">{shown.length} items match "{find.trim()}" across all categories.</p>}
          <div className="mgrid">{shown.length ? shown.map(tile) : <p className="sub">{form ? 'No item here.' : 'Loading items…'}</p>}</div>
        </div>
        <aside className="card pos-cart" id="cart">
          <h2>This request · {lines.length} item{lines.length === 1 ? '' : 's'}</h2>
          {groups.length ? <>
            {groups.map((g) => (
              <div key={g.vendor} className="cgroup">
                <p className="cvend"><b>{g.vendor}</b><span className="num">{inr(g.lines.reduce((t, d) => t + lineValue(d.qty, rateOf(d), gstOf(d)), 0))}</span></p>
                <p className="hint">{route(g)}</p>
                {g.lines.map((d) => { const i = byId.get(d.sku_id)!; const v = pick(d); return (
                  <div key={d.sku_id} className="cline">
                    <div><b>{i.item_name}</b><br /><span className="hint">{i.pack_size} · {v.rate == null
                      ? <>rate ₹ <input className="cest" type="number" min="0" inputMode="decimal" value={d.est_rate || ''} onChange={(e) => setLines(lines.map((x) => x.sku_id === d.sku_id ? { ...x, est_rate: +e.target.value } : x))} aria-label={'Estimated rate for ' + i.item_name} /></>
                      : inr(v.rate)} · {inr(lineValue(d.qty, rateOf(d), gstOf(d)))}</span>
                      {i.options.length > 1 && <select className="cvsel" value={v.vendor_id} onChange={(e) => setVendor(d.sku_id, +e.target.value)} aria-label={'Vendor for ' + i.item_name}>
                        {i.options.map((o) => <option key={o.vendor_id} value={o.vendor_id} disabled={!o.orderable}>{o.vendor_name} · {o.rate != null ? inr(o.rate) : o.orderable ? 'rate to be entered' : o.problem}</option>)}</select>}</div>
                    <div className="qs"><button onClick={() => setQty(d.sku_id, d.qty - 1)} aria-label={'One less ' + i.item_name}>−</button>
                      <input type="number" inputMode="decimal" min="0" value={d.qty} onChange={(e) => setQty(d.sku_id, +e.target.value)} aria-label={'Quantity of ' + i.item_name} />
                      <button onClick={() => setQty(d.sku_id, d.qty + 1)} aria-label={'One more ' + i.item_name}>+</button>
                      <button onClick={() => setQty(d.sku_id, 0)} aria-label={'Remove ' + i.item_name} title="Remove">×</button></div>
                  </div>) })}
              </div>))}
            <p className="ctotal"><b>Total incl. GST</b><b>{inr(total)}</b></p>
            {groups.length > 1 && <p className="hint">One request is sent per vendor.</p>}
            {form && form.budget.length > 0 && <><h3>Budget this month</h3>
              {form.budget.filter((b) => lines.some((d) => byId.get(d.sku_id)?.sub_category_id === b.sub_category_id)).map((b) => {
                const mine = lines.filter((d) => byId.get(d.sku_id)?.sub_category_id === b.sub_category_id).reduce((t, d) => t + lineValue(d.qty, rateOf(d), gstOf(d)), 0)
                const left = b.month_budget - b.month_spent
                return <div key={b.sub_category_id} className="cbud"><span>{b.sub_category}</span>
                  <span className="num">{inr(mine)} of {inr(Math.max(0, left))} left{mine > left && <> <span className="pill bad">Over</span></>}{mine <= left && b.week_spent + mine > b.week_budget && <> <span className="pill warn">Over week</span></>}</span></div>
              })}</>}
          </> : <p className="sub">Nothing added yet. Tap items on the left.</p>}
          {form?.property_kind === 'live' && <>
            <label className="ck"><input type="checkbox" checked={evt} onChange={(e) => setEvt(e.target.checked)} />Ordering extra for an event</label>
            {evt && <>
              <label className="field"><span>Reason</span><select value={evtWhy} onChange={(e) => setEvtWhy(e.target.value)}><option value="">Choose</option>{form?.event_reasons.map((r) => <option key={r}>{r}</option>)}</select></label>
              <label className="field"><span>Event details</span><input value={evtNote} onChange={(e) => setEvtNote(e.target.value)} placeholder="e.g. 120 pax banquet lunch on 8 Oct" /></label>
            </>}</>}
          <Errors list={a.errors} />
          <div className="btns">{!editId && <DraftControls draft={draft} onSave={saveDraft} />}
            <button className="btn" disabled={!lines.length || a.busy} onClick={submit}>{editId ? 'Save and send for approval' : 'Send for approval'}</button></div>
          {lines.length > 0 && <p><button className="link" onClick={() => setLines([])}>Clear all</button></p>}
        </aside>
      </div>
      {lines.length > 0 && <div className="cartbar"><span>{lines.length} item{lines.length === 1 ? '' : 's'} · {inr(total)}</span><a href="#cart">View request ›</a></div>}
      {upload && <UploadModal kind="request_lines" title="Upload item list for this request" query={`?property_id=${pid}`} onClose={() => setUpload(false)}
        onRows={(objs) => {
          const add: Draft[] = objs.map((o: any) => ({ sku_id: o.sku_id, qty: o.qty, est_rate: o.est_rate }))
          setLines((c) => { const m = new Map(c.map((d) => [d.sku_id, d])); add.forEach((d) => m.set(d.sku_id, m.has(d.sku_id) ? { ...m.get(d.sku_id)!, qty: m.get(d.sku_id)!.qty + d.qty } : d)); return [...m.values()] })
          setUpload(false)
        }} />}
    </>
  )
}
