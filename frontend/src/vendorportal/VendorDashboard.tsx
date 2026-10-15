// The vendor's home page: what waits for it (new orders, deliveries, bills being checked, payments due) and its
// statement with Saltstayz: every bill, TDS held, payment and advance with the running balance, searchable by text,
// type, property and period, and downloadable.
import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { saveBlob } from '../api/client'
import { day, inr } from '../format/format'
import { MultiSelect } from '../ui/MultiSelect'
import { Pill } from '../ui/Pill'
import { vget } from './portalApi'

interface Entry { date: string; kind: 'bill' | 'tds' | 'payment' | 'advance' | 'refund'; text: string; ref: string; po_number: string; property: string; bill: number; paid: number; balance: number; status: string }
interface Dash {
  new_orders: number; to_deliver: number; checking: number; checking_amount: number; approved: number; next_pay_by: string; paid_this_month: number; balance: number
  waiting: { id: number; po_number: string; po_date: string; property: string; total: number }[]
  statement: Entry[]
}

const KINDS: [string, string][] = [['', 'Bills and payments'], ['bill', 'Bills'], ['payment', 'Payments'], ['tds', 'TDS held'], ['advance', 'Advances paid'], ['refund', 'Advances returned']]
const PERIODS: [string, string][] = [['', 'All dates'], ['month', 'This month'], ['last', 'Last month'], ['3m', 'Last 3 months'], ['fy', 'This financial year'], ['custom', 'Choose dates']]

