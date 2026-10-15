// The frame around every screen: the left navigation (with counts of what waits for you) and the top bar.
import type { ScreenKey } from '../screenaccess/screen'
import { useEffect, useState, type ReactNode } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router-dom'
import { get } from '../api/client'
import { useSession } from '../auth/session'
import { useToast } from '../ui/Toast'

interface NavItem { to: string; label: string; badge?: string; show?: boolean; screen?: ScreenKey }

export function Shell({ children }: { children: ReactNode }) {
  const { me, signOut, hasRight, hasStep } = useSession()
  const nav = useNavigate()
  const loc = useLocation()
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const [counts, setCounts] = useState<Record<string, number>>({})
  const [settingsOpen, setSettingsOpen] = useState(loc.pathname.startsWith('/settings'))
  const [theme, setTheme] = useState(() => { try { return localStorage.getItem('pd-theme') || '' } catch { return '' } })

  useEffect(() => { setOpen(false) }, [loc.pathname])
  useEffect(() => {
    const load = () => get<Record<string, number>>('/worklist').then(setCounts).catch(() => {})
    load()
    const t = window.setInterval(load, 60000)
    return () => window.clearInterval(t)
  }, [loc.pathname])
  useEffect(() => {
    if (theme) document.documentElement.setAttribute('data-theme', theme)
    else document.documentElement.removeAttribute('data-theme')
    try { theme ? localStorage.setItem('pd-theme', theme) : localStorage.removeItem('pd-theme') } catch { /* ignore */ }
  }, [theme])

  const groups: NavItem[][] = [
    [{ to: '/', label: 'Dashboard' }],
    [
      { to: '/approvals', label: 'Approvals', badge: 'approvals', screen: 'approvals' },
      { to: '/requests', label: 'Purchase Requests', badge: 'requests', screen: 'requests' },
      { to: '/purchase-orders', label: 'Purchase Orders', badge: 'purchase_orders', screen: 'purchase_orders' },
      { to: '/vendor-sheets', label: 'Vendor Order Sheets', badge: 'vendor_sheets', screen: 'vendor_sheets' },
      { to: '/goods-receipts', label: 'Goods Receipts', badge: 'goods_receipts', screen: 'goods_receipts' },
      { to: '/invoices', label: 'Invoices', badge: 'invoices', screen: 'invoices' },
      { to: '/payments', label: 'Payments', badge: 'payments', screen: 'payments' },
    ],
    [
      { to: '/corporate', label: 'Corporate', badge: 'corporate', screen: 'corporate' },
      { to: '/warehouse', label: 'Warehouse', badge: 'warehouse', screen: 'warehouse' },
    ],
    [
      { to: '/vendors', label: 'Vendors', badge: 'vendors', screen: 'vendors' },
      { to: '/items', label: 'Items (SKU)', screen: 'items' },
      { to: '/vendor-mapping', label: 'Vendor Mapping', screen: 'vendor_mapping' },
      { to: '/settings/master-approvals', label: 'Masters to approve', badge: 'master_approvals', screen: 'master_approvals' },
    ],
    [{ to: '/reports', label: 'Reports', screen: 'reports' }],
  ]
  const settings: NavItem[] = [
    { to: '/settings/properties', label: 'Properties', screen: 'properties' },
    { to: '/settings/cities', label: 'Cities & Clusters', screen: 'cities' },
    { to: '/settings/budgets', label: 'Budgets', screen: 'budgets' },
    { to: '/settings/corporate', label: 'Corporate Setup', screen: 'corporate_setup' },
    { to: '/settings/bill-types', label: 'Bill Types', screen: 'bill_types' },
    { to: '/settings/item-setup', label: 'Item Setup', screen: 'item_setup' },
    { to: '/settings/users', label: 'Users & Rights', screen: 'users' },
    { to: '/settings/flow', label: 'Flow Settings', screen: 'flow' },
    { to: '/settings/zoho', label: 'Zoho Books', screen: 'zoho' },
    { to: '/settings/templates', label: 'Message Templates', screen: 'templates' },
    { to: '/settings/activity', label: 'Activity Log', screen: 'activity' },
  ]
  // A screen shows when the user's roles give at least view on it (screen levels from /me).
  const sees = (n: NavItem) => !n.screen || !!me?.is_admin || (me?.screens?.[n.screen] || 'none') !== 'none'
  const shown = (n: NavItem) => n.show !== false && sees(n)
  const link = (n: NavItem, cls = '') => !shown(n) ? null : (
    <NavLink key={n.to} to={n.to} end={n.to === '/'} className={({ isActive }) => cls + (isActive ? ' active' : '')}>
      <span>{n.label}</span>{n.badge && counts[n.badge] ? <span className="badge">{counts[n.badge]}</span> : null}
    </NavLink>
  )
  const roleName = me?.is_admin ? 'System Admin' : me?.roles.map((r) => r.name).join(' + ')

  return (
    <div className={'shell' + (open ? ' navopen' : '')}>
      <aside className="side">
        <div className="brand">Purchase Desk<small>Saltstayz</small></div>
        {groups.filter((g) => g.some(shown)).map((g, i) => <div key={i}>{i > 0 && <div className="sep" />}{g.map((n) => link(n))}</div>)}
        {settings.some(shown) && <>
          <div className="sep" />
          <button onClick={() => setSettingsOpen(!settingsOpen)} aria-expanded={settingsOpen}><span>Settings</span><span>{settingsOpen ? '▾' : '▸'}</span></button>
          {settingsOpen && settings.map((n) => link(n, 'subitem'))}
        </>}
      </aside>
      <div className="shade" onClick={() => setOpen(false)} />
      <div style={{ minWidth: 0 }}>
        <div className="top">
          <button className="btn ghost menu-btn" onClick={() => setOpen(true)} aria-label="Open menu">Menu</button>
          {hasRight('raise') && (hasStep('creq', 'dh') && !me?.is_admin ? <>
            <button className="btn" onClick={() => nav('/corporate/new/purchase')}>+ Corporate purchase</button>
            <button className="btn ghost" onClick={() => nav('/corporate/new/bill')}>+ Corporate bill</button>
          </> : <>
            <button className="btn" onClick={() => nav('/requests/new')}>+ Purchase request</button>
            <button className="btn ghost" onClick={() => nav('/bills/new/utility')}>+ Bill</button>
          </>)}
          <input type="search" placeholder="Find PR, PO or invoice no. and press Enter" aria-label="Find a request, PO or invoice" style={{ maxWidth: 300 }}
            onKeyDown={async (e) => {
              if (e.key !== 'Enter') return
              const q = (e.target as HTMLInputElement).value.trim()
              if (!q) return
              const hits = await get<{ id: number; label: string }[]>('/search?q=' + encodeURIComponent(q))
              if (hits.length === 0) toast(`No request, PO or invoice matches "${q}"`)
              else nav('/documents/' + hits[0].id)
            }} />
          <span className="sp" />
          <button className="btn ghost small theme" title="Light or dark" onClick={() => setTheme(theme === 'dark' ? 'light' : theme === 'light' ? '' : 'dark')}>
            {theme === 'dark' ? 'Dark' : theme === 'light' ? 'Light' : 'Auto'}</button>
          <span className="who"><span>{me?.name}</span><span className="who-role">· {roleName}</span></span>
          <button className="btn ghost small" onClick={signOut}>Sign out</button>
        </div>
        <main className="page">{children}</main>
      </div>
    </div>
  )
}
