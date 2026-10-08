// Finance: approve matched invoices and approved bills for payment, push them to Zoho Books, and follow payment.
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { post } from '../api/client'
import type { Invoice } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { day, inr } from '../format/format'
import { DataTable } from '../ui/DataTable'
import { Errors } from '../ui/Errors'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'
import { ZohoPreview } from './ZohoPreview'
import { PHASE, usePhase } from '../releasephase/phase'

const LABEL: Record<string, [string, '' | 'ok' | 'warn' | 'bad']> = {
  MATCHED: ['Payment approval', ''], PAY_APPROVED: ['Ready for Zoho', ''], PUSHED: ['In Zoho, to be paid', 'warn'], PAID: ['Paid', 'ok'],
}

export function PaymentsPage() {
  const nav = useNavigate()
  const { hasStep, me } = useSession()
  const fin = hasStep('finance')
  const zoho = usePhase().on(PHASE.zoho) // before Zoho opens, payment ends at "payment approved" and is marked paid by hand
  const { data, reload } = useApi<Invoice[]>('/invoices?status=MATCHED,PAY_APPROVED,PUSHED,PAID')
  const [sel, setSel] = useState<Set<string | number>>(new Set())
  const [preview, setPreview] = useState<number | null>(null)
  const [paid, setPaid] = useState<Invoice | null>(null)
  const [results, setResults] = useState<{ invoice_id: number; ok: boolean; message: string }[] | null>(null)
  const a = useAction()
  const rows = data || []
  const overdue = (r: Invoice) => r.status !== 'PAID' && r.pay_by && r.pay_by < new Date().toISOString().slice(0, 10)
  return (
    <>
      <div className="head"><div><h1>Payments</h1><p className="sub">{zoho ? 'Approve matched invoices and approved bills, push them to Zoho, and follow payment. Payment status is read back from Zoho.' : 'Approve matched invoices for payment. Once the bank transfer is done, the System Admin marks the invoice paid.'}</p></div>
        <div className="acts">{fin && zoho && <>
          <button className="btn ghost" disabled={a.busy} onClick={() => a.run(() => post('/zoho/read-payments'), (r: any) => { reload(); return r.message })}>Read payment status from Zoho</button>
          <button className="btn" disabled={!sel.size || a.busy} onClick={() => a.run(() => post<{ pushed: number; failed: number; results: any[] }>('/zoho/push', { invoice_ids: [...sel] }), (r) => {
            setResults(r.results); setSel(new Set()); reload(); return `${r.pushed} pushed to Zoho${r.failed ? `, ${r.failed} not pushed` : ''}`
          })}>Push selected to Zoho ({sel.size})</button></>}</div></div>
      <Errors list={a.errors} />
      {results && <section className="card"><h2>Last push</h2>{results.map((r) => <p key={r.invoice_id}><Pill tone={r.ok ? 'ok' : 'bad'}>{r.ok ? 'Pushed' : 'Not pushed'}</Pill> {rows.find((x) => x.id === r.invoice_id)?.invoice_number}: {r.message}</p>)}</section>}
      <DataTable screen="payments" rows={rows} rowKey={(r) => r.id} onOpen={(r) => nav('/invoices/' + r.id)}
        selectable={fin && zoho ? (r) => r.status === 'PAY_APPROVED' && !r.held.length : undefined} selected={sel} onSelect={setSel}
        search={(r) => [r.invoice_number, r.vendor, r.property, r.po_number, r.document_number, r.zoho_bill_id].join(' ')}
        filters={[{ id: 'st', label: 'Status', options: [['', 'All'], ['MATCHED', 'Payment approval'], ['PAY_APPROVED', zoho ? 'Ready for Zoho' : 'Payment approved'], ...(zoho ? [['HELD', 'Held from Zoho'], ['PUSHED', 'In Zoho, to be paid']] as [string, string][] : []), ['PAID', 'Paid'], ['MSME', 'MSME vendors'], ['LATE', 'Overdue']],
          test: (r, v) => v === 'HELD' ? r.held.length > 0 : v === 'MSME' ? r.vendor_msme : v === 'LATE' ? !!overdue(r) : r.status === v }]}
        columns={[
          { key: 'inv', label: 'Invoice', render: (r) => r.invoice_number, csv: (r) => r.invoice_number },
          { key: 'type', label: 'Type', render: (r) => r.kind === 'bill' ? r.bill_type + ' bill' : 'Purchase', csv: (r) => r.kind },
          { key: 'vendor', label: 'Vendor', render: (r) => <>{r.vendor}{r.vendor_msme && <> <Pill tone="warn">MSME</Pill></>}{!r.vendor_gst_registered && <> <Pill tone="off">Unregistered</Pill></>}</>, csv: (r) => r.vendor },
          { key: 'property', label: 'Property', render: (r) => r.property, csv: (r) => r.property },
          { key: 'amt', label: 'Amount', right: true, render: (r) => inr(r.amount), sort: (r) => r.amount, csv: (r) => r.amount },
          { key: 'payby', label: 'Pay by', render: (r) => overdue(r) ? <Pill tone="bad">Overdue {day(r.pay_by)}</Pill> : day(r.pay_by), sort: (r) => r.pay_by, csv: (r) => r.pay_by },
          ...(zoho ? [{ key: 'zoho', label: 'Zoho bill', render: (r: Invoice) => r.zoho_bill_id || (r.held.length ? <Pill tone="bad">Held: {r.held[0]}{r.held.length > 1 ? ` +${r.held.length - 1}` : ''}</Pill> : '—'), csv: (r: Invoice) => r.zoho_bill_id }] : []),
          { key: 'status', label: 'Status', render: (r) => <Pill tone={LABEL[r.status]?.[1] || ''}>{!zoho && r.status === 'PAY_APPROVED' ? 'Payment approved' : LABEL[r.status]?.[0] || r.status}</Pill>, csv: (r) => r.status },
          { key: 'act', label: '', render: (r) => <span className="tools" style={{ flexWrap: 'nowrap' }}>
            {fin && r.status === 'MATCHED' && <button className="btn ghost small" onClick={() => a.run(() => post(`/invoices/${r.id}/approve-payment`), (x: any) => { reload(); return x.message })}>Approve payment</button>}
            {zoho && r.status === 'PAY_APPROVED' && <button className="link" onClick={() => setPreview(r.id)}>Preview</button>}
            {me?.is_admin && (r.status === 'PUSHED' || (!zoho && r.status === 'PAY_APPROVED')) && <button className="link" onClick={() => setPaid(r)}>Mark paid</button>}</span> },
        ]} csvName="payments" />
      {preview != null && <ZohoPreview invoiceId={preview} onClose={() => setPreview(null)} />}
      {paid && <MarkPaid inv={paid} onClose={() => setPaid(null)} onDone={() => { setPaid(null); reload() }} />}
    </>
  )
}

function MarkPaid({ inv, onClose, onDone }: { inv: Invoice; onClose: () => void; onDone: () => void }) {
  const [ref, setRef] = useState('')
  const a = useAction()
  return (
    <Modal title={`Mark ${inv.invoice_number} as paid`} onClose={onClose}>
      <p className="note warn">Use this only as a fallback, for example while Zoho is disconnected. Normally payment is read back from Zoho.</p>
      <label className="field"><span>Payment reference (UTR or cheque number)</span><input value={ref} onChange={(e) => setRef(e.target.value)} /></label>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post(`/invoices/${inv.id}/mark-paid`, { reference: ref }), (r: any) => { onDone(); return r.message })}>Mark as paid</button></div>
    </Modal>
  )
}
