// Who supplies what, where. One item has one vendor at a property; a SKU mapping wins over a sub-category mapping.
import { useEffect, useState } from 'react'
import { download, post, put } from '../api/client'
import { useLookups } from '../api/lookups'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { UploadModal } from '../bulkupload/UploadModal'
import { inr, today } from '../format/format'
import { ActiveToggle } from '../ui/ActiveToggle'
import { DataTable } from '../ui/DataTable'
import { Errors } from '../ui/Errors'
import { Checks, Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { Tabs } from '../ui/Tabs'
import { useAction } from '../ui/useAction'
import type { Vendor } from '../vendors/VendorForm'
import type { SKU } from '../items/ItemsPage'

interface Mapping {
  id: number; vendor_id: number; vendor: string; vendor_usable: boolean; level: 'sku' | 'sc'; sku_id: number | null; sku_code: string; item: string
  sub_category_id: number; sub_category: string; category: string; all_properties: boolean; property_ids: number[]; estimate_allowed: boolean; active: boolean
  rated_skus: number; total_skus: number; has_rate: boolean
}
interface Rate { id: number; vendor_id: number; vendor: string; sku_id: number; sku_code: string; item: string; pack_size: string; sub_category: string; category: string; rate: number; gst_pct: number; valid_from: string; valid_to: string; status: string; days_left: number }
interface Source { sku_id: number; sku_code: string; item_name: string; brand: string; pack_size: string; sub_category_id: number; sub_category: string; category: string; level: string; vendor_id: number | null; vendor_name: string; rate: number | null; orderable: boolean; status: string; problem: string; estimate_allowed: boolean }

type Tab = 'sc' | 'sku' | 'rates' | 'check'

export function VendorMappingPage() {
  const [tab, setTab] = useState<Tab>('sc')
  return (
    <>
      <div className="head"><div><h1>Vendor Mapping</h1><p className="sub">Which vendor supplies each item at each property, and at what rate.</p></div></div>
      <Tabs value={tab} onChange={setTab} items={[['sc', 'Sub-category mapping'], ['sku', 'SKU mapping'], ['rates', 'SKU rates'], ['check', 'Check a property']]} />
      {tab !== 'check' && tab !== 'rates' && <p className="note"><b>One item has one vendor at a property.</b> Map a vendor to a whole sub-category for the properties it serves. Where one item comes from someone else at some properties (for example the Central Warehouse for Gurugram), add a SKU mapping: it wins over the sub-category mapping.</p>}
      {tab === 'sc' && <Mappings level="sc" />}
      {tab === 'sku' && <Mappings level="sku" />}
      {tab === 'rates' && <Rates />}
      {tab === 'check' && <Check />}
    </>
  )
}

function propsText(m: Mapping, l: ReturnType<typeof useLookups>) {
  if (m.all_properties) return 'All properties'
  const left = new Set(m.property_ids)
  const out: string[] = []
  for (const c of l?.clusters || []) {
    const members = (l?.properties || []).filter((p) => p.cluster_id === c.id && p.active && p.kind === 'live').map((p) => p.id)
    if (members.length && members.every((p) => left.has(p))) { out.push(`${c.name} (${members.length})`); members.forEach((p) => left.delete(p)) }
  }
  const rest = [...left]
  if (rest.length === 1) out.push(l?.properties.find((p) => p.id === rest[0])?.name || '1 property')
  else if (rest.length) out.push(`${rest.length} ${out.length ? 'other ' : ''}properties`)
  return out.join(' + ') || 'No property'
}

function Mappings({ level }: { level: 'sc' | 'sku' }) {
  const l = useLookups()
  const { hasRight } = useSession()
  const { data, reload } = useApi<Mapping[]>('/mappings?level=' + level)
  const [edit, setEdit] = useState<Mapping | 'new' | null>(null)
  const a = useAction()
  const ed = hasRight('masters')
  return (
    <>
      <DataTable screen={'mappings-' + level} rows={data || []} rowKey={(m) => m.id} dim={(m) => !m.active}
        search={(m) => [m.vendor, m.sub_category, m.sku_code, m.item].join(' ')}
        actions={ed ? <button className="btn" onClick={() => setEdit('new')}>+ Add mapping</button> : null}
        filters={[
          { id: 'p', label: 'Property', options: [['', 'Any property'], ...(l?.properties || []).filter((p) => p.active).map((p) => [String(p.id), p.name] as [string, string])], test: (m, v) => m.all_properties || m.property_ids.includes(+v) },
          { id: 'act', label: 'Active', options: [['', 'Active and inactive'], ['1', 'Active only'], ['0', 'Inactive only']], test: (m, v) => m.active === (v === '1') },
        ]}
        columns={[
          { key: 'vendor', label: 'Vendor', render: (m) => <>{m.vendor}{!m.vendor_usable && <> <Pill tone="warn">not usable</Pill></>}</>, csv: (m) => m.vendor },
          ...(level === 'sku' ? [{ key: 'sku', label: 'SKU', render: (m: Mapping) => <>{m.sku_code} <span className="hint">{m.item}</span></>, csv: (m: Mapping) => m.sku_code }] : []),
          { key: 'sub', label: 'Sub-category', render: (m) => m.sub_category, csv: (m) => m.sub_category },
          { key: 'cat', label: 'Category', render: (m) => m.category, csv: (m) => m.category },
          { key: 'props', label: 'Properties', render: (m) => <>{propsText(m, l)}{ed && m.active && <> <button className="link" onClick={() => setEdit(m)}>Change</button></>}</>, csv: (m) => propsText(m, l) },
          level === 'sc'
            ? { key: 'rated', label: 'SKUs with valid rate', right: true, render: (m) => `${m.rated_skus} of ${m.total_skus}`, sort: (m) => m.rated_skus }
            : { key: 'rate', label: 'Rate', render: (m) => m.has_rate ? <Pill tone="ok">Valid rate</Pill> : <Pill tone="warn">No valid rate, estimate used</Pill> },
          ...(level === 'sc' ? [{ key: 'est', label: 'Rates', render: (m: Mapping) => <>{m.estimate_allowed ? <Pill tone="ok">Estimate allowed</Pill> : <Pill>SKU rate needed</Pill>}
            {ed && m.active && <> <button className="link" onClick={() => a.run(() => put(`/mappings/${m.id}/estimate`, { estimate_allowed: !m.estimate_allowed }), () => { reload(); return 'Rate rule changed' })}>{m.estimate_allowed ? 'Require SKU rates' : 'Allow estimate'}</button></>}</> }] : []),
          { key: 'status', label: 'Status', render: (m) => <ActiveToggle kind="mapping" id={m.id} name={`${m.vendor} / ${m.sku_code || m.sub_category}`} active={m.active} onDone={reload} />, csv: (m) => m.active ? 'Active' : 'Inactive' },
        ]} csvName={'vendor-mapping-' + level} />
      {edit && <MappingModal level={level} m={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onDone={() => { setEdit(null); reload() }} />}
    </>
  )
}

function MappingModal({ level, m, onClose, onDone }: { level: 'sc' | 'sku'; m: Mapping | null; onClose: () => void; onDone: () => void }) {
  const l = useLookups()
  const vendors = useApi<Vendor[]>('/vendors')
  const skus = useApi<SKU[]>(level === 'sku' ? '/skus' : null)
  const [f, setF] = useState({ vendor_id: m?.vendor_id || 0, sku_id: m?.sku_id || 0, sub_category_id: m?.sub_category_id || 0, all_properties: m?.all_properties ?? false,
    cluster_ids: [] as number[], property_ids: m?.property_ids || [], estimate_allowed: m?.estimate_allowed || false })
  const a = useAction()
  const gv = (vendors.data || []).filter((v) => v.active && (v.vendor_type === 'goods' || v.vendor_type === 'warehouse'))
  return (
    <Modal title={m ? `Change properties for ${m.vendor} / ${m.sku_code || m.sub_category}` : 'Map a vendor to properties'} onClose={onClose} wide>
      {!m && <div className="grid2">
        <Field label="Vendor"><select value={f.vendor_id} onChange={(e) => setF({ ...f, vendor_id: +e.target.value })}><option value={0}>Choose vendor</option>{gv.map((v) => <option key={v.id} value={v.id}>{v.legal_name}</option>)}</select></Field>
        {level === 'sku'
          ? <Field label="SKU"><select value={f.sku_id} onChange={(e) => setF({ ...f, sku_id: +e.target.value })}><option value={0}>Choose SKU</option>{(skus.data || []).filter((s) => s.active).map((s) => <option key={s.id} value={s.id}>{s.code} · {s.item_name}</option>)}</select></Field>
          : <Field label="Sub-category"><select value={f.sub_category_id} onChange={(e) => setF({ ...f, sub_category_id: +e.target.value })}><option value={0}>Choose</option>{(l?.subCategories || []).filter((s) => s.active).map((s) => <option key={s.id} value={s.id}>{s.name} ({s.category})</option>)}</select></Field>}
      </div>}
      {level === 'sc' && <Field label="Rates" hint="With an estimate, the requester enters the rate and Purchase confirms it on the PO.">
        <select value={f.estimate_allowed ? 'y' : 'n'} onChange={(e) => setF({ ...f, estimate_allowed: e.target.value === 'y' })}><option value="n">A valid SKU rate is needed for each item</option><option value="y">Allow an estimated rate where there is no SKU rate</option></select></Field>}
      <Field label="Properties" hint="All properties also covers the warehouse, pre-opening properties and any property added later.">
        <select value={f.all_properties ? 'all' : 'sel'} onChange={(e) => setF({ ...f, all_properties: e.target.value === 'all' })}><option value="all">All properties</option><option value="sel">Choose clusters or properties</option></select></Field>
      {!f.all_properties && <>
        <div className="field"><span>Whole clusters</span><Checks value={f.cluster_ids} onChange={(v) => setF({ ...f, cluster_ids: v })} options={(l?.clusters || []).map((c) => [c.id, c.name])} /><span className="hint">Ticks every live property in the cluster.</span></div>
        <div className="field"><span>Individual properties</span><Checks value={f.property_ids} onChange={(v) => setF({ ...f, property_ids: v })} options={(l?.properties || []).filter((p) => p.active).map((p) => [p.id, p.name])} /></div>
      </>}
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => m ? put('/mappings/' + m.id, { ...f, level }) : post('/mappings', { ...f, level }), () => { onDone(); return 'Mapping saved' })}>Save mapping</button></div>
    </Modal>
  )
}

