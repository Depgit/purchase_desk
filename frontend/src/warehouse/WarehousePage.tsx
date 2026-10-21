// The central warehouse: stock ledger, orders to dispatch, delivery schedule, in transit, incoming POs,
// stock movements and delivery partners.
import { useLookups } from '../api/lookups'
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { put } from '../api/client'
import type { Doc } from '../api/types'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { UploadModal } from '../bulkupload/UploadModal'
import { dateTime, day, inr, qty } from '../format/format'
import { ActiveToggle } from '../ui/ActiveToggle'
import { DataTable } from '../ui/DataTable'
import { Pill } from '../ui/Pill'
import { useToast } from '../ui/Toast'
import { PartnerModal, ScheduleModal, type Partner } from './DispatchPanel'
import { useScreen } from '../screenaccess/screen'

interface Stock { sku_id: number; sku_code: string; item: string; brand: string; pack_size: string; sub_category: string; opening: number; purchased: number; issued: number; pilferage: number; in_transit: number; closing: number; min_level: number; rate: number; value: number; pilferage_value: number; status: string
  /** due: on approved transfers not yet sent; on_order: the warehouse's own open POs; short: still to buy. */
  due: number; on_order: number; short: number; wh_stocked: boolean }
type T = Doc & { status_label: string; short_lines: number; age_hours: number }
interface Move { at: string; kind: string; ref: string; document_id: number; party: string; sku_code: string; item: string; qty: number; value: number }
type Tab = 'stock' | 'dispatch' | 'schedule' | 'transit' | 'incoming' | 'moves' | 'partners'

