// Every purchase request in the user's scope.
import { useCallback, useState } from 'react'
import { daysBack } from '../format/format'
import { useNavigate } from 'react-router-dom'
import { useLookups } from '../api/lookups'
import type { ListRow } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { DataTable } from '../ui/DataTable'
import { col, placeFilters, search } from './columns'
import { useScreen } from '../screenaccess/screen'

export function RequestsPage() {
  const nav = useNavigate()
  const l = useLookups()
  const { me, hasRight } = useSession()
  const sc = useScreen('requests')
  const [days, setDays] = useState(90)
  const fromChanged = useCallback((f: string) => setDays(daysBack(f)), [])
  const { data } = useApi<ListRow[]>(`/requests?kind=purchase&days=${days}`)
  const statusFilter = { id: 'st', label: 'Status', options: [['', 'All statuses'], ['PENDING', 'In approval'], ['RETURNED', 'Returned'], ['REJECTED', 'Rejected'], ['DONE', 'Approved and later'], ['MOD', 'Quantity changed'], ['EVT', 'Event request']] as [string, string][],
    test: (r: ListRow, v: string) => v === 'EVT' ? r.event : v === 'MOD' ? r.modified : v === 'DONE' ? !['PENDING', 'RETURNED', 'REJECTED', 'CANCELLED'].includes(r.status) : r.status === v }
  return (
    <>
      <div className="head"><div><h1>Purchase Requests</h1><p className="sub">Every request raised by the properties you can see. Open ones, and closed ones from the last 90 days.</p></div>
        <div className="acts">{sc.edit && hasRight('raise') && <button className="btn" onClick={() => nav('/requests/new')}>+ New request</button>}</div></div>
      <DataTable screen="requests" rows={data || []} rowKey={(r) => r.id} date={{ label: 'Raised', value: (r) => r.created_at, onFrom: fromChanged }} search={search} filters={[statusFilter, ...placeFilters(l)]} onOpen={(r) => nav('/documents/' + r.id)}
        columns={[col.number, col.date, col.property, col.category, col.items, col.vendor, col.requester, col.amount, col.status,
          { key: 'acts', label: '', render: (r) => r.requester_id === me?.id ? <span className="tools">
            {r.editable && <button className="link" onClick={() => nav(`/requests/${r.id}/edit`)}>Edit</button>}
            <button className="link" onClick={() => nav(`/requests/new?clone=${r.id}`)}>Clone</button></span> : null }]} csvName="purchase-requests" />
    </>
  )
}
