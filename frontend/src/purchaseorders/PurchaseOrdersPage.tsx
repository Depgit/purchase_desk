// Approved requests waiting for a PO, and POs sent to vendors.
import { useNavigate } from 'react-router-dom'
import { useLookups } from '../api/lookups'
import type { ListRow } from '../api/types'
import { useApi } from '../api/useApi'
import { DataTable } from '../ui/DataTable'
import { Pill } from '../ui/Pill'
import { col, placeFilters, search } from '../requests/columns'

export function PurchaseOrdersPage() {
  const nav = useNavigate()
  const l = useLookups()
  const { data } = useApi<ListRow[]>('/requests?view=po')
  const st = (r: ListRow) => r.status === 'APPROVED' ? <Pill tone="warn">PO pending</Pill> : r.status === 'PO_ISSUED' ? <Pill>{r.is_transfer ? 'Warehouse to dispatch' : 'Awaiting delivery'}</Pill> : <Pill tone="ok">{r.status_label}</Pill>
  return (
    <>
      <div className="head"><div><h1>Purchase Orders</h1><p className="sub">Approved requests to convert, and orders sent to vendors.</p></div></div>
      <DataTable screen="purchase-orders" rows={data || []} rowKey={(r) => r.id} search={search} onOpen={(r) => nav('/documents/' + r.id)}
        filters={[{ id: 'st', label: 'Status', options: [['', 'All'], ['APPROVED', 'PO pending'], ['PO_ISSUED', 'Awaiting delivery'], ['DONE', 'Received']], test: (r, v) => v === 'DONE' ? !['APPROVED', 'PO_ISSUED'].includes(r.status) : r.status === v }, ...placeFilters(l)]}
        columns={[col.po, col.number, col.vendor, col.property, col.deliver, col.terms, col.amount, { key: 'status', label: 'Status', render: st, csv: (r) => r.status_label }]} csvName="purchase-orders" />
    </>
  )
}
