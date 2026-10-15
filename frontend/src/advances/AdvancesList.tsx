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
        { key: 'vendor', label: 'Vendor', render: (a) => <>{a.vendor}<br /><span className="hint">{a.property}</span></>, csv: (a) => `${a.vendor} · ${a.property}` },
        { key: 'amt', label: 'Advance', right: true, render: (a) => <><span className="nowrap">{inr(a.amount)}</span>{a.pct ? <span className="hint"> · {a.pct}%</span> : null}
          {a.tds_amount > 0 && <><br /><span className="hint nowrap">TDS {inr(a.tds_amount)} · vendor gets {inr(a.pay_vendor)}</span></>}</>, sort: (a) => a.amount, csv: (a) => a.amount },
        { key: 'paid', label: 'Paid', render: (a) => a.paid_on ? <><span className="nowrap">{day(a.paid_on)}</span><br /><span className="hint">{a.payment_ref}</span>
          {a.status === 'PAID' && <><br /><span className="hint">{a.zoho_payment_id ? 'In Zoho' : a.zoho_error ? 'Zoho: ' + a.zoho_error.replace(/^Held: /, '') : 'Going to Zoho'}</span></>}</> : '—', sort: (a) => a.paid_on, csv: (a) => a.paid_on ? `${a.paid_on} ${a.payment_ref}` : '' },
        { key: 'open', label: 'Used / left', right: true, render: (a) => a.status !== 'PAID' ? '—' : <><span className="nowrap"><b>{inr(a.open)}</b> left</span><br />
          <span className="hint nowrap">{inr(a.adjusted)} adjusted{a.refunded ? ` · ${inr(a.refunded)} refunded` : ''}</span></>, sort: (a) => a.open, csv: (a) => a.open },
        { key: 'status', label: 'Status', render: (a) => <AdvanceStatus a={a} />, csv: (a) => a.status },
        { key: 'act', label: '', render: (a) => <span onClick={(e) => e.stopPropagation()}><AdvanceActions a={a} onDone={done} /></span> },
      ]} csvName="advances" />
  )
}
