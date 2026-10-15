// POs to receive at the property, and what has been received. Opening one gives the phone-friendly receipt screen.
// Goods receipts saved as drafts are listed on top, to open on their PO and finish.
import { useCallback, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useLookups } from '../api/lookups'
import type { ListRow } from '../api/types'
import { useApi } from '../api/useApi'
import { inr, daysBack } from '../format/format'
import { DraftList } from '../formdrafts/drafts'
import { DataTable } from '../ui/DataTable'
import { Pill } from '../ui/Pill'
import { col, placeFilters, search } from '../requests/columns'

export function GoodsReceiptsPage() {
  const nav = useNavigate()
  const l = useLookups()
  const [days, setDays] = useState(90)
  const fromChanged = useCallback((f: string) => setDays(daysBack(f)), [])
  const { data } = useApi<ListRow[]>(`/requests?view=gr&days=${days}`)
  const st = (r: ListRow) => r.status === 'PO_ISSUED' || r.status === 'WH_SENT'
    ? <Pill tone="warn">{r.received_value > 0 ? 'Part received, more due' : 'To receive'}</Pill>
    : <Pill tone="ok">{r.po_closed_short ? 'Received, closed short' : 'Received'}</Pill>
  return (
    <>
      <div className="head"><div><h1>Goods Receipts</h1><p className="sub">Open a PO to record what arrived. This screen works on a phone at the receiving point.</p></div></div>
      <DraftList form="grn" label="Your saved goods receipts, not submitted yet" onOpen={(d) => d.ref_id && nav('/documents/' + d.ref_id)} />
      <DataTable screen="goods-receipts" rows={data || []} rowKey={(r) => r.id} date={{ label: 'Raised', value: (r) => r.created_at, onFrom: fromChanged }} search={search} onOpen={(r) => nav('/documents/' + r.id)}
        filters={[{ id: 'st', label: 'Status', options: [['', 'All'], ['OPEN', 'To receive'], ['DONE', 'Received']], test: (r, v) => (v === 'OPEN') === ['PO_ISSUED', 'WH_SENT'].includes(r.status) }, ...placeFilters(l)]}
        columns={[col.po, col.vendor, col.property, col.deliver, { key: 'rcv', label: 'Received value', right: true, render: (r) => inr(r.received_value), sort: (r) => r.received_value, csv: (r) => r.received_value }, col.amount,
          { key: 'status', label: 'Status', render: st, csv: (r) => r.status_label }]} csvName="goods-receipts" />
    </>
  )
}