export function WarehousePage() {
  const nav = useNavigate()
  const { hasStep, hasRight } = useSession()
  const sc = useScreen('warehouse')
  const wh = sc.edit && hasStep('whl2') // stock, reorder levels, delivery partners: the Warehouse Approver L2
  const dispatcher = sc.edit && hasStep('whl1', 'whl2') // either Warehouse Approver schedules and dispatches
  const [tab, setTab] = useState<Tab>('stock')
  const stock = useApi<Stock[]>('/warehouse/stock')
  const tr = useApi<T[]>('/warehouse/transfers')
  const [sel, setSel] = useState<Set<string | number>>(new Set())
  const [buy, setBuy] = useState<Set<string | number>>(new Set()) // items ticked to buy for the shortfall
  const l = useLookups()
  const whProp = (l?.properties || []).find((p) => p.kind === 'warehouse' && p.active)
  const raises = hasStep('whl2', 'whs') && hasRight('raise')
  // The shortfall goes into a new warehouse request, pre-filled with the missing quantities (rounded up).
  const raiseShort = (items: Stock[]) => whProp && nav(`/requests/new?property=${whProp.id}&lines=${items.map((s) => `${s.sku_id}:${Math.ceil(s.short)}`).join(',')}`)
  const [trip, setTrip] = useState(false)
  const [upload, setUpload] = useState(false)
  const all = tr.data || []
  const toDispatch = all.filter((d) => d.is_transfer && d.status === 'PO_ISSUED')
  const scheduled = all.filter((d) => d.is_transfer && d.delivery && ['PO_ISSUED', 'WH_SENT'].includes(d.status)).sort((a, b) => (a.delivery!.date + a.delivery!.slot).localeCompare(b.delivery!.date + b.delivery!.slot))
  const transit = all.filter((d) => d.status === 'WH_SENT')
  const incoming = all.filter((d) => !d.is_transfer && d.status === 'PO_ISSUED')
  const st = stock.data || []
  const value = (f: (s: Stock) => number) => st.reduce((t, s) => t + f(s) * s.rate, 0)
  const tile = (t: Tab, label: string, v: string | number, bad?: boolean) => <button className={'tile' + (tab === t ? ' on' : '')} onClick={() => setTab(t)}><span>{label}</span><b style={bad ? { color: 'var(--bad)' } : undefined}>{v}</b></button>
  const reload = () => { stock.reload(); tr.reload() }
  return (
    <>
      <div className="head"><div><h1>Warehouse</h1><p className="sub">Opening + purchased − issued to properties − pilferage − in transit = closing stock. Movements come from goods receipts and transfer orders, so the ledger always reconciles.</p></div>
        <div className="acts">{wh && hasRight('upload') && <button className="btn ghost" onClick={() => setUpload(true)}>Upload opening stock</button>}
          {wh && <button className="btn" onClick={() => nav('/requests/new')}>+ Stock request</button>}</div></div>
      <div className="tiles">
        {tile('stock', 'Stock on hand', inr(value((s) => s.closing)))}
        {tile('dispatch', 'Orders to dispatch', toDispatch.length)}
        {tile('dispatch', 'Not yet scheduled', toDispatch.filter((d) => !d.delivery).length, toDispatch.some((d) => !d.delivery))}
        {tile('stock', 'Items short for orders', (stock.data || []).filter((s) => s.short > 0).length, (stock.data || []).some((s) => s.short > 0))}
        {tile('incoming', 'Deliveries to receive', incoming.length)}
        {tile('transit', 'In transit', inr(value((s) => s.in_transit)))}
        {tile('moves', 'Pilferage booked', inr(value((s) => s.pilferage)), st.some((s) => s.pilferage > 0))}
      </div>
      <div className="tabs">{([['stock', 'Stock ledger'], ['dispatch', `To dispatch (${toDispatch.length})`], ['schedule', `Delivery schedule (${scheduled.length})`], ['transit', `In transit (${transit.length})`],
        ['incoming', `Incoming to warehouse (${incoming.length})`], ['moves', 'Movements'], ['partners', 'Delivery partners']] as [Tab, string][]).map(([k, l]) =>
        <button key={k} className={tab === k ? 'on' : ''} onClick={() => setTab(k)}>{l}</button>)}</div>
      {tab === 'stock' && st.some((s) => s.short > 0) && <p className="note warn"><b>Properties have ordered more than the warehouse holds.</b> Short items are what is still to buy after stock and the warehouse's own open orders. {raises ? 'Tick them and raise a purchase for the shortfall; what is in stock can be sent now, the rest follows when it arrives.' : 'The Warehouse Approver L2 or Warehouse Staff buy them.'}</p>}
      {tab === 'stock' && raises && buy.size > 0 && <div className="tools"><span style={{ flex: 1 }} /><button className="btn" disabled={!whProp} onClick={() => raiseShort(st.filter((s) => buy.has(s.sku_id) && s.short > 0))}>Raise purchase for shortfall ({buy.size})</button></div>}
      {tab === 'stock' && <DataTable screen="wh-stock" rows={st} rowKey={(s) => s.sku_id} search={(s) => [s.sku_code, s.item, s.brand].join(' ')}
        selectable={raises ? (s) => s.short > 0 : undefined} selected={buy} onSelect={setBuy}
        filters={[{ id: 'low', label: 'Level', options: [['', 'All items'], ['1', 'Below reorder level'], ['short', 'Short for orders'], ['stocked', 'Stocked at warehouse']],
          test: (s, v) => v === 'short' ? s.short > 0 : v === 'stocked' ? s.wh_stocked : s.closing < s.min_level }]}
        columns={[
          { key: 'sku', label: 'SKU', render: (s) => s.sku_code, csv: (s) => s.sku_code },
          { key: 'item', label: 'Item', render: (s) => <>{s.brand} {s.item} <span className="hint">{s.pack_size}</span></>, csv: (s) => s.item },
          { key: 'open', label: 'Opening', right: true, render: (s) => qty(s.opening), sort: (s) => s.opening, csv: (s) => s.opening },
          { key: 'pur', label: 'Purchased', right: true, render: (s) => qty(s.purchased), sort: (s) => s.purchased, csv: (s) => s.purchased },
          { key: 'iss', label: 'Issued', right: true, render: (s) => qty(s.issued), sort: (s) => s.issued, csv: (s) => s.issued },
          { key: 'pil', label: 'Pilferage', right: true, render: (s) => s.pilferage ? <Pill tone="bad">{qty(s.pilferage)}</Pill> : 0, sort: (s) => s.pilferage, csv: (s) => s.pilferage },
          { key: 'tr', label: 'In transit', right: true, render: (s) => qty(s.in_transit), sort: (s) => s.in_transit, csv: (s) => s.in_transit },
          { key: 'close', label: 'Closing', right: true, render: (s) => <b>{qty(s.closing)}</b>, sort: (s) => s.closing, csv: (s) => s.closing },
          { key: 'due', label: 'Ordered by properties', right: true, render: (s) => s.due ? qty(s.due) : '—', sort: (s) => s.due, csv: (s) => s.due },
          { key: 'ord', label: 'On order', right: true, render: (s) => s.on_order ? qty(s.on_order) : '—', sort: (s) => s.on_order, csv: (s) => s.on_order },
          { key: 'short', label: 'Short', right: true, render: (s) => s.short > 0 ? <Pill tone="bad">{qty(s.short)}</Pill> : '—', sort: (s) => s.short, csv: (s) => s.short },
          { key: 'min', label: 'Reorder level', right: true, render: (s) => wh ? <MinLevel s={s} onDone={stock.reload} /> : qty(s.min_level), sort: (s) => s.min_level, csv: (s) => s.min_level },
          { key: 'val', label: 'Closing value', right: true, render: (s) => inr(s.value), sort: (s) => s.value, csv: (s) => s.value },
          { key: 'st', label: 'Status', render: (s) => <Pill tone={s.status === 'OK' ? 'ok' : s.status === 'Reorder' ? 'warn' : 'bad'}>{s.status}</Pill>, csv: (s) => s.status },
        ]} csvName="warehouse-stock" />}
      {tab === 'dispatch' && <>
        <p className="sub">Orders the properties are waiting for. Schedule each delivery with a partner, date and slot, then dispatch from the order. Tick several to put them on one trip.</p>
        <DataTable screen="wh-dispatch" rows={toDispatch} rowKey={(d) => d.id} date={{ label: 'Raised', value: (d) => d.created_at }} onOpen={(d) => nav('/documents/' + d.id)} selectable={dispatcher ? () => true : undefined} selected={sel} onSelect={setSel}
          actions={dispatcher ? <button className="btn" disabled={!sel.size} onClick={() => setTrip(true)}>Schedule {sel.size || ''} selected on one trip</button> : null}
          search={(d) => [d.po_number, d.number, d.property].join(' ')}
          columns={[
            { key: 'wt', label: 'Order', render: (d) => <>{d.po_number}<br /><span className="hint">{d.number}</span></>, csv: (d) => d.po_number },
            { key: 'prop', label: 'Property', render: (d) => <>{d.property}<br /><span className="hint">{d.city}</span></>, csv: (d) => d.property },
            { key: 'items', label: 'Items', render: (d) => d.lines.map((l) => `${l.item_name} × ${qty(l.qty)}`).join(', '), csv: (d) => d.lines.length },
            { key: 'val', label: 'Value', right: true, render: (d) => inr(d.total), sort: (d) => d.total, csv: (d) => d.total },
            { key: 'stock', label: 'Stock', render: (d) => d.short_lines ? <Pill tone="bad">{d.short_lines} of {d.lines.length} short</Pill> : <Pill tone="ok">In stock</Pill> },
            { key: 'dlv', label: 'Delivery', render: (d) => d.delivery ? `${day(d.delivery.date)}, ${d.delivery.slot} · ${d.delivery.partner}` : d.wait_until ? <Pill tone="warn">Awaiting stock, by {day(d.wait_until)}</Pill> : <Pill tone="warn">Not scheduled</Pill>,
              sort: (d) => d.delivery?.date || '9', csv: (d) => d.delivery?.date || '' },
            { key: 'age', label: 'Waiting', right: true, render: (d) => `${Math.floor(d.age_hours / 24)} d`, sort: (d) => d.age_hours },
          ]} csvName="warehouse-dispatch" /></>}
      {tab === 'schedule' && <DataTable screen="wh-schedule" rows={scheduled} rowKey={(d) => d.id} date={{ label: 'Delivery date', value: (d) => d.delivery?.date }} onOpen={(d) => nav('/documents/' + d.id)} search={(d) => [d.po_number, d.property, d.delivery?.partner].join(' ')}
        columns={[
          { key: 'date', label: 'Date', render: (d) => day(d.delivery!.date), sort: (d) => d.delivery!.date + d.delivery!.slot, csv: (d) => d.delivery!.date },
          { key: 'slot', label: 'Time slot', render: (d) => d.delivery!.slot, csv: (d) => d.delivery!.slot },
          { key: 'wt', label: 'Order', render: (d) => d.po_number, csv: (d) => d.po_number },
          { key: 'prop', label: 'Property', render: (d) => d.property, csv: (d) => d.property },
          { key: 'partner', label: 'Delivery partner', render: (d) => d.delivery!.partner, csv: (d) => d.delivery!.partner },
          { key: 'veh', label: 'Vehicle', render: (d) => d.delivery!.vehicle || '—', csv: (d) => d.delivery!.vehicle },
          { key: 'st', label: 'Status', render: (d) => d.status === 'WH_SENT' ? <Pill tone="ok">On the way</Pill> : <Pill>{d.delivery!.version > 1 ? 'Rescheduled' : 'Scheduled'}</Pill> },
        ]} csvName="delivery-schedule" />}
      {(tab === 'transit' || tab === 'incoming') && <DataTable screen={'wh-' + tab} rows={tab === 'transit' ? transit : incoming} rowKey={(d) => d.id} date={{ label: tab === 'transit' ? 'Dispatched' : 'Raised', value: (d) => tab === 'transit' ? d.dispatched_at : d.created_at }} onOpen={(d) => nav('/documents/' + d.id)}
        search={(d) => [d.po_number, d.property, d.vendor].join(' ')} empty={tab === 'transit' ? 'Nothing is in transit.' : 'No delivery is due at the warehouse or a pre-opening property.'}
        columns={[
          { key: 'po', label: 'Order', render: (d) => d.po_number, csv: (d) => d.po_number },
          { key: 'prop', label: tab === 'transit' ? 'Property' : 'Deliver to', render: (d) => d.property, csv: (d) => d.property },
          { key: 'who', label: tab === 'transit' ? 'Delivery' : 'Vendor', render: (d) => tab === 'transit' ? (d.delivery ? `${d.delivery.partner} · ${d.delivery.slot}` : '—') : d.vendor },
          { key: 'items', label: 'Items', render: (d) => d.lines.map((l) => `${l.item_name} × ${qty(tab === 'transit' ? l.dispatched_qty ?? 0 : l.qty)}`).join(', ') },
          { key: 'val', label: 'Value', right: true, render: (d) => inr(d.total), sort: (d) => d.total, csv: (d) => d.total },
          { key: 'st', label: 'Status', render: (d) => <Pill tone="warn">{d.status_label}</Pill> },
        ]} csvName={'warehouse-' + tab} />}
      {tab === 'moves' && <Movements />}
      {tab === 'partners' && <Partners canEdit={wh} />}
      {trip && <ScheduleModal ids={[...sel].map(Number)} onClose={() => setTrip(false)} onDone={() => { setTrip(false); setSel(new Set()); reload() }} />}
      {upload && <UploadModal kind="warehouse_stock" title="Upload warehouse opening stock" onClose={() => setUpload(false)} onDone={reload} />}
    </>
  )
}

function MinLevel({ s, onDone }: { s: Stock; onDone: () => void }) {
  const toast = useToast()
  return <input className="qty" type="number" min="0" defaultValue={s.min_level} key={s.min_level} aria-label={'Reorder level ' + s.sku_code}
    onBlur={async (e) => { const v = +e.target.value; if (v === s.min_level) return
      try { await put(`/warehouse/stock/${s.sku_id}`, { min_level: v }); toast('Reorder level saved'); onDone() } catch (err: any) { toast(err.message, true) } }} />
}

function Movements() {
  const nav = useNavigate()
  const { data } = useApi<Move[]>('/warehouse/movements')
  return <DataTable screen="wh-moves" rows={data || []} rowKey={(m) => m.kind + m.ref + m.sku_code + m.at} date={{ label: 'Date', value: (m) => m.at }} onOpen={(m) => nav('/documents/' + m.document_id)} search={(m) => [m.kind, m.ref, m.party, m.sku_code, m.item].join(' ')}
    filters={[{ id: 'k', label: 'Movement', options: [['', 'All movements'], ['Purchase received', 'Purchase received'], ['Dispatched to property', 'Dispatched to property'], ['Pilferage', 'Pilferage']], test: (m, v) => m.kind === v }]}
    columns={[
      { key: 'at', label: 'Date', render: (m) => dateTime(m.at), sort: (m) => m.at, csv: (m) => m.at },
      { key: 'kind', label: 'Movement', render: (m) => m.kind === 'Pilferage' ? <Pill tone="bad">{m.kind}</Pill> : m.kind, csv: (m) => m.kind },
      { key: 'ref', label: 'Reference', render: (m) => m.ref, csv: (m) => m.ref },
      { key: 'party', label: 'Vendor or property', render: (m) => m.party, csv: (m) => m.party },
      { key: 'sku', label: 'SKU', render: (m) => m.sku_code, csv: (m) => m.sku_code },
      { key: 'item', label: 'Item', render: (m) => m.item, csv: (m) => m.item },
      { key: 'qty', label: 'Quantity', right: true, render: (m) => qty(m.qty), sort: (m) => m.qty, csv: (m) => m.qty },
      { key: 'val', label: 'Value', right: true, render: (m) => inr(m.value), sort: (m) => m.value, csv: (m) => m.value },
    ]} csvName="warehouse-movements" />
}

function Partners({ canEdit }: { canEdit: boolean }) {
  const { data, reload } = useApi<Partner[]>('/delivery-partners')
  const [edit, setEdit] = useState<Partner | 'new' | null>(null)
  return (
    <>
      <DataTable screen="wh-partners" rows={data || []} rowKey={(p) => p.id} dim={(p) => !p.active} search={(p) => [p.name, p.driver, p.phone].join(' ')}
        actions={canEdit ? <button className="btn" onClick={() => setEdit('new')}>+ Add delivery partner</button> : null}
        columns={[
          { key: 'name', label: 'Name', render: (p) => p.name, csv: (p) => p.name },
          { key: 'kind', label: 'Type', render: (p) => p.kind === 'own' ? 'Own vehicle' : 'Delivery partner', csv: (p) => p.kind },
          { key: 'driver', label: 'Driver or contact', render: (p) => p.driver, csv: (p) => p.driver },
          { key: 'phone', label: 'Phone', render: (p) => p.phone, csv: (p) => p.phone },
          { key: 'open', label: 'Open deliveries', right: true, render: (p) => p.open_deliveries, csv: (p) => p.open_deliveries },
          { key: 'st', label: 'Status', render: (p) => <ActiveToggle kind="partner" id={p.id} name={p.name} active={p.active} onDone={reload} canEdit={canEdit} /> },
          { key: 'edit', label: '', render: (p) => canEdit ? <button className="link" onClick={() => setEdit(p)}>Edit</button> : null },
        ]} csvName="delivery-partners" />
      {edit && <PartnerModal p={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onDone={() => { setEdit(null); reload() }} />}
    </>
  )
}