const ymd = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`

// periodOf turns a period into from and to days, both included ('' = open).
function periodOf(p: string, from: string, to: string): [string, string] {
  const now = new Date()
  const y = now.getFullYear(), m = now.getMonth()
  switch (p) {
    case 'month': return [ymd(new Date(y, m, 1)), '']
    case 'last': return [ymd(new Date(y, m - 1, 1)), ymd(new Date(y, m, 0))]
    case '3m': return [ymd(new Date(y, m - 2, 1)), '']
    case 'fy': return [ymd(new Date(m >= 3 ? y : y - 1, 3, 1)), ''] // April to March
    case 'custom': return [from, to]
  }
  return ['', '']
}

function csvCell(v: unknown) {
  const s = String(v ?? '')
  return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
}

export function VendorDashboard() {
  const nav = useNavigate()
  const [d, setD] = useState<Dash | null>(null)
  const [error, setError] = useState('')
  const [q, setQ] = useState('')
  const [kinds, setKinds] = useState<string[]>([])
  const [props, setProps] = useState<string[]>([])
  const [period, setPeriod] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [oldestFirst, setOldestFirst] = useState(false)
  useEffect(() => { vget<Dash>('/dashboard').then(setD).catch((e) => setError(e.message)) }, [])

  const st = d?.statement || []
  const propOptions = useMemo(() => [['', 'All properties'], ...[...new Set(st.map((e) => e.property))].sort().map((p) => [p, p])] as [string, string][], [st])
  const [f, t] = periodOf(period, from, to)
  const view = useMemo(() => {
    const needle = q.trim().toLowerCase()
    // Opening balance: the whole account up to the day before the period; closing: up to its last day.
    const before = f ? st.filter((e) => e.date < f) : []
    const inPeriod = st.filter((e) => (!f || e.date >= f) && (!t || e.date <= t))
    const rows = inPeriod.filter((e) => (!kinds.length || kinds.includes(e.kind)) && (!props.length || props.includes(e.property))
      && (!needle || [e.text, e.ref, e.po_number, e.property, e.status].join(' ').toLowerCase().includes(needle)))
    return {
      opening: before.length ? before[before.length - 1].balance : 0,
      closing: inPeriod.length ? inPeriod[inPeriod.length - 1].balance : (before.length ? before[before.length - 1].balance : 0),
      rows: oldestFirst ? rows : [...rows].reverse(),
      bills: rows.reduce((s, e) => s + e.bill, 0),
      paid: rows.reduce((s, e) => s + e.paid, 0),
    }
  }, [st, q, kinds, props, f, t, oldestFirst])

  if (error) return <p className="note bad">{error}</p>
  if (!d) return <p className="sub">Loading…</p>
  const filtered = !!(q || kinds.length || props.length)

  function download() {
    const lines = [['Date', 'Bill / payment', 'Reference', 'PO', 'Property', 'Bill amount', 'Payment amount', 'Balance', 'Status'].join(',')]
    if (f) lines.push([f, 'Opening balance', '', '', '', '', '', view.opening, ''].map(csvCell).join(','))
    const rows = oldestFirst ? view.rows : [...view.rows].reverse()
    for (const e of rows) lines.push([e.date, e.text, e.ref, e.po_number, e.property, e.bill || '', e.paid || '', e.balance, e.status].map(csvCell).join(','))
    saveBlob(new Blob(['﻿' + lines.join('\n')], { type: 'text/csv' }), 'saltstayz-statement.csv')
  }

  return (
    <>
      <div className="head"><div><h1>Dashboard</h1><p className="sub">What waits for you, and your account with Saltstayz.</p></div></div>
      <div className="tiles">
        <button className="tile" onClick={() => nav('/vendor/orders')}><span>New orders to accept</span><b>{d.new_orders}</b></button>
        <button className="tile" onClick={() => nav('/vendor/orders')}><span>Orders to deliver</span><b>{d.to_deliver}</b></button>
        <button className="tile" onClick={() => nav('/vendor/payments')}><span>Bills being checked</span><b>{d.checking}</b><small>{inr(d.checking_amount)}</small></button>
        <button className="tile" onClick={() => nav('/vendor/payments')}><span>Approved, to be paid</span><b>{inr(d.approved)}</b>{d.next_pay_by && <small>next by {day(d.next_pay_by)}</small>}</button>
        <div className="tile"><span>Paid this month</span><b>{inr(d.paid_this_month)}</b></div>
        <div className="tile"><span>{d.balance < 0 ? 'Advance not yet billed' : 'Balance due to you'}</span><b>{inr(Math.abs(d.balance))}</b></div>
      </div>

      {d.waiting.length > 0 && <section className="card flush"><h2>New orders waiting for you</h2><div className="scroll"><table className="dt">
        <thead><tr><th>PO</th><th>Date</th><th>Property</th><th className="r">Value</th><th></th></tr></thead>
        <tbody>{d.waiting.map((o) => <tr key={o.id}>
          <td data-label="PO" className="id">{o.po_number}</td><td data-label="Date">{day(o.po_date)}</td><td data-label="Property">{o.property}</td>
          <td data-label="Value" className="r">{inr(o.total)}</td>
          <td data-label=""><button className="btn small" onClick={() => nav('/vendor/orders/' + o.id)}>Open</button></td></tr>)}</tbody>
      </table></div></section>}

      <h2 style={{ margin: '6px 0 0' }}>Statement</h2>
      <div className="tools">
        <input type="search" placeholder="Search bill, PO, reference, property" aria-label="Search" value={q} onChange={(e) => setQ(e.target.value)} />
        <MultiSelect ariaLabel="Type" options={KINDS} value={kinds} onChange={setKinds} />
        {propOptions.length > 2 && <MultiSelect ariaLabel="Property" options={propOptions} value={props} onChange={setProps} />}
        <select aria-label="Period" value={period} onChange={(e) => setPeriod(e.target.value)}>
          {PERIODS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </select>
        {period === 'custom' && <>
          <input type="date" aria-label="From" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} style={{ width: 'auto' }} />
          <input type="date" aria-label="To" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} style={{ width: 'auto' }} />
        </>}
        <span style={{ flex: 1 }} />
        <button className="btn ghost" onClick={() => setOldestFirst(!oldestFirst)}>{oldestFirst ? 'Oldest first' : 'Newest first'}</button>
        <button className="btn ghost" onClick={download}>Download CSV</button>
      </div>
      <section className="card flush">
        {!st.length ? <p className="sub" style={{ padding: 16 }}>No bills or payments yet.</p> : <div className="scroll"><table className="dt">
          <thead><tr><th>Date</th><th>Bill / payment</th><th className="r">Bill amount</th><th className="r">Payment amount</th><th className="r">Balance</th></tr></thead>
          <tbody>
            {f && !oldestFirst && <Closing date={t} balance={view.closing} />}
            {f && oldestFirst && <Opening date={f} balance={view.opening} />}
            {view.rows.map((e, i) => <tr key={i}>
              <td data-label="Date">{day(e.date)}</td>
              <td data-label="Bill / payment"><b>{e.text}</b>
                <br /><span className="hint">{[e.po_number, e.property, e.kind === 'payment' || e.kind === 'advance' || e.kind === 'refund' ? e.ref && 'Ref ' + e.ref : ''].filter(Boolean).join(' · ')}</span>
                {e.kind === 'bill' && <> <Pill tone={e.status === 'Paid' ? 'ok' : e.status.startsWith('Approved') ? 'warn' : ''}>{e.status}</Pill></>}</td>
              <td data-label="Bill amount" className="r">{e.bill ? inr(e.bill) : '—'}</td>
              <td data-label="Payment amount" className="r">{e.paid ? inr(e.paid) : '—'}</td>
              <td data-label="Balance" className="r"><b>{inr(e.balance)}</b></td></tr>)}
            {f && oldestFirst && <Closing date={t} balance={view.closing} />}
            {f && !oldestFirst && <Opening date={f} balance={view.opening} />}
            {!view.rows.length && <tr><td colSpan={5} data-label="" className="sub">Nothing matches these filters.</td></tr>}
          </tbody>
          <tfoot><tr><td colSpan={2} data-label=""><b>Total{filtered || f ? ' of what is shown' : ''}</b></td>
            <td data-label="Bills" className="r"><b>{inr(view.bills)}</b></td><td data-label="Payments" className="r"><b>{inr(view.paid)}</b></td>
            <td data-label="Balance now" className="r"><b>{inr(d.balance)}</b></td></tr></tfoot>
        </table></div>}
      </section>
      <p className="hint">Balance is what Saltstayz owes you across your whole account, after each entry: bills add to it; TDS held, payments and advances take from it. A minus balance is an advance not yet billed.</p>
    </>
  )
}

function Opening({ date, balance }: { date: string; balance: number }) {
  return <tr className="muted"><td data-label="Date">{day(date)}</td><td data-label=""><i>Opening balance</i></td><td data-label="" /><td data-label="" /><td data-label="Balance" className="r"><b>{inr(balance)}</b></td></tr>
}

function Closing({ date, balance }: { date: string; balance: number }) {
  return <tr className="muted"><td data-label="Date">{date ? day(date) : 'Today'}</td><td data-label=""><i>Closing balance</i></td><td data-label="" /><td data-label="" /><td data-label="Balance" className="r"><b>{inr(balance)}</b></td></tr>
}
