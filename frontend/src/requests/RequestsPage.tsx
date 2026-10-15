// Every purchase request in the user's scope, and the utility and AMC bills (raised from "+ Bill"; no PO or receipt).
import { useCallback, useMemo, useState } from 'react'
import { daysBack, month } from '../format/format'
import { useNavigate } from 'react-router-dom'
import { useLookups } from '../api/lookups'
import type { ListRow } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { DataTable } from '../ui/DataTable'
import { col, placeFilters, search } from './columns'
import { useScreen } from '../screenaccess/screen'
import { DraftList } from '../formdrafts/drafts'

const kindOf = (r: ListRow) => r.kind === 'bill' ? r.bill_section : 'purchase'

export function RequestsPage() {
  const nav = useNavigate()
  const l = useLookups()
  const { me, hasRight } = useSession()
  const sc = useScreen('requests')
  const util = useScreen('bills_utility')
  const amc = useScreen('bills_amc')
  const [days, setDays] = useState(90)
  const fromChanged = useCallback((f: string) => setDays(daysBack(f)), [])
  const { data } = useApi<ListRow[]>(`/requests?kind=purchase&days=${days}`)
  const { data: ub } = useApi<ListRow[]>(util.view ? `/requests?kind=bill&section=utility&days=${days}` : null)
  const { data: ab } = useApi<ListRow[]>(amc.view ? `/requests?kind=bill&section=amc&days=${days}` : null)
  const rows = useMemo(() => [...(data || []), ...(ub || []), ...(ab || [])].sort((a, b) => b.created_at.localeCompare(a.created_at)), [data, ub, ab])
  const hasBills = util.view || amc.view
  const billEdit = util.edit || amc.edit
  const statusFilter = { id: 'st', label: 'Status', options: [['', 'All statuses'], ['PENDING', 'In approval'], ['RETURNED', 'Returned'], ['REJECTED', 'Rejected'], ['DONE', 'Approved and later'], ['MOD', 'Quantity changed'], ['EVT', 'Event request']] as [string, string][],
    test: (r: ListRow, v: string) => v === 'EVT' ? r.event : v === 'MOD' ? r.modified : v === 'DONE' ? !['PENDING', 'RETURNED', 'REJECTED', 'CANCELLED'].includes(r.status) : r.status === v }
  const typeFilter = { id: 'kind', label: 'Type', options: [['', 'Requests and bills'], ['purchase', 'Purchase requests'], ['utility', 'Utility bills'], ['amc', 'AMC bills']] as [string, string][],
    test: (r: ListRow, v: string) => kindOf(r) === v }
  return (
    <>
      <div className="head"><div><h1>Purchase Requests</h1><p className="sub">Every request{hasBills ? ', utility bill and AMC bill' : ''} raised by the properties you can see. Open ones, and closed ones from the last 90 days.</p></div>
        <div className="acts">
          {billEdit && hasRight('raise') && <button className="btn ghost" onClick={() => nav('/bills/new/' + (util.edit ? 'utility' : 'amc'))}>+ New bill</button>}
          {sc.edit && hasRight('raise') && <button className="btn" onClick={() => nav('/requests/new')}>+ New request</button>}</div></div>
      {sc.edit && hasRight('raise') && <DraftList form="request" label="Your saved drafts, not sent yet" onOpen={(d) => nav(`/requests/new?draft=${d.id}`)} />}
      {billEdit && <DraftList form="bill" label="Your bill drafts" onOpen={(d) => nav(`/bills/new/${d.fields.section}?draft=${d.id}`)} />}
      <DataTable screen="requests" rows={rows} rowKey={(r) => r.id} date={{ label: 'Raised', value: (r) => r.created_at, onFrom: fromChanged }} search={search}
        filters={[...(hasBills ? [typeFilter] : []), statusFilter, ...placeFilters(l)]} onOpen={(r) => nav('/documents/' + r.id)}
        columns={[col.number, col.date, col.property, col.category,
          { ...col.items, render: (r) => r.kind === 'bill' ? <>{r.items} <span className="hint">· {r.bill_section === 'amc' ? 'AMC' : 'utility'} bill, {month(r.period_start)}{r.months > 1 ? ` · ${r.months} months` : ''}</span></> : r.items },
          col.vendor, col.requester, col.amount, col.status,
          { key: 'acts', label: '', render: (r) => r.requester_id === me?.id ? <span className="tools">
            {r.kind === 'purchase' && r.editable && <button className="link" onClick={() => nav(`/requests/${r.id}/edit`)}>Edit</button>}
            <button className="link" onClick={() => nav(r.kind === 'bill' ? `/bills/new/${r.bill_section}?clone=${r.id}` : `/requests/new?clone=${r.id}`)}>Clone</button></span> : null }]} csvName="purchase-requests" />
    </>
  )
}
