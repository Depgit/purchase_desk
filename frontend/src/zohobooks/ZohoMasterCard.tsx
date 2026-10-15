// The Zoho Books card on a vendor's, item's or property's page: whether it is in Zoho (and as what), why not yet, and
// "Send to Zoho now" for Finance and the Admin.
import { post } from '../api/client'
import { useApi } from '../api/useApi'
import { dateTime } from '../format/format'
import { Errors } from '../ui/Errors'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'

interface Status { linked: boolean; zoho_name: string; problem: string; at: string | null; auto: boolean; can_send: boolean }

const SEND: Record<string, (id: number) => string> = {
  vendor: (id) => `/zoho/vendors/${id}/create`, item: (id) => `/zoho/items/${id}/create`, property: (id) => `/zoho/properties/${id}/create-option`,
}

export function ZohoMasterCard({ kind, id }: { kind: 'vendor' | 'item' | 'property'; id: number }) {
  const { data, error, reload } = useApi<Status>(`/zoho/masters/${kind}/${id}`)
  const a = useAction()
  if (error || !data) return null
  return (
    <section className="card"><h2>Zoho Books</h2>
      {data.linked ? <p><Pill tone="ok">In Zoho</Pill> as {data.zoho_name}</p>
        : <p><Pill tone="warn">Not in Zoho yet</Pill> {data.problem ? <>{data.problem} <span className="hint">({dateTime(data.at)})</span></>
          : data.auto ? <span className="hint">It goes to Zoho by itself within a minute once it is ready.</span> : <span className="hint">Automatic sending is off: send it with the button.</span>}</p>}
      <Errors list={a.errors} />
      {!data.linked && data.can_send && <div className="btns"><button className="btn ghost" disabled={a.busy} onClick={() => a.run(() => post(SEND[kind](id)), () => reload())}>Send to Zoho now</button></div>}
    </section>
  )
}
