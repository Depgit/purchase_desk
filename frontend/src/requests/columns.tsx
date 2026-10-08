// Columns and filters shared by the request, PO and goods receipt lists.
import type { ListRow } from '../api/types'
import type { Lookups } from '../api/lookups'
import type { Column, Filter } from '../ui/DataTable'
import { day, inr } from '../format/format'
import { Pill, statusTone } from '../ui/Pill'

export const search = (r: ListRow) => [r.number, r.po_number, r.property, r.vendor, r.items, r.requester, r.category, r.status_label].join(' ')

export function placeFilters(l: Lookups | null): Filter<ListRow>[] {
  return [
    { id: 'city', label: 'City', options: [['', 'All cities'], ...(l?.cities || []).map((c) => [c.name, c.name] as [string, string])], test: (r, v) => r.city === v },
    { id: 'cluster', label: 'Cluster', options: [['', 'All clusters'], ...(l?.clusters || []).map((c) => [String(c.id), c.name] as [string, string])], test: (r, v) => String(r.cluster_id) === v },
    { id: 'cat', label: 'Category', options: [['', 'All categories'], ...(l?.categories || []).map((c) => [c.name, c.name] as [string, string])], test: (r, v) => r.category === v },
  ]
}

export const col = {
  number: { key: 'number', label: 'Request', render: (r: ListRow) => r.number, sort: (r: ListRow) => r.id, csv: (r: ListRow) => r.number } as Column<ListRow>,
  po: { key: 'po', label: 'PO', render: (r: ListRow) => r.po_number || 'To be issued', csv: (r: ListRow) => r.po_number } as Column<ListRow>,
  date: { key: 'date', label: 'Date', render: (r: ListRow) => day(r.created_at), sort: (r: ListRow) => r.created_at, csv: (r: ListRow) => r.created_at.slice(0, 10) } as Column<ListRow>,
  property: { key: 'property', label: 'Property', render: (r: ListRow) => r.property, csv: (r: ListRow) => r.property } as Column<ListRow>,
  category: { key: 'category', label: 'Category', render: (r: ListRow) => r.category, csv: (r: ListRow) => r.category } as Column<ListRow>,
  items: { key: 'items', label: 'Items', render: (r: ListRow) => r.items, csv: (r: ListRow) => r.items } as Column<ListRow>,
  vendor: { key: 'vendor', label: 'Vendor', render: (r: ListRow) => <>{r.vendor}{r.vendor_msme && <> <Pill tone="warn">MSME</Pill></>}</>, csv: (r: ListRow) => r.vendor } as Column<ListRow>,
  requester: { key: 'requester', label: 'Raised by', render: (r: ListRow) => r.requester, csv: (r: ListRow) => r.requester } as Column<ListRow>,
  amount: { key: 'amount', label: 'Amount', right: true, render: (r: ListRow) => inr(r.amount), sort: (r: ListRow) => r.amount, csv: (r: ListRow) => r.amount } as Column<ListRow>,
  deliver: { key: 'deliver', label: 'Deliver by', render: (r: ListRow) => day(r.deliver_by), sort: (r: ListRow) => r.deliver_by, csv: (r: ListRow) => r.deliver_by } as Column<ListRow>,
  terms: { key: 'terms', label: 'Terms', render: (r: ListRow) => r.payment_terms || '—', csv: (r: ListRow) => r.payment_terms } as Column<ListRow>,
  status: { key: 'status', label: 'Status', render: (r: ListRow) => <><Pill tone={statusTone(r.status)}>{r.status_label}</Pill>{r.modified && <> <Pill tone="warn">Qty changed</Pill></>}{r.event && <> <Pill>Event</Pill></>}</>, sort: (r: ListRow) => r.status_label, csv: (r: ListRow) => r.status_label } as Column<ListRow>,
}
