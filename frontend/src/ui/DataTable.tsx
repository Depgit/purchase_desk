// The list used on every screen: search, filters, a date range, 25 rows per page, click a heading to sort,
// a column chooser remembered per user, tick-boxes for bulk actions, and CSV download of the filtered list.
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { get, put, saveBlob } from '../api/client'
import { addDays, thisMonth, today } from '../format/format'
import { Modal } from './Modal'
import { MultiSelect } from './MultiSelect'

export interface Column<T> {
  key: string
  label: string
  render: (r: T) => ReactNode
  sort?: (r: T) => string | number
  csv?: (r: T) => string | number
  right?: boolean
}
export interface Filter<T> {
  id: string
  label: string
  options: [string, string][]
  test: (r: T, v: string) => boolean
  /** Tick several values; a row shows when it passes the test for any of them. */
  multi?: boolean
}

/** The date a row is filtered on: a label for the picker and the row's date (YYYY-MM-DD or an ISO timestamp). */
export interface DateFilter<T> {
  label: string
  value: (r: T) => string | null | undefined
  /** Called with the first day of the chosen range ('' for any date), for lists the server limits to recent rows. */
  onFrom?: (from: string) => void
}

const RANGES: [string, string][] = [['', 'Any date'], ['today', 'Today'], ['7', 'Last 7 days'], ['30', 'Last 30 days'], ['month', 'This month'], ['last', 'Last month'], ['custom', 'Choose dates']]

// rangeOf turns a quick range into from and to days, both included.
function rangeOf(r: string, from: string, to: string): [string, string] {
  const t = today()
  if (r === 'today') return [t, t]
  if (r === '7' || r === '30') return [addDays(t, 1 - +r), t]
  if (r === 'month') return [thisMonth() + '-01', t]
  if (r === 'last') { const end = addDays(thisMonth() + '-01', -1); return [end.slice(0, 7) + '-01', end] }
  return [from, to]
}

// dayOf gives the local calendar day of a date or timestamp, so a request raised at 1 am IST counts on that day.
function dayOf(v: string): string {
  if (v.length <= 10) return v
  const d = new Date(v)
  return isNaN(d.getTime()) ? v.slice(0, 10) : d.toLocaleDateString('en-CA')
}

interface Props<T> {
  screen: string
  rows: T[]
  columns: Column<T>[]
  rowKey: (r: T) => string | number
  search?: (r: T) => string
  filters?: Filter<T>[]
  date?: DateFilter<T>
  onOpen?: (r: T) => void
  selectable?: (r: T) => boolean
  selected?: Set<string | number>
  onSelect?: (s: Set<string | number>) => void
  actions?: ReactNode
  csvName?: string
  dim?: (r: T) => boolean
  empty?: string
  pageSize?: number
  /** Sort used until the person clicks a heading (their choice is saved), for example ['sku', 1] for SKU code A to Z. */
  defaultSort?: [string, number]
}

interface Pref { cols?: string[]; sort?: [string, number] }

function csvCell(v: unknown) {
  const s = v == null ? '' : String(v)
  return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
}

