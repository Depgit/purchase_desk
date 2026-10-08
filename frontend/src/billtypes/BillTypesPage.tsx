// Utility and AMC bill types. Vendors are linked to bill types on the vendor itself.
import { useState } from 'react'
import { post, put } from '../api/client'
import { refreshLookups } from '../api/lookups'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { ActiveToggle } from '../ui/ActiveToggle'
import { DataTable } from '../ui/DataTable'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'

interface BT { id: number; name: string; section: string; default_gst: number; active: boolean; vendors: string[]; usable_vendors: number; bills: number }

export function BillTypesPage() {
  const { hasRight } = useSession()
  const { data, reload } = useApi<BT[]>('/bill-types')
  const [edit, setEdit] = useState<BT | 'new' | null>(null)
  return (
    <>
      <div className="head"><div><h1>Bill Types</h1><p className="sub">A bill can be raised only for an active type with at least one verified vendor. Vendors tick their bill types under Vendors.</p></div>
        <div className="acts">{hasRight('masters') && <button className="btn" onClick={() => setEdit('new')}>+ Add bill type</button>}</div></div>
      <DataTable screen="bill-types" rows={data || []} rowKey={(b) => b.id} dim={(b) => !b.active} search={(b) => b.name + ' ' + b.vendors.join(' ')}
        filters={[{ id: 's', label: 'Section', options: [['', 'Utility and AMC'], ['utility', 'Utility'], ['amc', 'Maintenance AMC']], test: (b, v) => b.section === v }]}
        columns={[
          { key: 'name', label: 'Bill type', render: (b) => b.name, csv: (b) => b.name },
          { key: 'section', label: 'Section', render: (b) => b.section === 'amc' ? 'Maintenance AMC' : 'Utility', csv: (b) => b.section },
          { key: 'gst', label: 'Default GST', right: true, render: (b) => b.default_gst + '%', csv: (b) => b.default_gst },
          { key: 'vendors', label: 'Vendors', render: (b) => b.vendors.length ? <>{b.vendors.join(', ')}{b.usable_vendors === 0 && <> <Pill tone="warn">none verified</Pill></>}</> : <Pill tone="bad">No vendor yet</Pill>, csv: (b) => b.vendors.join('; ') },
          { key: 'bills', label: 'Bills raised', right: true, render: (b) => b.bills, sort: (b) => b.bills, csv: (b) => b.bills },
          { key: 'status', label: 'Status', render: (b) => <ActiveToggle kind="bill_type" id={b.id} name={b.name} active={b.active} onDone={reload} />, csv: (b) => b.active ? 'Active' : 'Inactive' },
          { key: 'edit', label: '', render: (b) => hasRight('masters') ? <button className="link" onClick={() => setEdit(b)}>Edit</button> : null },
        ]} csvName="bill-types" />
      {edit && <BTModal b={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onDone={() => { setEdit(null); reload(); refreshLookups() }} />}
    </>
  )
}

function BTModal({ b, onClose, onDone }: { b: BT | null; onClose: () => void; onDone: () => void }) {
  const [f, setF] = useState({ name: b?.name || '', section: b?.section || 'utility', default_gst: b?.default_gst ?? 18 })
  const a = useAction()
  return (
    <Modal title={b ? 'Edit bill type' : 'Add bill type'} onClose={onClose}>
      <div className="grid3">
        <Field label="Name"><input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
        <Field label="Section"><select value={f.section} onChange={(e) => setF({ ...f, section: e.target.value })}><option value="utility">Utility bill</option><option value="amc">Maintenance AMC</option></select></Field>
        <Field label="Default GST %"><select value={f.default_gst} onChange={(e) => setF({ ...f, default_gst: +e.target.value })}>{[0, 5, 12, 18, 28, 40].map((g) => <option key={g} value={g}>{g}%</option>)}</select></Field>
      </div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => b ? put('/bill-types/' + b.id, f) : post('/bill-types', f), () => { onDone(); return 'Bill type saved' })}>Save</button></div>
    </Modal>
  )
}