function Rates() {
  const { hasRight } = useSession()
  const { data, reload } = useApi<Rate[]>('/rates')
  const vendors = useApi<Vendor[]>('/vendors')
  const l = useLookups()
  const [add, setAdd] = useState(false)
  const [fmt, setFmt] = useState(false)
  const [upload, setUpload] = useState(false)
  const ed = hasRight('masters')
  const tone = (s: string) => s === 'Active' ? 'ok' : s === 'Expiring soon' ? 'warn' : s === 'Expired' ? 'bad' : s === 'Superseded' ? 'off' : ''
  return (
    <>
      <p className="note"><b>Three steps for a vendor price list:</b> 1. Download the rate format for the vendor. 2. The vendor fills the new rate and validity against each SKU. 3. Bulk upload: every row is checked, and only clean rows are saved.</p>
      <DataTable screen="rates" rows={data || []} rowKey={(r) => r.id} dim={(r) => r.status === 'Superseded'} search={(r) => [r.vendor, r.sku_code, r.item].join(' ')}
        actions={<><button className="btn ghost" onClick={() => setFmt(true)}>Rate format</button>{ed && <>{hasRight('upload') && <button className="btn ghost" onClick={() => setUpload(true)}>Bulk upload</button>}<button className="btn" onClick={() => setAdd(true)}>+ Add rate</button></>}</>}
        filters={[
          { id: 'v', label: 'Vendor', options: [['', 'All vendors'], ...(vendors.data || []).filter((v) => v.vendor_type === 'goods' || v.vendor_type === 'warehouse').map((v) => [String(v.id), v.legal_name] as [string, string])], test: (r, v) => String(r.vendor_id) === v },
          { id: 'st', label: 'Validity', options: [['', 'Any validity'], ['Active', 'Active'], ['Expiring soon', 'Expiring soon'], ['Expired', 'Expired'], ['Starts later', 'Starts later'], ['Superseded', 'Superseded']], test: (r, v) => r.status === v },
          { id: 'cat', label: 'Category', options: [['', 'All categories'], ...(l?.categories || []).map((c) => [c.name, c.name] as [string, string])], test: (r, v) => r.category === v },
        ]}
        columns={[
          { key: 'vendor', label: 'Vendor', render: (r) => r.vendor, csv: (r) => r.vendor },
          { key: 'sku', label: 'SKU', render: (r) => r.sku_code, csv: (r) => r.sku_code },
          { key: 'item', label: 'Item', render: (r) => <>{r.item} <span className="hint">{r.pack_size}</span></>, csv: (r) => r.item },
          { key: 'sub', label: 'Sub-category', render: (r) => r.sub_category, csv: (r) => r.sub_category },
          { key: 'rate', label: 'Rate ex GST', right: true, render: (r) => inr(r.rate), sort: (r) => r.rate, csv: (r) => r.rate },
          { key: 'gst', label: 'GST', right: true, render: (r) => r.gst_pct + '%', csv: (r) => r.gst_pct },
          { key: 'from', label: 'Valid from', render: (r) => r.valid_from, csv: (r) => r.valid_from },
          { key: 'to', label: 'Valid to', render: (r) => r.valid_to, csv: (r) => r.valid_to },
          { key: 'status', label: 'Status', render: (r) => <Pill tone={tone(r.status) as any}>{r.status}</Pill>, csv: (r) => r.status },
        ]} csvName="vendor-rates" />
      {add && <RateModal onClose={() => setAdd(false)} onDone={() => { setAdd(false); reload() }} />}
      {fmt && <FormatModal onClose={() => setFmt(false)} />}
      {upload && <UploadModal kind="rates" title="Bulk upload vendor rates" onClose={() => setUpload(false)} onDone={reload} />}
    </>
  )
}

function RateModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const vendors = useApi<Vendor[]>('/vendors')
  const skus = useApi<SKU[]>('/skus')
  const [f, setF] = useState({ vendor_id: 0, sku_id: 0, rate: '', gst_pct: 18, valid_from: today(), valid_to: '' })
  const v = vendors.data?.find((x) => x.id === f.vendor_id)
  const list = (skus.data || []).filter((s) => s.active && (!v || v.vendor_type === 'warehouse' || v.sub_category_ids.includes(s.sub_category_id)))
  useEffect(() => { const s = skus.data?.find((x) => x.id === f.sku_id); if (s) setF((x) => ({ ...x, gst_pct: s.gst_pct })) }, [f.sku_id, skus.data])
  const a = useAction()
  return (
    <Modal title="Add vendor rate" onClose={onClose}>
      <div className="grid2">
        <Field label="Vendor"><select value={f.vendor_id} onChange={(e) => setF({ ...f, vendor_id: +e.target.value, sku_id: 0 })}><option value={0}>Choose</option>{(vendors.data || []).filter((x) => x.active && (x.vendor_type === 'goods' || x.vendor_type === 'warehouse')).map((x) => <option key={x.id} value={x.id}>{x.legal_name}</option>)}</select></Field>
        <Field label="SKU" hint="Only SKUs in the vendor's approved sub-categories."><select value={f.sku_id} onChange={(e) => setF({ ...f, sku_id: +e.target.value })}><option value={0}>Choose</option>{list.map((s) => <option key={s.id} value={s.id}>{s.code} · {s.item_name}</option>)}</select></Field>
        <Field label="Rate ex GST (₹)"><input type="number" min="0" step="0.01" value={f.rate} onChange={(e) => setF({ ...f, rate: e.target.value })} /></Field>
        <Field label="GST %"><select value={f.gst_pct} onChange={(e) => setF({ ...f, gst_pct: +e.target.value })}>{[0, 5, 12, 18, 28, 40].map((g) => <option key={g} value={g}>{g}%</option>)}</select></Field>
        <Field label="Valid from"><input type="date" value={f.valid_from} onChange={(e) => setF({ ...f, valid_from: e.target.value })} /></Field>
        <Field label="Valid to"><input type="date" value={f.valid_to} onChange={(e) => setF({ ...f, valid_to: e.target.value })} /></Field>
      </div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post('/rates', { ...f, rate: +f.rate }), () => { onDone(); return 'Rate saved' })}>Save rate</button></div>
    </Modal>
  )
}

