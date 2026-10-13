// Marks a master record active or inactive. Before marking inactive it shows what will be affected; nothing is deleted.
import { useState } from 'react'
import { get, post } from '../api/client'
import { useSession } from '../auth/session'
import type { ScreenKey } from '../screenaccess/screen'
import { Modal } from './Modal'
import { Pill } from './Pill'
import { useAction } from './useAction'
import { Errors } from './Errors'

// The screen each master kind belongs to; view only there hides the switch (the server checks the same).
const SCREEN: Record<string, ScreenKey> = { city: 'cities', cluster: 'cities', property: 'properties', sub_category: 'item_setup', category: 'item_setup',
  brand: 'item_setup', pack_size: 'item_setup', sku: 'items', vendor: 'vendors', bill_type: 'bill_types', mapping: 'vendor_mapping',
  department: 'corporate_setup', expense_head: 'corporate_setup', partner: 'warehouse' }

export function ActiveToggle({ kind, id, name, active, reason, onDone, canEdit }: {
  kind: string; id: number; name: string; active: boolean; reason?: string; onDone: () => void; canEdit?: boolean
}) {
  const { hasRight, me } = useSession()
  const screen = SCREEN[kind]
  const editable = canEdit ?? (kind === 'user' || kind === 'role' ? !!me?.is_admin : hasRight('masters') && (!!me?.is_admin || !screen || me?.screens?.[screen] === 'edit'))
  const [ask, setAsk] = useState<string | null>(null)
  const [why, setWhy] = useState('')
  const a = useAction()
  return (
    <>
      <Pill tone={active ? 'ok' : 'off'}>{active ? 'Active' : 'Inactive'}</Pill>
      {editable && (active
        ? <> <button className="link" onClick={async () => setAsk((await get(`/masters/${kind}/${id}/impact`)).message)}>Mark inactive</button></>
        : <> <button className="link" onClick={() => a.run(() => post(`/masters/${kind}/${id}/active`, { active: true }), () => { onDone(); return name + ' is active again' })}>Mark active</button></>)}
      {!active && reason ? <><br /><span className="hint">{reason}</span></> : null}
      {ask != null && (
        <Modal title={`Mark ${name} inactive?`} onClose={() => setAsk(null)}>
          <p className="note">{ask}</p>
          <p className="sub">Nothing is deleted. You can mark it active again at any time, and the change is logged.</p>
          <label className="field"><span>Reason (optional)</span><input value={why} onChange={(e) => setWhy(e.target.value)} /></label>
          <Errors list={a.errors} />
          <div className="btns"><button className="btn ghost" onClick={() => setAsk(null)}>Cancel</button>
            <button className="btn no" disabled={a.busy} onClick={() => a.run(() => post(`/masters/${kind}/${id}/active`, { active: false, reason: why }), () => { setAsk(null); onDone(); return name + ' marked inactive' })}>Mark inactive</button></div>
        </Modal>
      )}
    </>
  )
}
