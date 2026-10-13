// Screen levels from the user's roles (no access, view, edit), as the server sends them in /me. They decide the menu,
// whether a screen opens, and whether its change buttons show. The server checks the same on every request.
import type { ReactNode } from 'react'
import { useSession } from '../auth/session'
import type { Level } from '../api/types'

export type ScreenKey = 'approvals' | 'requests' | 'purchase_orders' | 'vendor_sheets' | 'goods_receipts' | 'invoices' | 'payments'
  | 'bills_utility' | 'bills_amc' | 'corporate' | 'warehouse' | 'vendors' | 'items' | 'vendor_mapping' | 'reports'
  | 'properties' | 'cities' | 'budgets' | 'corporate_setup' | 'bill_types' | 'item_setup' | 'users' | 'flow' | 'zoho' | 'templates' | 'activity'

const RANK: Record<Level, number> = { none: 0, view: 1, edit: 2 }

/** The user's level on a screen: view is true for view or edit, edit only for edit. */
export function useScreen(key: ScreenKey) {
  const { me } = useSession()
  const level: Level = me?.is_admin ? 'edit' : (me?.screens?.[key] || 'none')
  return { level, view: RANK[level] >= 1, edit: RANK[level] >= 2 }
}

/** Opens a screen only when the user's roles give at least view on it. */
export function ScreenGate({ screen, children }: { screen: ScreenKey; children: ReactNode }) {
  const { view } = useScreen(screen)
  if (view) return <>{children}</>
  return (
    <div className="head"><div><h1>No access</h1>
      <p className="sub">Your role does not include this screen. Ask the System Admin if you need it.</p></div></div>
  )
}