function FormatModal({ onClose }: { onClose: () => void }) {
  const vendors = useApi<Vendor[]>('/vendors')
  const l = useLookups()
  const [vid, setVid] = useState(0)
  const [cat, setCat] = useState(0)
  return (
    <Modal title="Rate format for a vendor" onClose={onClose}>
      <p className="sub">The file lists every active SKU the vendor is approved for, with the current rate. The vendor fills the new rate and validity. Rows without a rate are skipped on upload.</p>
      <div className="grid2">
        <Field label="Vendor"><select value={vid} onChange={(e) => setVid(+e.target.value)}><option value={0}>Choose</option>{(vendors.data || []).filter((v) => v.vendor_type === 'goods' && v.active).map((v) => <option key={v.id} value={v.id}>{v.legal_name}</option>)}</select></Field>
        <Field label="Category"><select value={cat} onChange={(e) => setCat(+e.target.value)}><option value={0}>All categories</option>{l?.categories.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}</select></Field>
      </div>
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={!vid} onClick={() => download(`/rates/format?vendor_id=${vid}&category_id=${cat}`, 'rate-format.csv')}>Download</button></div>
    </Modal>
  )
}

function Check() {
  const l = useLookups()
  const props = (l?.properties || []).filter((p) => p.active)
  const [pid, setPid] = useState(0)
  useEffect(() => { if (!pid && props.length) setPid(props[0].id) }, [props, pid])
  const { data } = useApi<Source[]>(pid ? `/mappings/check?property_id=${pid}` : null)
  const rows = data || []
  const miss = rows.filter((r) => !r.orderable).length
  return (
    <>
      <div className="tools">
        <select value={pid} onChange={(e) => setPid(+e.target.value)} aria-label="Property">{props.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select>
        <button className="btn ghost" onClick={() => download('/mappings/check-all', 'vendor-check-all-properties.csv')}>Download all properties</button>
      </div>
      {data && <p className={'note ' + (miss ? 'warn' : 'ok')}><b>{rows.length - miss} of {rows.length} items have a vendor.</b>{miss ? ` ${miss} cannot be ordered here until they are mapped.` : ''}</p>}
      <DataTable screen="mapping-check" rows={rows} rowKey={(r) => r.sku_id} search={(r) => [r.sku_code, r.item_name, r.brand, r.vendor_name].join(' ')}
        filters={[
          { id: 'cat', label: 'Category', options: [['', 'All categories'], ...(l?.categories || []).map((c) => [c.name, c.name] as [string, string])], test: (r, v) => r.category === v },
          { id: 'how', label: 'Status', options: [['', 'Any status'], ['ok', 'Has a vendor'], ['none', 'Not mapped'], ['bad', 'Mapped but not usable'], ['sku', 'Through SKU mapping'], ['sc', 'Through sub-category mapping'], ['est', 'Estimate, no SKU rate']],
            test: (r, v) => v === 'ok' ? r.orderable : v === 'none' || v === 'bad' ? r.status === v : v === 'est' ? r.orderable && r.rate == null : r.orderable && r.level === v },
        ]}
        columns={[
          { key: 'sku', label: 'SKU', render: (r) => r.sku_code, csv: (r) => r.sku_code },
          { key: 'item', label: 'Item', render: (r) => <>{r.brand} {r.item_name} <span className="hint">{r.pack_size}</span></>, csv: (r) => r.item_name },
          { key: 'sub', label: 'Sub-category', render: (r) => r.sub_category, csv: (r) => r.sub_category },
          { key: 'vendor', label: 'Vendor', render: (r) => r.orderable ? r.vendor_name : <Pill tone="bad">{r.problem}</Pill>, csv: (r) => r.orderable ? r.vendor_name : r.problem },
          { key: 'through', label: 'Mapped through', render: (r) => r.level === 'sku' ? 'SKU mapping' : r.level === 'sc' ? 'Sub-category mapping' : '—', csv: (r) => r.level },
          { key: 'rate', label: 'Rate ex GST', right: true, render: (r) => r.rate != null ? inr(r.rate) : r.orderable ? 'estimate' : '', sort: (r) => r.rate || 0, csv: (r) => r.rate ?? '' },
        ]} csvName="vendor-check" />
    </>
  )
}
