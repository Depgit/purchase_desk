// Every action on documents, master data and settings, newest first. The log cannot be edited.
import { MultiSelect } from '../ui/MultiSelect'
import { useState } from 'react'
import { useApi } from '../api/useApi'
import { dateTime } from '../format/format'
import { DataTable } from '../ui/DataTable'

interface Entry { id: number; at: string; user: string; role: string; module: string; action: string; record: string; details: string }
const MODULES = ['Purchases', 'Bills', 'Invoices', 'Payments', 'Zoho', 'Vendors', 'Items', 'Vendor Mapping', 'Vendor Rates', 'Properties', 'Budgets', 'Users', 'Settings', 'Warehouse', 'Corporate', 'Vendor Orders']

export function ActivityLogPage() {
  const [mod, setMod] = useState<string[]>([])
  const { data } = useApi<Entry[]>(`/activity?limit=5000&module=${encodeURIComponent(mod.join(','))}`)
  return (
    <>
      <div className="head"><div><h1>Activity Log</h1><p className="sub">Every action, with user, role, time, record and details. The latest 5,000 entries are shown; download to keep a copy.</p></div>
        <div className="acts"><MultiSelect ariaLabel="Module" value={mod} onChange={setMod} options={[['', 'All modules'], ...MODULES.map((m) => [m, m] as [string, string])]} /></div></div>
      <DataTable screen="activity" rows={data || []} rowKey={(e) => e.id} date={{ label: 'Date', value: (e) => e.at }} search={(e) => [e.user, e.role, e.module, e.action, e.record, e.details].join(' ')}
        columns={[
          { key: 'at', label: 'Time', render: (e) => dateTime(e.at), sort: (e) => e.at, csv: (e) => e.at },
          { key: 'user', label: 'User', render: (e) => e.user, csv: (e) => e.user },
          { key: 'role', label: 'Role', render: (e) => e.role, csv: (e) => e.role },
          { key: 'module', label: 'Module', render: (e) => e.module, csv: (e) => e.module },
          { key: 'action', label: 'Action', render: (e) => e.action, csv: (e) => e.action },
          { key: 'record', label: 'Record', render: (e) => e.record, csv: (e) => e.record },
          { key: 'details', label: 'Details', render: (e) => e.details, csv: (e) => e.details },
        ]} csvName="activity-log" />
    </>
  )
}
