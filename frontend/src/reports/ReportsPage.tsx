// The 14 reports: pick one, set the date range and filters, look at it, download it, or download every report as one Excel pack.
import { MultiSelect } from '../ui/MultiSelect'
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { download } from '../api/client'
import { useLookups } from '../api/lookups'
import { useApi } from '../api/useApi'
import { addDays, inr, today } from '../format/format'
import { DataTable } from '../ui/DataTable'

interface Cat { key: string; title: string; use: string; group: string }
interface Res { key: string; title: string; use: string; columns: { key: string; label: string; right: boolean }[]; rows: unknown[][]; document_ids?: number[] }
const BUDGET = ['budget', 'budget_months', 'category_cluster']
const NO_DATES = ['ageing', 'fnb', 'payables', 'rates', ...BUDGET]

function preset(p: string): [string, string] {
  const t = today(), d = new Date()
  const ymd = (x: Date) => `${x.getFullYear()}-${String(x.getMonth() + 1).padStart(2, '0')}-${String(x.getDate()).padStart(2, '0')}`
  switch (p) {
    case '7': return [addDays(t, -6), t]
    case 'lm': return [ymd(new Date(d.getFullYear(), d.getMonth() - 1, 1)), ymd(new Date(d.getFullYear(), d.getMonth(), 0))]
    case 'fy': return [ymd(new Date(d.getMonth() >= 3 ? d.getFullYear() : d.getFullYear() - 1, 3, 1)), t]
    default: return [t.slice(0, 8) + '01', t]
  }
}

export function ReportsPage() {
  const nav = useNavigate()
  const l = useLookups()
  const cat = useApi<Cat[]>('/reports')
  const [key, setKey] = useState('tracker')
  const [f, setF] = useState(() => { const [from, to] = preset('tm'); return { from, to, basis: '', property_id: [] as string[], cluster_id: [] as string[], city: [] as string[], period: 'mtd', pre: 'tm' } })
  const qs = useMemo(() => new URLSearchParams({ from: f.from, to: f.to, basis: f.basis, property_id: f.property_id.join(','), cluster_id: f.cluster_id.join(','), city: f.city.join(','), period: f.period }).toString(), [f])
  const res = useApi<Res>(`/reports/${key}?${qs}`)
  const r = res.data
  const groups = [...new Set((cat.data || []).map((c) => c.group))]
  const rows = useMemo(() => (r?.rows || []).map((cells, i) => ({ i, cells, doc: r?.document_ids?.[i] })), [r])
  const fmt = (v: unknown, right: boolean, label: string) => typeof v === 'number' && right && !/qty|Quantity|Ordered|Received|Short|Days|Documents|Requests|Months|%|Rate ex GST|GST/i.test(label) ? inr(v) : String(v ?? '')
  return (
    <>
      <div className="head"><div><h1>Reports</h1><p className="sub">{r?.use || 'Choose a report.'}</p></div>
        <div className="acts"><button className="btn ghost" onClick={() => download(`/reports/${key}?${qs}&format=xlsx`, `report-${key}.xlsx`)}>Download Excel</button>
          <button className="btn" onClick={() => download(`/reports/pack?${qs}`, 'report-pack.xlsx')}>Download report pack (Excel)</button></div></div>
      <div className="tools">
        <select value={key} onChange={(e) => setKey(e.target.value)} aria-label="Report" style={{ maxWidth: 360 }}>
          {groups.map((g) => <optgroup key={g} label={g}>{cat.data!.filter((c) => c.group === g).map((c) => <option key={c.key} value={c.key}>{c.title}</option>)}</optgroup>)}</select>
        <MultiSelect ariaLabel="Property" value={f.property_id} onChange={(v) => setF({ ...f, property_id: v })} options={[['', 'All properties'], ...(l?.properties || []).map((p) => [String(p.id), p.name] as [string, string])]} />
        <MultiSelect ariaLabel="Cluster" value={f.cluster_id} onChange={(v) => setF({ ...f, cluster_id: v })} options={[['', 'All clusters'], ...(l?.clusters || []).map((c) => [String(c.id), c.name] as [string, string])]} />
        <MultiSelect ariaLabel="City" value={f.city} onChange={(v) => setF({ ...f, city: v })} options={[['', 'All cities'], ...(l?.cities || []).map((c) => [c.name, c.name] as [string, string])]} />
        {BUDGET.includes(key) && <select value={f.period} onChange={(e) => setF({ ...f, period: e.target.value })} aria-label="Period">
          <option value="mtd">This month to date</option><option value="lm">Last month</option><option value="m3">Last 3 months</option><option value="m6">Last 6 months</option><option value="fy">Financial year to date</option></select>}
        {!NO_DATES.includes(key) && <>
          <select value={f.basis} onChange={(e) => setF({ ...f, basis: e.target.value })} aria-label="Date basis"><option value="">By request date</option><option value="po">By PO date</option><option value="gr">By receipt date</option></select>
          <select value={f.pre} onChange={(e) => { const [from, to] = e.target.value === 'custom' ? [f.from, f.to] : preset(e.target.value); setF({ ...f, pre: e.target.value, from, to }) }} aria-label="Period">
            <option value="7">Last 7 days</option><option value="tm">This month</option><option value="lm">Last month</option><option value="fy">Financial year</option><option value="custom">Custom</option></select>
          <input type="date" value={f.from} onChange={(e) => setF({ ...f, from: e.target.value, pre: 'custom' })} aria-label="From" style={{ width: 'auto' }} />
          <input type="date" value={f.to} onChange={(e) => setF({ ...f, to: e.target.value, pre: 'custom' })} aria-label="To" style={{ width: 'auto' }} /></>}
      </div>
      <h2>{r?.title}</h2>
      {r && <DataTable screen={'report-' + key} rows={rows} rowKey={(x) => x.i} onOpen={r.document_ids?.length ? (x) => x.doc && nav('/documents/' + x.doc) : undefined}
        search={(x) => x.cells.map((c) => String(c ?? '')).join(' ')}
        columns={r.columns.map((c, j) => ({ key: c.key, label: c.label, right: c.right, render: (x: { cells: unknown[] }) => fmt(x.cells[j], c.right, c.label),
          sort: (x: { cells: unknown[] }) => (typeof x.cells[j] === 'number' ? x.cells[j] as number : String(x.cells[j] ?? '')), csv: (x: { cells: unknown[] }) => String(x.cells[j] ?? '') }))}
        csvName={'report-' + key} />}
    </>
  )
}