export function DataTable<T>(p: Props<T>) {
  const [q, setQ] = useState('')
  const [fv, setFv] = useState<Record<string, string>>({})
  const [range, setRange] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [page, setPage] = useState(0)
  const [pref, setPref] = useState<Pref>({})
  const [chooser, setChooser] = useState(false)
  const size = p.pageSize || 25
  const onFrom = p.date?.onFrom
  useEffect(() => { onFrom?.(range ? rangeOf(range, from, to)[0] : '') }, [range, from, to, onFrom])

  useEffect(() => {
    get<Pref>('/prefs/list-' + p.screen).then((x) => setPref(x || {})).catch(() => {})
  }, [p.screen])
  const savePref = (next: Pref) => { setPref(next); put('/prefs/list-' + p.screen, next).catch(() => {}) }

  const shown = useMemo(() => {
    const order = pref.cols?.length ? pref.cols.map((k) => p.columns.find((c) => c.key === k)).filter(Boolean) as Column<T>[] : p.columns
    return order
  }, [pref.cols, p.columns])

  const filtered = useMemo(() => {
    let rows = p.rows
    for (const f of p.filters || []) {
      const v = fv[f.id]
      if (!v) continue
      const vs = f.multi ? v.split(',') : [v]
      rows = rows.filter((r) => vs.some((x) => f.test(r, x)))
    }
    if (p.date && range) {
      const [a, b] = rangeOf(range, from, to)
      const dv = p.date.value
      rows = rows.filter((r) => { const v = dv(r); if (!v) return false; const d = dayOf(v); return (!a || d >= a) && (!b || d <= b) })
    }
    if (q && p.search) {
      const s = q.toLowerCase()
      rows = rows.filter((r) => p.search!(r).toLowerCase().includes(s))
    }
    const sort = pref.sort || p.defaultSort
    if (sort) {
      const c = p.columns.find((x) => x.key === sort[0])
      if (c) {
        const val = (r: T) => (c.sort ? c.sort(r) : c.csv ? c.csv(r) : '')
        const dir = sort[1]
        rows = [...rows].sort((a, b) => {
          const x = val(a), y = val(b)
          return (typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y), 'en', { numeric: true })) * dir
        })
      }
    }
    return rows
  }, [p.rows, p.filters, p.search, p.columns, p.date, range, from, to, fv, q, pref.sort, p.defaultSort])

  const pages = Math.max(1, Math.ceil(filtered.length / size))
  const pg = Math.min(page, pages - 1)
  const slice = filtered.slice(pg * size, (pg + 1) * size)
  const selectableIds = p.selectable ? filtered.filter(p.selectable).map(p.rowKey) : []
  const allTicked = selectableIds.length > 0 && selectableIds.every((id) => p.selected?.has(id))

  function toggle(id: string | number) {
    const s = new Set(p.selected)
    s.has(id) ? s.delete(id) : s.add(id)
    p.onSelect?.(s)
  }
  function exportCSV() {
    const cols = shown
    const lines = [cols.map((c) => csvCell(c.label)).join(',')]
    for (const r of filtered) lines.push(cols.map((c) => csvCell(c.csv ? c.csv(r) : c.sort ? c.sort(r) : '')).join(','))
    saveBlob(new Blob(['\uFEFF' + lines.join('\n')], { type: 'text/csv' }), (p.csvName || p.screen) + '.csv')
  }
  return (
    <>
      <div className="tools">
        {p.search && <input type="search" placeholder="Search" aria-label="Search" value={q} onChange={(e) => { setQ(e.target.value); setPage(0) }} />}
        {(p.filters || []).map((f) => f.multi ? (
          <MultiSelect key={f.id} ariaLabel={f.label} options={f.options} value={fv[f.id] ? fv[f.id].split(',') : []} onChange={(vs) => { setFv({ ...fv, [f.id]: vs.join(',') }); setPage(0) }} />
        ) : (
          <select key={f.id} aria-label={f.label} value={fv[f.id] || ''} onChange={(e) => { setFv({ ...fv, [f.id]: e.target.value }); setPage(0) }}>
            {f.options.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
          </select>
        ))}
        {p.date && <>
          <select aria-label={p.date.label} title={p.date.label} value={range} onChange={(e) => { setRange(e.target.value); setPage(0) }}>
            {RANGES.map(([v, l]) => <option key={v} value={v}>{v ? l : p.date!.label + ': any'}</option>)}
          </select>
          {range === 'custom' && <>
            <input type="date" aria-label="From" value={from} max={to || undefined} onChange={(e) => { setFrom(e.target.value); setPage(0) }} style={{ width: 'auto' }} />
            <input type="date" aria-label="To" value={to} min={from || undefined} onChange={(e) => { setTo(e.target.value); setPage(0) }} style={{ width: 'auto' }} />
          </>}
        </>}
        <span style={{ flex: 1 }} />
        {p.actions}
        <button className="btn ghost" onClick={() => setChooser(true)}>Columns{pref.cols?.length ? ` (${pref.cols.length} of ${p.columns.length})` : ''}</button>
        <button className="btn ghost" onClick={exportCSV}>Download CSV ({filtered.length})</button>
      </div>
      <section className="card flush">
        {filtered.length ? (
          <div className="scroll"><table>
            <thead><tr>
              {p.selectable && <th><input type="checkbox" aria-label="Select all" checked={allTicked} onChange={() => {
                const s = new Set(p.selected)
                selectableIds.forEach((id) => (allTicked ? s.delete(id) : s.add(id)))
                p.onSelect?.(s)
              }} /></th>}
              {shown.map((c) => (
                <th key={c.key} className={c.right ? 'r' : ''}>
                  {c.label ? <button className="thb" onClick={() => { const cur = pref.sort || p.defaultSort; savePref({ ...pref, sort: cur && cur[0] === c.key ? [c.key, -cur[1]] : [c.key, 1] }) }}>
                    {c.label}{(pref.sort || p.defaultSort)?.[0] === c.key ? ((pref.sort || p.defaultSort)![1] > 0 ? ' ▲' : ' ▼') : ''}</button> : null}
                </th>
              ))}
            </tr></thead>
            <tbody>
              {slice.map((r) => {
                const id = p.rowKey(r)
                return (
                  <tr key={id} className={(p.onOpen ? 'click ' : '') + (p.dim?.(r) ? 'dim' : '')} onClick={p.onOpen ? (e) => {
                    if ((e.target as HTMLElement).closest('button,a,input,select,label')) return
                    p.onOpen!(r)
                  } : undefined}>
                    {p.selectable && <td>{p.selectable(r) && <input type="checkbox" aria-label={'Select ' + id} checked={!!p.selected?.has(id)} onChange={() => toggle(id)} />}</td>}
                    {shown.map((c, i) => <td key={c.key} className={(c.right ? 'r ' : '') + (i === 0 ? 'id' : '')}>{c.render(r)}</td>)}
                  </tr>
                )
              })}
            </tbody>
          </table></div>
        ) : <p className="empty">{p.empty || 'Nothing matches.'}</p>}
        <div className="pager">
          <span>{filtered.length ? `${pg * size + 1} to ${Math.min(filtered.length, (pg + 1) * size)} of ${filtered.length}` : '0 rows'}</span>
          <span className="tools">
            <button className="btn ghost small" disabled={pg === 0} onClick={() => setPage(pg - 1)}>Previous</button>
            <button className="btn ghost small" disabled={pg >= pages - 1} onClick={() => setPage(pg + 1)}>Next</button>
          </span>
        </div>
      </section>
      {chooser && <ColumnChooser columns={p.columns} chosen={pref.cols?.length ? pref.cols : p.columns.map((c) => c.key)}
        onClose={() => setChooser(false)} onSave={(cols) => { savePref({ ...pref, cols }); setChooser(false) }} onReset={() => { savePref({}); setChooser(false) }} />}
    </>
  )
}

