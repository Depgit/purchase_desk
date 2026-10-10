// Monthly budgets per property and sub-category, split into weeks and days, against approved spend; and the F&B sales that drive F&B budgets.
import { useEffect, useState } from 'react'
import { download, put } from '../api/client'
import { useLookups } from '../api/lookups'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { UploadModal } from '../bulkupload/UploadModal'
import { inr, thisMonth } from '../format/format'
import { Pill } from '../ui/Pill'
import { Seg, Tabs } from '../ui/Tabs'
import { useToast } from '../ui/Toast'

interface Over { property_id: number; code: string; property: string; cluster: string; month_budget: number; budget_to_date: number; spent: number; left: number; pace: string; tone: string; categories: { category: string; pace: string; tone: string; ratio: number }[] }
interface SubDetail { sub_category_id: number; sub_category: string; category: string; is_fb: boolean; month_budget: number; budget_to_date: number; spent: number; pace: string; tone: string; weeks: { from: number; to: number; budget: number; spent: number }[]; day_budget: number[]; day_spent: number[] }
interface Detail { month: string; days: number; today: number; current_week: number; allowed_fb: number[]; sub_categories: SubDetail[]; can_edit: boolean }
interface FB { property_id: number; property: string; cluster: string; plan_sale: number; ird_sale: number; banquet_sale: number; total_sale: number; allowed_cost: number; ordered: number; cost_pct_of_sale: number; pace: string; tone: string; month_budget: number; plan_pct: number; ird_pct: number; banquet_pct: number }

export function BudgetsPage() {
  const l = useLookups()
  const { hasStep, hasRight } = useSession()
  const [tab, setTab] = useState<'all' | 'one' | 'fb'>('all')
  const [m, setM] = useState(thisMonth())
  const [cl, setCl] = useState('')
  const [pid, setPid] = useState(0)
  const [upload, setUpload] = useState<'budgets' | 'fb_sales' | null>(null)
  const canEdit = hasStep('cc')
  const live = (l?.properties || []).filter((p) => p.active && p.kind === 'live')
  useEffect(() => { if (!pid && live.length) setPid(live[0].id) }, [live, pid])
  return (
    <>
      <div className="head"><div><h1>Budgets</h1><p className="sub">One amount per property, sub-category and month, split evenly into days and into 7-day weeks from the 1st. Spend counts on the day a request is finally approved.</p></div>
        <div className="acts"><input type="month" value={m} onChange={(e) => setM(e.target.value)} aria-label="Month" style={{ width: 'auto' }} />
          <button className="btn ghost" onClick={() => download(`/budgets/export?month=${m}`, `budgets-${m}.csv`)}>Download budgets CSV</button>
          {canEdit && hasRight('upload') && <><button className="btn ghost" onClick={() => setUpload('budgets')}>Bulk upload / edit budgets</button><button className="btn ghost" onClick={() => setUpload('fb_sales')}>Upload F&amp;B sales</button></>}</div></div>
      <Tabs value={tab} onChange={setTab} items={[['all', 'All properties'], ['one', 'Property detail'], ['fb', 'F&B sales']]} />
      {tab !== 'one' && <div className="tools"><select value={cl} onChange={(e) => setCl(e.target.value)} aria-label="Cluster"><option value="">All clusters</option>{l?.clusters.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}</select></div>}
      {tab === 'all' && <Overview m={m} cl={cl} onOpen={(id) => { setPid(id); setTab('one') }} />}
      {tab === 'one' && <PropertyDetail m={m} pid={pid} setPid={setPid} props={live.map((p) => [p.id, p.name])} canEdit={canEdit} />}
      {tab === 'fb' && <Sales m={m} cl={cl} />}
      {upload && <UploadModal kind={upload} title={upload === 'budgets' ? 'Bulk upload or edit monthly budgets' : 'Upload F&B sales'} exportQuery={`?month=${m}`} onClose={() => setUpload(null)} />}
    </>
  )
}

