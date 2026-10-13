// Every vendor of every type. A vendor can be used only when active and its bank details are verified by Finance.
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { fileUrl, post } from '../api/client'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { UploadModal } from '../bulkupload/UploadModal'
import { ActiveToggle } from '../ui/ActiveToggle'
import { DataTable } from '../ui/DataTable'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'
import { PHASE, usePhase } from '../releasephase/phase'
import type { Vendor } from './VendorForm'
import { useScreen } from '../screenaccess/screen'

const TYPE: Record<string, string> = { goods: 'Goods', utility: 'Utility', amc: 'AMC', other: 'Other services', warehouse: 'Internal warehouse' }

export function VendorsPage() {
  const nav = useNavigate()
  const { hasRight, hasStep } = useSession()
  const sc = useScreen('vendors')
  const bills = usePhase().on(PHASE.bills)
  const { data, reload } = useApi<Vendor[]>('/vendors')
  const [upload, setUpload] = useState(false)
  const a = useAction()
  const fin = sc.edit && hasStep('finance')
  return (
    <>
      <div className="head"><div><h1>Vendors</h1><p className="sub">Goods suppliers, utility providers, AMC contractors and other services. A vendor is used only when active and bank verified.</p></div>
        <div className="acts">{(sc.edit && hasRight('masters')) && <>{hasRight('upload') && <button className="btn ghost" onClick={() => setUpload(true)}>Bulk upload / edit</button>}
          <button className="btn" onClick={() => nav('/vendors/new')}>+ Onboard vendor</button></>}</div></div>
      <DataTable screen="vendors" rows={data || []} rowKey={(v) => v.id} dim={(v) => !v.active}
        search={(v) => [v.code, v.legal_name, v.city, v.gstin, v.pan, ...v.supplies].join(' ')}
        filters={[
          { id: 'type', label: 'Type', options: bills ? [['', 'All vendor types'], ['goods', 'Goods'], ['utility', 'Utility'], ['amc', 'AMC'], ['other', 'Other services']] : [['', 'All vendor types'], ['goods', 'Goods']], test: (v, x) => v.vendor_type === x },
          { id: 'bank', label: 'Bank', options: [['', 'Any bank status'], ['1', 'Bank verified'], ['0', 'Verification pending']], test: (v, x) => v.bank_verified === (x === '1') },
          { id: 'act', label: 'Active', options: [['', 'Active and inactive'], ['1', 'Active only'], ['0', 'Inactive only']], test: (v, x) => v.active === (x === '1') },
        ]}
        columns={[
          { key: 'code', label: 'Code', render: (v) => v.code, csv: (v) => v.code },
          { key: 'name', label: 'Vendor', render: (v) => v.legal_name, csv: (v) => v.legal_name },
          { key: 'type', label: 'Type', render: (v) => TYPE[v.vendor_type], csv: (v) => v.vendor_type },
          { key: 'city', label: 'City', render: (v) => v.city, csv: (v) => v.city },
          { key: 'gst', label: 'GST', render: (v) => v.gst_registered ? v.gstin : 'Unregistered', csv: (v) => v.gstin },
          { key: 'msme', label: 'MSME', render: (v) => v.msme ? <Pill tone="warn">MSME</Pill> : 'No', csv: (v) => v.msme ? 'MSME' : 'No' },
          { key: 'supplies', label: 'Supplies', render: (v) => v.supplies.join(', '), csv: (v) => v.supplies.join('; ') },
          { key: 'terms', label: 'Terms', render: (v) => v.payment_terms, csv: (v) => v.payment_terms },
          { key: 'bank', label: 'Bank details', render: (v) => <>{v.bank_verified ? <Pill tone="ok">Verified</Pill> : <Pill tone="bad">Pending</Pill>}
            {v.bank_account_masked && <span className="hint"> {v.bank_account_masked}</span>}
            {fin && v.bank_proof_file_id && <> <a className="link" href={fileUrl(v.bank_proof_file_id)} target="_blank" rel="noreferrer">Proof</a></>}
            {fin && !v.bank_verified && v.vendor_type !== 'warehouse' && <> <button className="link" disabled={a.busy} onClick={() => a.run(() => post(`/vendors/${v.id}/verify`), () => { reload(); return 'Bank details verified for ' + v.legal_name })}>Verify</button></>}</>,
            csv: (v) => v.bank_verified ? 'Verified' : 'Pending' },
          { key: 'status', label: 'Status', render: (v) => <ActiveToggle kind="vendor" id={v.id} name={v.legal_name} active={v.active} reason={v.inactive_reason} onDone={reload} />, csv: (v) => v.active ? 'Active' : 'Inactive' },
          { key: 'edit', label: '', render: (v) => (sc.edit && hasRight('masters')) && v.vendor_type !== 'warehouse' ? <button className="link" onClick={() => nav(`/vendors/${v.id}/edit`)}>Edit</button> : null },
        ]} csvName="vendors" />
      {a.errors.length > 0 && <p className="note bad">{a.errors.join(' ')}</p>}
      {upload && <UploadModal kind="vendors" title="Bulk upload or edit vendors" onClose={() => setUpload(false)} onDone={reload} />}
    </>
  )
}
