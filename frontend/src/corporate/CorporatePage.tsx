// The corporate office's purchases and bills: what waits for me, what I raised, and all of them.
import { useState, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import type { ListRow } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { day, inr, daysBack } from '../format/format'
import { DataTable } from '../ui/DataTable'
import { Pill, statusTone } from '../ui/Pill'
import { Tabs } from '../ui/Tabs'
import { useScreen } from '../screenaccess/screen'
import { DraftList } from '../formdrafts/drafts'

export function CorporatePage() {
  const nav = useNavigate()
  const { me, hasRight } = useSession()
  const sc = useScreen('corporate')
  const [days, setDays] = useState(90)
  const fromChanged = useCallback((f: string) => setDays(daysBack(f)), [])
  const { data } = useApi<ListRow[]>(`/requests?kind=corp&days=${days}`)
  const rows = data || []
  const mine = rows.filter((r) => r.my_turn)
  const raised = rows.filter((r) => r.requester === me?.name)
  const [tab, setTab] = useState<'todo' | 'mine' | 'all'>('todo')
  const shown = tab === 'todo' ? mine : tab === 'mine' ? raised : rows
  return (
    <>
      <div className="head"><div><h1>Corporate</h1><p className="sub">Purchases and bills of the corporate office. Lines need no SKU: a description and an expense head. Every request is approved by the department's L1, then its L2.</p></div>
        <div className="acts">{sc.edit && hasRight('raise') && <><button className="btn" onClick={() => nav('/corporate/new/purchase')}>+ Corporate purchase</button><button className="btn ghost" onClick={() => nav('/corporate/new/bill')}>+ Corporate bill</button></>}</div></div>
      {sc.edit && <DraftList form="corporate" label="Your corporate drafts" onOpen={(d) => nav(`/corporate/new/${d.fields.kind}?draft=${d.id}`)} />}
      <Tabs value={tab} onChange={setTab} items={[['todo', `Waiting for me (${mine.length})`], ['mine', `Raised by me (${raised.length})`], ['all', `All corporate (${rows.length})`]]} />
      <DataTable screen="corporate" rows={shown} rowKey={(r) => r.id} date={{ label: 'Raised', value: (r) => r.created_at, onFrom: fromChanged }} onOpen={(r) => nav('/documents/' + r.id)} search={(r) => [r.number, r.po_number, r.department, r.vendor, r.items, r.requester].join(' ')}
        filters={[{ id: 't', label: 'Type', options: [['', 'Purchases and bills'], ['corp_purchase', 'Purchases (with PO)'], ['corp_bill', 'Bills (no PO)']], test: (r, v) => r.kind === v },
          { id: 'st', label: 'Status', options: [['', 'Any status'], ['open', 'Open'], ['PENDING', 'Waiting for approval'], ['RETURNED', 'Returned'], ['CLOSED', 'Paid'], ['REJECTED', 'Rejected']],
            test: (r, v) => v === 'open' ? !['CLOSED', 'REJECTED', 'CANCELLED'].includes(r.status) : r.status === v }]}
        columns={[
          { key: 'no', label: 'Request', render: (r) => r.po_number || r.number, sort: (r) => r.id, csv: (r) => r.number },
          { key: 'type', label: 'Type', render: (r) => r.kind === 'corp_bill' ? 'Bill' : 'Purchase', csv: (r) => r.kind },
          { key: 'dep', label: 'Department', render: (r) => r.department, csv: (r) => r.department },
          { key: 'by', label: 'Raised by', render: (r) => r.requester, csv: (r) => r.requester },
          { key: 'date', label: 'Raised on', render: (r) => day(r.created_at), sort: (r) => r.created_at, csv: (r) => r.created_at.slice(0, 10) },
          { key: 'vendor', label: 'Vendor', render: (r) => r.vendor, csv: (r) => r.vendor },
          { key: 'what', label: 'What', render: (r) => r.items, csv: (r) => r.items },
          { key: 'amt', label: 'Amount', right: true, render: (r) => inr(r.amount), sort: (r) => r.amount, csv: (r) => r.amount },
          { key: 'st', label: 'Status', render: (r) => <><Pill tone={statusTone(r.status)}>{r.status_label}</Pill>{r.my_turn && <> <Pill tone="warn">Your turn</Pill></>}</>, csv: (r) => r.status_label },
        ]} csvName="corporate-requests" />
    </>
  )
}
