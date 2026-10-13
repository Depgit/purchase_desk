// Utility or AMC bills: no PO or goods receipt; the Cluster Manager approves, then Finance pays.
import { useCallback, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useLookups } from '../api/lookups'
import type { ListRow } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { inr, month, daysBack } from '../format/format'
import { DataTable } from '../ui/DataTable'
import { Pill, statusTone } from '../ui/Pill'
import { placeFilters } from '../requests/columns'
import { useScreen } from '../screenaccess/screen'
import { DraftList } from '../formdrafts/drafts'

export function BillsPage() {
  const { section = 'utility' } = useParams()
  const nav = useNavigate()
  const l = useLookups()
  const { hasRight } = useSession()
  const sc = useScreen(section === 'amc' ? 'bills_amc' : 'bills_utility')
  const [days, setDays] = useState(90)
  const fromChanged = useCallback((f: string) => setDays(daysBack(f)), [])
  const { data } = useApi<ListRow[]>(`/requests?kind=bill&section=${section}&days=${days}`)
  const amc = section === 'amc'
  return (
    <>
      <div className="head"><div><h1>{amc ? 'AMC Bills' : 'Utility Bills'}</h1>
        <p className="sub">{amc ? 'Maintenance contracts billed for several months.' : 'Monthly utility bills.'} The Cluster Manager approves, then Finance pays.</p></div>
        <div className="acts">{sc.edit && hasRight('raise') && <button className="btn" onClick={() => nav('/bills/new/' + section)}>+ New {amc ? 'AMC' : 'utility'} bill</button>}</div></div>
      {sc.edit && <DraftList form="bill" label={`Your ${amc ? 'AMC' : 'utility'} bill drafts`} only={(d) => d.fields.section === section} onOpen={(d) => nav(`/bills/new/${section}?draft=${d.id}`)} />}
      <DataTable screen={'bills-' + section} rows={data || []} rowKey={(r) => r.id} date={{ label: 'Raised', value: (r) => r.created_at, onFrom: fromChanged }} onOpen={(r) => nav('/documents/' + r.id)} filters={placeFilters(l).slice(0, 2)}
        search={(r) => [r.number, r.bill_type, r.property, r.vendor].join(' ')}
        columns={[
          { key: 'bill', label: 'Bill', render: (r) => r.number, sort: (r) => r.id, csv: (r) => r.number },
          { key: 'type', label: 'Item', render: (r) => r.bill_type, csv: (r) => r.bill_type },
          { key: 'period', label: 'Period', render: (r) => month(r.period_start) + (r.months > 1 ? ` · ${r.months} months` : ''), sort: (r) => r.period_start, csv: (r) => r.period_start },
          { key: 'property', label: 'Property', render: (r) => r.property, csv: (r) => r.property },
          { key: 'vendor', label: 'Vendor', render: (r) => r.vendor, csv: (r) => r.vendor },
          { key: 'amt', label: 'Amount', right: true, render: (r) => inr(r.amount), sort: (r) => r.amount, csv: (r) => r.amount },
          { key: 'status', label: 'Status', render: (r) => <Pill tone={statusTone(r.status)}>{r.status_label}</Pill>, csv: (r) => r.status_label },
        ]} csvName={'bills-' + section} />
    </>
  )
}