function ColumnChooser<T>({ columns, chosen, onClose, onSave, onReset }: { columns: Column<T>[]; chosen: string[]; onClose: () => void; onSave: (c: string[]) => void; onReset: () => void }) {
  const [order, setOrder] = useState<string[]>([...chosen, ...columns.map((c) => c.key).filter((k) => !chosen.includes(k))])
  const [on, setOn] = useState<string[]>(chosen)
  const label = (k: string) => columns.find((c) => c.key === k)?.label || k
  const move = (i: number, d: number) => {
    const j = i + d
    if (j < 0 || j >= order.length) return
    const o = [...order];[o[i], o[j]] = [o[j], o[i]]
    setOrder(o)
  }
  return (
    <Modal title="Columns for this list" onClose={onClose}>
      <p className="sub">Tick the columns you want and move them into your order. Saved for you.</p>
      <div>{order.map((k, i) => (
        <div className="colrow" key={k}>
          <label className="ck"><input type="checkbox" checked={on.includes(k)} onChange={() => setOn(on.includes(k) ? on.filter((x) => x !== k) : [...on, k])} />{label(k) || '(actions)'}</label>
          <span className="tools"><button className="btn ghost small" onClick={() => move(i, -1)} disabled={!i} aria-label="Move up">▲</button>
            <button className="btn ghost small" onClick={() => move(i, 1)} disabled={i === order.length - 1} aria-label="Move down">▼</button></span>
        </div>))}
      </div>
      <div className="btns">
        <button className="btn ghost" onClick={onReset}>Reset to default</button>
        <button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={!on.length} onClick={() => onSave(order.filter((k) => on.includes(k)))}>Save</button>
      </div>
    </Modal>
  )
}
