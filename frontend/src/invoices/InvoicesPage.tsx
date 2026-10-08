// Vendor invoices against goods receipts, for Accounts Payable's three-way match.
import { useNavigate } from 'react-router-dom'
import type { Invoice } from '../api/types'
import { useApi } from '../api/useApi'
import { day, inr } from '../format/format'
import { DataTable } from '../ui/DataTable'
import { Pill } from '../ui/Pill'

export function InvoicesPage() {
  const nav = useNavigate()
  const { data } = useApi<Invoice[]>('/invoices')
  const rows = (data || []).filter((i) => i.kind === 'purchase' || i.kind === 'corp_purchase')
  return (
    <>
      <div className="head"><div><h1>Invoices</h1><p className="sub">Three-way match of the PO, the goods receipt and the vendor invoice. Each delivery has its own invoice and match.</p></div></div>
      <DataTable screen="invoices" rows={rows} rowKey={(r) => r.id} onOpen={(r) => nav('/invoices/' + r.id)}
        search={(r) => [r.invoice_number, r.vendor, r.po_number, r.document_number, r.property].join(' ')}
        filters={[{ id: 'st', label: 'Match', options: [['', 'All'], ['RECEIVED', 'To match'], ['DONE', 'Matched']], test: (r, v) => v === 'RECEIVED' ? r.status === 'RECEIVED' : r.status !== 'RECEIVED' }]}
        columns={[
          { key: 'inv', label: 'Invoice', render: (r) => r.invoice_number, csv: (r) => r.invoice_number },
          { key: 'date', label: 'Invoice date', render: (r) => day(r.invoice_date), sort: (r) => r.invoice_date, csv: (r) => r.invoice_date },
          { key: 'vendor', label: 'Vendor', render: (r) => r.vendor, csv: (r) => r.vendor },
          { key: 'po', label: 'PO · receipt', render: (r) => `${r.po_number} · ${r.grn_number}`, csv: (r) => r.po_number },
          { key: 'property', label: 'Property', render: (r) => r.property, csv: (r) => r.property },
          { key: 'rcv', label: 'Received value', right: true, render: (r) => inr(r.received_value), sort: (r) => r.received_value, csv: (r) => r.received_value },
          { key: 'amt', label: 'Invoice amount', right: true, render: (r) => inr(r.amount), sort: (r) => r.amount, csv: (r) => r.amount },
          { key: 'match', label: 'Match', render: (r) => r.status === 'RECEIVED' ? (r.within_tolerance ? <Pill tone="warn">To confirm</Pill> : <Pill tone="bad">Mismatch {inr(r.difference)}</Pill>) : <Pill tone="ok">Matched</Pill>, csv: (r) => r.status },
        ]} csvName="invoices" />
    </>
  )
}
