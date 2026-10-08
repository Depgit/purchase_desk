// The home page before the full dashboards open (phase 5): what is waiting for the signed-in person, one tile per
// list, using the same counts as the menu badges. Each tile opens its list.
import { useNavigate } from 'react-router-dom'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { PHASE, usePhase } from '../releasephase/phase'

const TILES: { key: string; label: string; to: string; phase: number }[] = [
  { key: 'approvals', label: 'Waiting for my approval', to: '/approvals', phase: PHASE.core },
  { key: 'requests', label: 'My requests to act on', to: '/requests', phase: PHASE.core },
  { key: 'purchase_orders', label: 'Purchase orders to issue', to: '/purchase-orders', phase: PHASE.core },
  { key: 'goods_receipts', label: 'Deliveries to receive', to: '/goods-receipts', phase: PHASE.core },
  { key: 'invoices', label: 'Invoices to match', to: '/invoices', phase: PHASE.core },
  { key: 'payments', label: 'Payments to approve', to: '/payments', phase: PHASE.core },
  { key: 'vendors', label: 'Vendors waiting for bank verification', to: '/vendors', phase: PHASE.core },
  { key: 'bills_utility', label: 'Utility bills to act on', to: '/bills/utility', phase: PHASE.bills },
  { key: 'bills_amc', label: 'AMC bills to act on', to: '/bills/amc', phase: PHASE.bills },
  { key: 'corporate', label: 'Corporate requests to act on', to: '/corporate', phase: PHASE.corporate },
  { key: 'warehouse', label: 'Warehouse work', to: '/warehouse', phase: PHASE.warehouse },
]

export function MyWorkPage() {
  const nav = useNavigate()
  const { me } = useSession()
  const { on } = usePhase()
  const { data } = useApi<Record<string, number>>('/worklist')
  const waiting = TILES.filter((t) => on(t.phase) && (data?.[t.key] || 0) > 0)
  return (
    <>
      <div className="head"><div><h1>Namaste, {me?.name.split(' ')[0]}</h1><p className="sub">What is waiting for you right now.</p></div></div>
      {!data ? <p className="sub">Loading…</p>
        : waiting.length === 0 ? <p className="note ok">Nothing is waiting for you right now.</p>
          : <div className="tiles">{waiting.map((t) => (
            <button key={t.key} className="tile" onClick={() => nav(t.to)}><span>{t.label}</span><b>{data[t.key]}</b><small>Open the list</small></button>
          ))}</div>}
    </>
  )
}
