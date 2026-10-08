// Every purchase request in the user's scope.
import { useNavigate } from 'react-router-dom'
import { useLookups } from '../api/lookups'
import type { ListRow } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { DataTable } from '../ui/DataTable'
import { col, placeFilters, search } from './columns'

export function RequestsPage() {
  const nav = useNavigate()
  const l = useLookups()
  const { hasRight } = useSession()
  const { data } = useApi<ListRow[]>('/requests?kind=purchase')
  const statusFilter = { id: 'st', label: 'Status', options: [['', 'All statuses'], ['PENDING', 'In approval'], ['RETURNED', 'Returned'], ['REJECTED', 'Rejected'], ['DONE', 'Approved and later'], ['MOD', 'Quantity changed'], ['EVT', 'Event request']] as [string, string][],
    test: (r: ListRow, v: string) => v === 'EVT' ? r.event : v === 'MOD' ? r.modified : v === 'DONE' ? !['PENDING', 'RETURNED', 'REJECTED', 'CANCELLED'].includes(r.status) : r.status === v }
  return (
    <>
      <div className="head"><div><h1>Purchase Requests</h1><p className="sub">Every request raised by the properties you can see. Open ones, and closed ones from the last 90 days.</p></div>
        <div className="acts">{hasRight('raise') && <button className="btn" onClick={() => nav('/requests/new')}>+ New request</button>}</div></div>
      <DataTable screen="requests" rows={data || []} rowKey={(r) => r.id} search={search} filters={[statusFilter, ...placeFilters(l)]} onOpen={(r) => nav('/documents/' + r.id)}
        columns={[col.number, col.date, col.property, col.category, col.items, col.vendor, col.requester, col.amount, col.status]} csvName="purchase-requests" />
    </>
  )
}