function Overview({ m, cl, onOpen }: { m: string; cl: string; onOpen: (id: number) => void }) {
  const { data } = useApi<Over[]>(`/budgets/overview?month=${m}&cluster_id=${cl || 0}`)
  const rows = [...(data || [])].sort((a, b) => b.categories.reduce((t, c) => Math.max(t, c.ratio), -9) - a.categories.reduce((t, c) => Math.max(t, c.ratio), -9))
  const cats = data?.[0]?.categories.map((c) => c.category) || []
  return (
    <section className="card flush"><div className="scroll"><table>
      <thead><tr><th>Property</th><th className="r">Month budget</th><th className="r">Budget to date</th><th className="r">Spent</th><th>Pace</th><th className="r">Left for month</th>{cats.map((c) => <th key={c}>{c}</th>)}</tr></thead>
      <tbody>{rows.map((r) => <tr key={r.property_id} className="click" onClick={() => onOpen(r.property_id)}>
        <td className="id">{r.property}</td><td className="r">{inr(r.month_budget)}</td><td className="r">{inr(r.budget_to_date)}</td><td className="r">{inr(r.spent)}</td>
        <td><Pill tone={r.tone as any}>{r.pace}</Pill></td><td className="r">{inr(r.left)}</td>
        {r.categories.map((c) => <td key={c.category}><Pill tone={c.tone as any}>{c.pace}</Pill></td>)}</tr>)}</tbody></table></div>
      <p className="hint" style={{ padding: '10px 14px' }}>Over pace means spend to date is more than 5% above budget to date. Click a property for its sub-category, week and day view.</p></section>
  )
}

function PropertyDetail({ m, pid, setPid, props, canEdit }: { m: string; pid: number; setPid: (n: number) => void; props: [number, string][]; canEdit: boolean }) {
  const { data, reload } = useApi<Detail>(pid ? `/budgets/property?property_id=${pid}&month=${m}` : null)
  const [view, setView] = useState<'m' | 'w' | 'd'>('m')
  const toast = useToast()
  const save = async (sc: number, v: number) => {
    try { await put('/budgets', { property_id: pid, sub_category_id: sc, month: m, amount: v }); toast('Budget saved'); reload() } catch (e: any) { toast(e.message, true) }
  }
  const subs = data?.sub_categories || []
  return (
    <>
      <div className="tools">
        <select value={pid} onChange={(e) => setPid(+e.target.value)} aria-label="Property">{props.map(([id, n]) => <option key={id} value={id}>{n}</option>)}</select>
        <Seg value={view} onChange={setView} items={[['m', 'Month'], ['w', 'Weeks'], ['d', 'Days']]} />
        {data && <span className="sub">Day {data.today} of {data.days} · week {data.current_week}</span>}
      </div>
      <section className="card flush"><div className="scroll">
        {view === 'm' && <table><thead><tr><th>Sub-category</th><th>Category</th><th className="r">Month budget</th><th className="r">Per day</th><th className="r">Budget to date</th><th className="r">Spent</th><th>Pace</th><th className="r">Left</th></tr></thead>
          <tbody>{subs.map((s) => <tr key={s.sub_category_id}><td>{s.sub_category}</td><td>{s.category}</td>
            <td className="r">{canEdit ? <input className="qty" style={{ width: 120 }} type="number" min="0" step="1000" defaultValue={s.month_budget} key={s.month_budget}
              onBlur={(e) => { const v = +e.target.value; if (v !== s.month_budget) save(s.sub_category_id, v) }} aria-label={s.sub_category + ' month budget'} /> : inr(s.month_budget)}</td>
            <td className="r">{s.is_fb ? <span className="sub">by sales</span> : inr(s.month_budget / (data?.days || 30))}</td>
            <td className="r">{inr(s.budget_to_date)}</td><td className="r">{inr(s.spent)}</td><td><Pill tone={s.tone as any}>{s.pace}</Pill></td><td className="r">{inr(s.month_budget - s.spent)}</td></tr>)}</tbody></table>}
        {view === 'w' && <table><thead><tr><th>Sub-category</th>{subs[0]?.weeks.map((w, i) => <th key={i} className="r">Week {i + 1} ({w.from}–{w.to}){i + 1 === data?.current_week ? ' · now' : ''}</th>)}</tr></thead>
          <tbody>{subs.map((s) => <tr key={s.sub_category_id}><td>{s.sub_category}</td>{s.weeks.map((w, i) => <td key={i} className="r">
            {i + 1 > (data?.current_week || 0) ? <span className="sub">{inr(w.budget)}</span> : <><Pill tone={w.spent > w.budget ? 'bad' : 'ok'}>{inr(w.spent)}</Pill><br /><span className="hint">of {inr(w.budget)}</span></>}</td>)}</tr>)}</tbody></table>}
        {view === 'd' && <table><thead><tr><th>Day</th><th className="r">Day budget</th><th className="r">Spent</th>{data && data.allowed_fb.some((x) => x > 0) && <th className="r">F&amp;B allowed from sales</th>}</tr></thead>
          <tbody>{Array.from({ length: data?.today || 0 }, (_, k) => data!.today - 1 - k).map((k) => {
            const b = subs.reduce((t, s) => t + s.day_budget[k], 0), sp = subs.reduce((t, s) => t + s.day_spent[k], 0)
            return <tr key={k}><td>{k + 1} {data!.month}</td><td className="r">{inr(b)}</td><td className="r">{sp > b ? <Pill tone="bad">{inr(sp)}</Pill> : inr(sp)}</td>
              {data!.allowed_fb.some((x) => x > 0) && <td className="r">{inr(data!.allowed_fb[k])}</td>}</tr>
          })}</tbody></table>}
      </div>{canEdit && view === 'm' && <p className="hint" style={{ padding: '10px 14px' }}>Change a month budget and leave the box. Week and day budgets are recalculated and the change is logged.</p>}</section>
    </>
  )
}

