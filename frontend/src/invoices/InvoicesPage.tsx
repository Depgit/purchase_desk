// Vendor invoices against goods receipts, for Accounts Payable's three-way match. Several in-tolerance invoices can be matched at once.
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { post } from '../api/client'
import { useSession } from '../auth/session'
import { BulkResults, type BulkResult } from '../ui/BulkResults'
import { Errors } from '../ui/Errors'
import { useAction } from '../ui/useAction'
import type { Invoice } from '../api/types'
import { useApi } from '../api/useApi'
import { day, inr } from '../format/format'
import { DataTable } from '../ui/DataTable'
import { Pill } from '../ui/Pill'

export function InvoicesPage() {
  const nav = useNavigate()
  const { hasStep } = useSession()
  const ap = hasStep('ap')
  const { data, reload } = useApi<Invoice[]>('/invoices')
  const rows = (data || []).filter((i) => i.kind === 'purchase' || i.kind === 'corp_purchase')
  const [sel, setSel] = useState<Set<string | number>>(new Set())
  const [results, setResults] = useState<BulkResult[] | null>(null)
  const a = useAction()
  return (
    <>
      <div className="head"><div><h1>Invoices</h1><p className="sub">Three-way match of the PO, the goods receipt and the vendor invoice. Each delivery has its own invoice and match. Tick invoices that match within the tolerance to confirm them together; a mismatch needs its own remark.</p></div>
        {ap && <div className="acts"><button className="btn" disabled={!sel.size || a.busy} onClick={() => a.run(() => post<{ done: number; failed: number; results: BulkResult[] }>('/invoices/bulk', { action: 'match', ids: [...sel] }),
          (r) => { setResults(r.results); setSel(new Set()); reload(); return `${r.done} matched and sent to Finance${r.failed ? `, ${r.failed} not matched` : ''}` })}>Match selected ({sel.size})</button></div>}</div>
      <Errors list={a.errors} />
      {results && <BulkResults title="Last bulk match" results={results} label={(id) => rows.find((r) => r.id === id)?.invoice_number || String(id)} onClose={() => setResults(null)} />}
      <DataTable screen="invoices" rows={rows} rowKey={(r) => r.id} date={{ label: 'Invoice date', value: (r) => r.invoice_date }} onOpen={(r) => nav('/invoices/' + r.id)}
        selectable={ap ? (r) => r.status === 'RECEIVED' && r.within_tolerance : undefined} selected={sel} onSelect={setSel}
        search={(r) => [r.invoice_number, r.voucher_number, r.vendor, r.po_number, r.document_number, r.property].join(' ')}
        filters={[{ id: 'st', label: 'Match', options: [['', 'All'], ['RECEIVED', 'To match'], ['DONE', 'Matched']], test: (r, v) => v === 'RECEIVED' ? r.status === 'RECEIVED' : r.status !== 'RECEIVED' }]}
        columns={[
          { key: 'pv', label: 'Voucher', render: (r) => r.voucher_number, sort: (r) => r.id, csv: (r) => r.voucher_number },
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
