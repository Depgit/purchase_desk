// The frame around every screen: the left navigation (with counts of what waits for you) and the top bar.
import { useEffect, useState, type ReactNode } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router-dom'
import { get } from '../api/client'
import { useSession } from '../auth/session'
import { useToast } from '../ui/Toast'
import { PHASE, usePhase } from '../releasephase/phase'

interface NavItem { to: string; label: string; badge?: string; show?: boolean; phase?: number }

export function Shell({ children }: { children: ReactNode }) {
  const { me, signOut, hasRight, hasStep } = useSession()
  const { on } = usePhase()
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
    [{ to: '/', label: on(PHASE.dashboard) ? 'Dashboard' : 'My Work' }],
    [
      { to: '/approvals', label: 'Approvals', badge: 'approvals', show: hasStep('l2', 'cc', 'preop') },
      { to: '/requests', label: 'Purchase Requests', badge: 'requests' },
      { to: '/purchase-orders', label: 'Purchase Orders', badge: 'purchase_orders' },
      { to: '/vendor-sheets', label: 'Vendor Order Sheets', show: hasStep('cc', 'purchase'), phase: PHASE.sheets },
      { to: '/goods-receipts', label: 'Goods Receipts', badge: 'goods_receipts' },
      { to: '/invoices', label: 'Invoices', badge: 'invoices' },
      { to: '/payments', label: 'Payments', badge: 'payments' },
    ],
    [
      { to: '/bills/utility', label: 'Utility Bills', badge: 'bills_utility', phase: PHASE.bills },
      { to: '/bills/amc', label: 'AMC Bills', badge: 'bills_amc', phase: PHASE.bills },
    ],
    [
      { to: '/corporate', label: 'Corporate', badge: 'corporate', phase: PHASE.corporate },
      { to: '/warehouse', label: 'Warehouse', badge: 'warehouse', phase: PHASE.warehouse },
    ],
    [
      { to: '/vendors', label: 'Vendors', badge: 'vendors' },
      { to: '/items', label: 'Items (SKU)' },
      { to: '/vendor-mapping', label: 'Vendor Mapping' },
    ],
    [{ to: '/reports', label: 'Reports', phase: PHASE.reports }],
  ]
  const settings: NavItem[] = [
    { to: '/settings/properties', label: 'Properties' },
    { to: '/settings/cities', label: 'Cities & Clusters' },
    { to: '/settings/budgets', label: 'Budgets', phase: PHASE.budgets },
    { to: '/settings/corporate', label: 'Corporate Setup', phase: PHASE.corporate },
    { to: '/settings/bill-types', label: 'Bill Types', phase: PHASE.bills },
    { to: '/settings/item-setup', label: 'Item Setup' },
    { to: '/settings/users', label: 'Users & Rights', show: me?.is_admin || hasRight('masters') || !!me?.creatable_roles?.length },
    { to: '/settings/flow', label: 'Flow Settings' },
    { to: '/settings/zoho', label: 'Zoho Books', phase: PHASE.zoho },
    { to: '/settings/templates', label: 'Message Templates' },
    { to: '/settings/activity', label: 'Activity Log', show: me?.is_admin || hasRight('masters') || hasStep('finance', 'cc') },
  ]
  const link = (n: NavItem, cls = '') => n.show === false || (n.phase && !on(n.phase)) ? null : (
    <NavLink key={n.to} to={n.to} end={n.to === '/'} className={({ isActive }) => cls + (isActive ? ' active' : '')}>
      <span>{n.label}</span>{n.badge && counts[n.badge] ? <span className="badge">{counts[n.badge]}</span> : null}
    </NavLink>
  )
  const roleName = me?.is_admin ? 'System Admin' : me?.roles.map((r) => r.name).join(' + ')

  return (
    <div className={'shell' + (open ? ' navopen' : '')}>
      <aside className="side">
        <div className="brand">Purchase Desk<small>Saltstayz</small></div>
        {groups.filter((g) => g.some((n) => n.show !== false && (!n.phase || on(n.phase)))).map((g, i) => <div key={i}>{i > 0 && <div className="sep" />}{g.map((n) => link(n))}</div>)}
        <div className="sep" />
        <button onClick={() => setSettingsOpen(!settingsOpen)} aria-expanded={settingsOpen}><span>Settings</span><span>{settingsOpen ? '▾' : '▸'}</span></button>
        {settingsOpen && settings.map((n) => link(n, 'subitem'))}
      </aside>
      <div className="shade" onClick={() => setOpen(false)} />
      <div style={{ minWidth: 0 }}>
        <div className="top">
          <button className="btn ghost menu-btn" onClick={() => setOpen(true)} aria-label="Open menu">Menu</button>
          {hasRight('raise') && (on(PHASE.corporate) && hasStep('creq', 'dh') && !me?.is_admin ? <>
            <button className="btn" onClick={() => nav('/corporate/new/purchase')}>+ Corporate purchase</button>
            <button className="btn ghost" onClick={() => nav('/corporate/new/bill')}>+ Corporate bill</button>
          </> : <>
            <button className="btn" onClick={() => nav('/requests/new')}>+ Purchase request</button>
            {on(PHASE.bills) && <button className="btn ghost" onClick={() => nav('/bills/new/utility')}>+ Bill</button>}
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
