// The wording of every email and WhatsApp message. Words in {curly brackets} are filled in automatically.
import { useEffect, useState } from 'react'
import { get, put } from '../api/client'
import { useSession } from '../auth/session'
import { Pill } from '../ui/Pill'
import { PHASE, usePhase } from '../releasephase/phase'
import { useToast } from '../ui/Toast'

interface T { code: string; name: string; channel: string; body: string }
// Messages that belong to a later release phase: vendor order sheets and warehouse deliveries.
const LATER: Record<string, number> = { vse: PHASE.sheets, vsw: PHASE.sheets, dlv: PHASE.warehouse }

export function TemplatesPage() {
  const { me } = useSession()
  const { on } = usePhase()
  const toast = useToast()
  const [list, setList] = useState<T[]>([])
  useEffect(() => { get<T[]>('/templates').then(setList) }, [])
  return (
    <>
      <div className="head"><div><h1>Message Templates</h1><p className="sub">WhatsApp messages go out through MSG91 and must match a template approved on the sender number. {me?.is_admin ? 'Edit the text and click outside the box to save.' : 'Only the System Admin can change these.'}</p></div></div>
      <div className="grid2">{list.filter((t) => on(LATER[t.code] || PHASE.core)).map((t) => (
        <section key={t.code} className="card"><h2>{t.name} <Pill>{t.channel === 'email' ? 'Email' : 'WhatsApp'}</Pill></h2>
          <textarea rows={Math.min(12, t.body.split('\n').length + 2)} defaultValue={t.body} disabled={!me?.is_admin}
            onBlur={async (e) => { if (e.target.value === t.body) return
              try { await put('/templates/' + t.code, { body: e.target.value }); setList(list.map((x) => x.code === t.code ? { ...x, body: e.target.value } : x)); toast('Template saved') } catch (err: any) { toast(err.message, true) } }} />
          <span className="hint">Fills in: {[...new Set(t.body.match(/\{\w+\}/g) || [])].join(' ') || 'nothing'}</span></section>))}
      </div>
    </>
  )
}