function Sales({ m, cl }: { m: string; cl: string }) {
  const { data } = useApi<FB[]>(`/fb-sales?month=${m}&cluster_id=${cl || 0}`)
  return (
    <>
      <p className="note">Allowed F&amp;B cost for a day = Plan sale × its cost % + IRD sale × its cost % + Banquet sale × its cost %, shared between the F&amp;B sub-categories in the ratio of their month budgets.
        Sales are uploaded by file; nothing is pulled from eZee. <button className="link" onClick={() => download(`/fb-sales/export?month=${m}`, `fnb-sales-${m}.csv`)}>Download sales CSV</button></p>
      <section className="card flush"><div className="scroll"><table>
        <thead><tr><th>Property</th><th className="r">Plan</th><th className="r">IRD</th><th className="r">Banquet</th><th className="r">Total sale</th><th className="r">Allowed cost</th><th className="r">Ordered</th><th className="r">Cost % of sale</th><th>Against allowed</th><th>Cost % set</th></tr></thead>
        <tbody>{(data || []).map((r) => <tr key={r.property_id}><td className="id">{r.property}</td><td className="r">{inr(r.plan_sale)}</td><td className="r">{inr(r.ird_sale)}</td><td className="r">{inr(r.banquet_sale)}</td>
          <td className="r">{inr(r.total_sale)}</td><td className="r">{inr(r.allowed_cost)}</td><td className="r">{inr(r.ordered)}</td><td className="r">{r.total_sale ? r.cost_pct_of_sale.toFixed(1) + '%' : '—'}</td>
          <td><Pill tone={r.tone as any}>{r.pace}</Pill></td><td>{r.plan_pct} / {r.ird_pct} / {r.banquet_pct}</td></tr>)}</tbody></table></div></section>
    </>
  )
}
