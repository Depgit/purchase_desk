// Every vendor advance in the viewer's access, on the Payments page: Finance approves and pays them here, and follows
// what is still to be adjusted, refunded or carried forward.
import { useNavigate } from 'react-router-dom'
import type { Advance } from '../api/types'
import { useApi } from '../api/useApi'
import { day, inr } from '../format/format'
import { DataTable } from '../ui/DataTable'
import { AdvanceActions, AdvanceStatus } from './AdvanceActions'

export function AdvancesList({ onChange }: { onChange?: () => void }) {
  const nav = useNavigate()
  const { data, reload } = useApi<Advance[]>('/advances')
  const done = () => { reload(); onChange?.() }
  return (
    <DataTable screen="advances" rows={data || []} rowKey={(a) => a.id} onOpen={(a) => nav('/documents/' + a.document_id)} date={{ label: 'Requested', value: (a) => a.requested_at.slice(0, 10) }}
      search={(a) => [a.number, a.po_number, a.document_number, a.vendor, a.property, a.payment_ref, a.reason].join(' ')}
      filters={[{ id: 'st', label: 'Status', options: [['', 'All'], ['TODO', 'Waiting for Finance'], ['PENDING', 'To approve'], ['APPROVED', 'Approved, to pay'], ['OPEN', 'Paid, still to adjust'], ['CARRY', 'Carried forward'], ['SETTLED', 'Settled'], ['REJECTED', 'Rejected'], ['CANCELLED', 'Cancelled']],
        test: (a, v) => v === 'TODO' ? a.status === 'PENDING' || a.status === 'APPROVED' : v === 'OPEN' ? a.open > 0.005 : v === 'CARRY' ? a.carry_forward && a.open > 0.005
          : v === 'SETTLED' ? a.status === 'PAID' && a.open < 0.005 : a.status === v }]}
      columns={[
        { key: 'no', label: 'Advance', render: (a) => a.number, csv: (a) => a.number },
        { key: 'po', label: 'PO', render: (a) => a.po_number, csv: (a) => a.po_number },
        { key: 'vendor', label: 'Vendor', render: (a) => a.vendor, csv: (a) => a.vendor },
        { key: 'property', label: 'Property', render: (a) => a.property, csv: (a) => a.property },
        { key: 'amt', label: 'Advance', right: true, render: (a) => <>{inr(a.amount)}{a.pct ? <span className="hint"> · {a.pct}%</span> : null}</>, sort: (a) => a.amount, csv: (a) => a.amount },
        { key: 'tds', label: 'TDS', right: true, render: (a) => a.tds_amount ? '− ' + inr(a.tds_amount) : '—', csv: (a) => a.tds_amount },
        { key: 'pay', label: 'Pay the vendor', right: true, render: (a) => inr(a.pay_vendor), sort: (a) => a.pay_vendor, csv: (a) => a.pay_vendor },
        { key: 'paid', label: 'Paid', render: (a) => a.paid_on ? <>{day(a.paid_on)} <span className="hint">{a.payment_ref}</span></> : '—', sort: (a) => a.paid_on, csv: (a) => a.paid_on ? `${a.paid_on} ${a.payment_ref}` : '' },
        { key: 'adj', label: 'Adjusted', right: true, render: (a) => a.adjusted ? inr(a.adjusted) : '—', csv: (a) => a.adjusted },
        { key: 'ref', label: 'Refunded', right: true, render: (a) => a.refunded ? inr(a.refunded) : '—', csv: (a) => a.refunded },
        { key: 'open', label: 'Left to adjust', right: true, render: (a) => a.open ? inr(a.open) : '—', sort: (a) => a.open, csv: (a) => a.open },
        { key: 'status', label: 'Status', render: (a) => <AdvanceStatus a={a} />, csv: (a) => a.status },
        { key: 'act', label: '', render: (a) => <span onClick={(e) => e.stopPropagation()}><AdvanceActions a={a} onDone={done} /></span> },
      ]} csvName="advances" />
  )
}
