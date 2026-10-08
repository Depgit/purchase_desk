// The lists used when a SKU is added: categories, sub-categories (with their 2-letter code), pack sizes and brands.
import { useState } from 'react'
import { post } from '../api/client'
import { refreshLookups, useLookups } from '../api/lookups'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { ActiveToggle } from '../ui/ActiveToggle'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Tabs } from '../ui/Tabs'
import { PHASE, usePhase } from '../releasephase/phase'
import { useAction } from '../ui/useAction'

type Tab = 'cat' | 'pack' | 'brand'

export function ItemSetupPage() {
  const { hasRight } = useSession()
  const bud = usePhase().on(PHASE.budgets)
  const l = useLookups()
  const [tab, setTab] = useState<Tab>('cat')
  const [add, setAdd] = useState<'category' | 'sub' | 'pack' | 'brand' | null>(null)
  const subs = useApi<{ id: number; name: string; code: string; category: string; skus: number; vendors: number; active: boolean }[]>('/sub-categories')
  const cats = useApi<{ id: number; name: string; is_fb: boolean; sub_categories: number; skus: number }[]>('/categories')
  const packs = useApi<{ id: number; name: string; active: boolean; skus: number }[]>('/pack-sizes')
  const brands = useApi<{ id: number; name: string; active: boolean; skus: number }[]>('/brands')
  const ed = hasRight('masters')
  const reloadAll = () => { subs.reload(); cats.reload(); packs.reload(); brands.reload(); refreshLookups() }
  const named = (rows: { id: number; name: string; active: boolean; skus: number }[] | null, kind: string) => (
    <section className="card flush"><div className="scroll"><table><thead><tr><th>Name</th><th className="r">SKUs</th><th>Status</th></tr></thead>
      <tbody>{(rows || []).map((x) => <tr key={x.id} className={x.active ? '' : 'dim'}><td className="id">{x.name}</td><td className="r">{x.skus}</td>
        <td><ActiveToggle kind={kind} id={x.id} name={x.name} active={x.active} onDone={reloadAll} /></td></tr>)}</tbody></table></div></section>
  )
  return (
    <>
      <div className="head"><div><h1>Item Setup</h1><p className="sub">Whatever is added here shows up in the Add item form and in the bulk upload checks.</p></div>
        <div className="acts">{ed && (tab === 'cat' ? <><button className="btn ghost" onClick={() => setAdd('category')}>+ Add category</button><button className="btn" onClick={() => setAdd('sub')}>+ Add sub-category</button></>
          : tab === 'pack' ? <button className="btn" onClick={() => setAdd('pack')}>+ Add pack size</button> : <button className="btn" onClick={() => setAdd('brand')}>+ Add brand</button>)}</div></div>
      <Tabs value={tab} onChange={setTab} items={[['cat', 'Categories & sub-categories'], ['pack', 'Pack sizes'], ['brand', 'Brands']]} />
      {tab === 'cat' && <div className="cols">
        <section className="card flush"><h2>Sub-categories</h2><div className="scroll"><table>
          <thead><tr><th>Sub-category</th><th>Category</th><th>SKU code</th><th className="r">SKUs</th><th className="r">Vendors</th><th>Status</th></tr></thead>
          <tbody>{(subs.data || []).map((s) => <tr key={s.id} className={s.active ? '' : 'dim'}><td className="id">{s.name}</td><td>{s.category}</td><td>{s.code}-0000</td><td className="r">{s.skus}</td><td className="r">{s.vendors}</td>
            <td><ActiveToggle kind="sub_category" id={s.id} name={s.name} active={s.active} onDone={reloadAll} /></td></tr>)}</tbody></table></div></section>
        <section className="card flush"><h2>Categories</h2><div className="scroll"><table>
          <thead><tr><th>Category</th><th className="r">Sub-categories</th><th className="r">SKUs</th></tr></thead>
          <tbody>{(cats.data || []).map((c) => <tr key={c.id}><td className="id">{c.name}{c.is_fb ? ' (F&B)' : ''}</td><td className="r">{c.sub_categories}</td><td className="r">{c.skus}</td></tr>)}</tbody></table></div>
          {bud && <p className="hint" style={{ padding: '10px 14px' }}>A new sub-category starts with a budget of zero for every property, so requests against it are flagged until a budget is set. F&amp;B categories get sales-driven day budgets.</p>}</section>
      </div>}
      {tab === 'pack' && named(packs.data, 'pack_size')}
      {tab === 'brand' && named(brands.data, 'brand')}
      {add && <AddModal kind={add} cats={l?.categories || []} onClose={() => setAdd(null)} onDone={() => { setAdd(null); reloadAll() }} />}
    </>
  )
}

function AddModal({ kind, cats, onClose, onDone }: { kind: 'category' | 'sub' | 'pack' | 'brand'; cats: { id: number; name: string }[]; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState('')
  const [cat, setCat] = useState(cats[0]?.id || 0)
  const [code, setCode] = useState('')
  const [fb, setFb] = useState(false)
  const a = useAction()
  const title = { category: 'Add category', sub: 'Add sub-category', pack: 'Add pack size', brand: 'Add brand' }[kind]
  const path = { category: '/categories', sub: '/sub-categories', pack: '/pack-sizes', brand: '/brands' }[kind]
  return (
    <Modal title={title} onClose={onClose}>
      <Field label="Name"><input value={name} onChange={(e) => setName(e.target.value)} /></Field>
      {kind === 'category' && <label className="ck"><input type="checkbox" checked={fb} onChange={(e) => setFb(e.target.checked)} />Food &amp; Beverage (day budgets follow sales)</label>}
      {kind === 'sub' && <div className="grid2">
        <Field label="Category"><select value={cat} onChange={(e) => setCat(+e.target.value)}>{cats.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}</select></Field>
        <Field label="SKU code letters" hint="2 letters, used in SKU codes such as SP-0001."><input value={code} maxLength={2} onChange={(e) => setCode(e.target.value.toUpperCase())} /></Field></div>}
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post(path, { name, category_id: cat, code, is_fb: fb }), () => { onDone(); return name + ' added' })}>Save</button></div>
    </Modal>
  )
}
