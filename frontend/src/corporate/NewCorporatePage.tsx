// Raise a corporate purchase (something still to buy: items from the item master, a PO follows) or a corporate bill (an
// invoice already received). Lines only use existing items: nothing new is added to the item master from here.
// Save draft keeps the half-filled form (invoice and quotation files too); ?draft=<id> opens it again. ?clone=<id> starts
// from an earlier corporate purchase or bill (department, vendor, lines or expense head, reason), to change and send.
// Picking an item fills its expense head from Corporate Setup (the head that covers its sub-category).
import { useEffect, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { get, upload } from '../api/client'
import type { Doc } from '../api/types'
import { DraftControls, DraftList, KeptFiles, useDraft, type Draft } from '../formdrafts/drafts'
import { useApi } from '../api/useApi'
import { UploadModal } from '../bulkupload/UploadModal'
import { useSession } from '../auth/session'
import { dateTime, inr, lineValue, today } from '../format/format'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { SearchSelect } from '../ui/SearchSelect'
import { Seg } from '../ui/Tabs'
import { useAction } from '../ui/useAction'

interface Form {
  office: string; l1_only_below: number
  /** C1 / C2 limits: corporate purchases, and bills. */
  amount_limits: Record<'purchase' | 'bill', { c1: number; c2: number }>
  departments: { id: number; name: string; l1_user_id: number; l1: string; l2: string; backup: string; active: boolean }[]
  expense_heads: { id: number; name: string; active: boolean; fin_status: string }[]
  vendors: { id: number; name: string; vendor_type: string }[]
  /** Items the corporate office may order (active, approved, available there). */
  items: { id: number; code: string; name: string; pack_size: string; gst_pct: number; sub_category: string; category: string; expense_head_id: number | null }[]
  /** Every vendor's current rate card per item. */
  rates: { vendor_id: number; sku_id: number; rate: number; gst_pct: number }[]
}
/** A line: an existing item; the rate is typed only when the vendor has no rate card for it. */
interface L { sku_id: number; expense_head_id: number; qty: string; rate: string }
const blank = (h = 0): L => ({ sku_id: 0, expense_head_id: h, qty: '1', rate: '' })

export function NewCorporatePage() {
  const { kind: k0 = 'purchase' } = useParams()
  const nav = useNavigate()
  const { me } = useSession()
  const { data, error } = useApi<Form>('/corporate/form')
  const [kind, setKind] = useState(k0)
  const [dept, setDept] = useState(0)
  const [vendor, setVendor] = useState(0)
  const [note, setNote] = useState('')
  const [lines, setLines] = useState<L[]>([blank()])
  const [linesUpload, setLinesUpload] = useState(false)
  const [head, setHead] = useState(0)
  const [inv, setInv] = useState({ number: '', date: today(), amount: '', gst: 18 })
  const [file, setFile] = useState<File | null>(null)
  const [quote, setQuote] = useState<File | null>(null)
  const a = useAction()
  const [sp] = useSearchParams()
  const draftId = Number(sp.get('draft')) || 0
  const cloneId = Number(sp.get('clone')) || 0
  const [clonedFrom, setClonedFrom] = useState('')
  const draft = useDraft('corporate')
  useEffect(() => {
    if (!cloneId) return
    get<{ document: Doc }>(`/documents/${cloneId}`).then(({ document: c }) => {
      setKind(c.kind === 'corp_bill' ? 'bill' : 'purchase'); setDept(c.department_id || 0); setVendor(c.vendor_id); setNote(c.remark || '')
      const ls = c.lines.filter((l) => l.sku_id && l.qty > 0).map((l) => ({ sku_id: l.sku_id, expense_head_id: l.expense_head_id || 0, qty: String(l.qty), rate: String(l.rate) }))
      setLines(ls.length ? ls : [blank()]); setHead(c.expense_head_id || 0); setClonedFrom(c.number)
    }).catch(() => {})
  }, [cloneId])
  useEffect(() => {
    if (!draftId) return
    get<Draft<CorpFields>[]>('/drafts?form=corporate').then(async (list) => {
      const x = list.find((y) => y.id === draftId)
      if (!x) return
      const f = x.fields
      setKind(f.kind); setDept(f.dept); setVendor(f.vendor); setNote(f.note); const kept = (f.lines || []).filter((l) => l.sku_id) // older drafts had typed lines, which are no longer allowed
      setLines(kept.length ? kept : [blank()]); setHead(f.head); setInv(f.inv)
      const files = await draft.open(x)
      setFile(files.invoice?.[0] || null); setQuote(files.quote?.[0] || null)
    }).catch(() => {})
  }, [draftId]) // eslint-disable-line react-hooks/exhaustive-deps
  const deps = (data?.departments || []).filter((d) => d.active)
  const heads = (data?.expense_heads || []).filter((h) => h.active && h.fin_status === 'approved') // a new head waits for Finance
  useEffect(() => { if (!dept && deps.length) setDept(deps[0].id) }, [deps, dept])
  const d = deps.find((x) => x.id === dept)
  const itemOf = (id: number) => data?.items.find((x) => x.id === id)
  const cardOf = (sku: number) => data?.rates.find((x) => x.vendor_id === vendor && x.sku_id === sku)
  // The vendor's rate card decides the rate and GST; without one the requester types the rate and the item's GST applies.
  const priced = (l: L) => { const c = cardOf(l.sku_id); return { rate: c ? c.rate : +l.rate || 0, gst: c ? c.gst_pct : itemOf(l.sku_id)?.gst_pct ?? 0, card: !!c } }
  const total = kind === 'purchase' ? lines.reduce((t, l) => { const p = priced(l); return t + lineValue(+l.qty || 0, p.rate, p.gst) }, 0) : +inv.amount || 0
  const itemChoices = (data?.items || []).map((x) => ({ value: String(x.id), label: `${x.name} (${x.pack_size}) · ${x.code}`, group: `${x.category} › ${x.sub_category}` }))
  const isL1 = d && d.l1_user_id === me?.id
  const set = (i: number, patch: Partial<L>) => setLines(lines.map((l, j) => j === i ? { ...l, ...patch } : l))
  const saveDraft = () => draft.save([kind === 'purchase' ? 'Corporate purchase' : 'Corporate bill', deps.find((x) => x.id === dept)?.name, data?.vendors.find((v) => v.id === vendor)?.name,
    kind === 'bill' && inv.number ? 'invoice ' + inv.number : '', total ? inr(total) : ''].filter(Boolean).join(' · '),
    { kind, dept, vendor, note, lines, head, inv } satisfies CorpFields, { invoice: kind === 'bill' ? file : null, quote: kind === 'purchase' ? quote : null })
  if (error) return <p className="note bad">{error.message}</p>
  return (
    <>
      <div className="head"><div><button className="link" onClick={() => nav('/corporate')}>‹ Corporate</button><h1>{kind === 'purchase' ? 'New corporate purchase' : 'New corporate bill'}</h1>
        <p className="sub">{kind === 'purchase' ? 'For something still to buy: a PO goes to the vendor after approval.' : 'For an invoice already received, with no PO. Finance pays it after approval.'} Charged to {data?.office}.</p></div></div>
      {!draftId && <DraftList form="corporate" label="Your corporate drafts" onOpen={(x) => nav(`/corporate/new/${x.fields.kind}?draft=${x.id}`)} />}
      {clonedFrom && <p className="note">Filled in from {clonedFrom}. Change what is different (quantities, rates, vendor, reason), then send it.</p>}
      {draftId > 0 && draft.savedAt && <p className="note">Your draft from {dateTime(draft.savedAt)} is filled in. Finish it and send it, or save the draft again.</p>}
      <Seg value={kind} onChange={setKind} items={[['purchase', 'Corporate purchase'], ['bill', 'Corporate bill']]} />
      <div className="cols">
        <div className="stack">
          <section className="card"><div className="grid2">
            <Field label="Department"><select value={dept} onChange={(e) => setDept(+e.target.value)}>{deps.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></Field>
            <Field label="Vendor" hint="Any active vendor with verified bank details."><select value={vendor} onChange={(e) => setVendor(+e.target.value)}><option value={0}>Choose vendor</option>{data?.vendors.map((v) => <option key={v.id} value={v.id}>{v.name}</option>)}</select></Field>
          </div></section>
          {kind === 'purchase' ? (
            <section className="card flush"><h2>Items</h2>
              <p className="hint" style={{ padding: '0 14px' }}>Pick items from the item master; nothing new is created here. An item that is missing is added under Items (and approved by Finance) first. {vendor ? 'Rates come from the vendor\'s rate card; type one only where it has none.' : 'Choose the vendor to fill in rates from its rate card.'}</p>
              <div className="scroll"><table><thead><tr><th>Item</th><th>Expense head</th><th className="r">Qty</th><th>Unit</th><th className="r">Rate before GST</th><th>GST</th><th className="r">Amount</th><th></th></tr></thead>
                <tbody>{lines.map((l, i) => { const it = itemOf(l.sku_id); const p = priced(l); return <tr key={i}>
                  <td style={{ minWidth: 260 }}><SearchSelect value={l.sku_id ? String(l.sku_id) : ''} items={itemChoices} onChange={(v) => set(i, { sku_id: +v, rate: '', expense_head_id: itemOf(+v)?.expense_head_id || l.expense_head_id })} placeholder="Type to find an item" ariaLabel={'Item ' + (i + 1)} /></td>
                  <td><select value={l.expense_head_id} onChange={(e) => set(i, { expense_head_id: +e.target.value })} aria-label={'Expense head ' + (i + 1)}><option value={0}>Choose</option>{heads.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}</select></td>
                  <td className="r"><input className="qty" style={{ width: 70 }} type="number" min="0" value={l.qty} onChange={(e) => set(i, { qty: e.target.value })} aria-label={'Quantity ' + (i + 1)} /></td>
                  <td>{it?.pack_size || '—'}</td>
                  <td className="r">{!l.sku_id ? '—' : p.card ? <span title="From the vendor's rate card">{inr(p.rate)}</span>
                    : <input className="qty" type="number" min="0" value={l.rate} onChange={(e) => set(i, { rate: e.target.value })} placeholder={vendor ? 'No rate card' : 'Rate'} aria-label={'Rate ' + (i + 1)} />}</td>
                  <td>{l.sku_id ? p.gst + '%' : '—'}</td>
                  <td className="r">{inr(lineValue(+l.qty || 0, p.rate, p.gst))}</td>
                  <td><button className="link" onClick={() => setLines(lines.length > 1 ? lines.filter((_, j) => j !== i) : [blank()])}>Remove</button></td></tr> })}</tbody></table></div>
              <div className="btns" style={{ padding: '10px 14px', justifyContent: 'space-between' }}><span className="tools"><button className="btn ghost" onClick={() => setLines([...lines, blank(lines[lines.length - 1]?.expense_head_id)])}>+ Add line</button><button className="btn ghost" disabled={!vendor} title={vendor ? '' : 'Choose the vendor first: rates come from its rate card'} onClick={() => setLinesUpload(true)}>Upload lines (Excel)</button></span><b>Total {inr(total)}</b></div>
            </section>
          ) : (
            <section className="card"><div className="grid2">
              <Field label="Expense head"><select value={head} onChange={(e) => setHead(+e.target.value)}><option value={0}>Choose</option>{heads.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}</select></Field>
              <Field label="Invoice number"><input value={inv.number} onChange={(e) => setInv({ ...inv, number: e.target.value })} placeholder="As printed on the invoice" /></Field>
              <Field label="Invoice date"><input type="date" max={today()} value={inv.date} onChange={(e) => setInv({ ...inv, date: e.target.value })} /></Field>
              <Field label="Invoice amount incl. GST (₹)"><input type="number" min="0" value={inv.amount} onChange={(e) => setInv({ ...inv, amount: e.target.value })} /></Field>
              <Field label="GST %"><select value={inv.gst} onChange={(e) => setInv({ ...inv, gst: +e.target.value })}>{[0, 5, 12, 18, 28, 40].map((g) => <option key={g} value={g}>{g}%</option>)}</select></Field>
              <Field label="Invoice file"><input type="file" accept="image/*,application/pdf" onChange={(e) => setFile(e.target.files?.[0] || null)} /><KeptFiles files={[file]} /></Field>
            </div></section>
          )}
          <section className="card">
            <Field label="Why is this needed?"><textarea value={note} onChange={(e) => setNote(e.target.value)} /></Field>
            {kind === 'purchase' && <Field label="Quotation (optional)"><input type="file" accept="image/*,application/pdf" onChange={(e) => setQuote(e.target.files?.[0] || null)} /><KeptFiles files={[quote]} /></Field>}
          </section>
        </div>
        <section className="card"><h2>Approval</h2>
          {d && <>{isL1 && <p className="note">You are the L1 for {d.name}, so it goes straight to the L2.</p>}
            <p>1. L1, department head: <b>{d.l1}</b>{isL1 ? ' (skipped)' : ''}</p>
            <p>2. L2: <b>{d.l2}</b>{data && data.l1_only_below > 0 && total < data.l1_only_below && !isL1 ? ` (not needed below ${inr(data.l1_only_below)})` : ''}</p>
            {d.backup && <p className="hint">Backup if either is away: {d.backup}</p>}
            {(() => { const lim = data?.amount_limits?.[kind as 'purchase' | 'bill']; if (!lim) return null; const c2 = lim.c2 > 0 && total > lim.c2; const c1 = (lim.c1 > 0 && total > lim.c1) || (c2 && lim.c1 > 0)
              return <>{c1 && <p>3. C1: above {inr(lim.c1)}</p>}{c2 && <p>{c1 ? 4 : 3}. C2: above {inr(lim.c2)}</p>}</> })()}</>}
          <p className="hint">Then: {kind === 'purchase' ? 'the last approval raises the PO to the vendor, you confirm receipt with the invoice, Accounts matches, Finance approves payment and it goes to Zoho.' : 'Finance approves payment and it goes to Zoho.'}</p>
          <Errors list={a.errors} />
          <div className="btns"><DraftControls draft={draft} onSave={saveDraft} />
          <button className="btn" disabled={a.busy} onClick={() => a.run(() => upload('/corporate/requests', {
            kind, department_id: dept, vendor_id: vendor, note,
            lines: kind === 'purchase' ? lines.map((l) => ({ sku_id: l.sku_id, expense_head_id: l.expense_head_id, qty: +l.qty, rate: priced(l).rate })) : [],
            expense_head_id: head, invoice_number: inv.number, invoice_date: inv.date, invoice_amount: +inv.amount, gst_pct: inv.gst,
          }, { invoice: file, quote }), (r: any) => { draft.clear(); nav('/documents/' + r.id); return r.message })}>Send for approval · {inr(total)}</button></div>
        </section>
      </div>
      {linesUpload && <UploadModal kind="corp_lines" title="Upload the lines of this corporate purchase" query={`?vendor_id=${vendor}`} onClose={() => setLinesUpload(false)}
        onRows={(objs) => {
          // Uploaded lines are added after what is already filled in (an empty starting line is dropped).
          const add: L[] = objs.map((o: any) => ({ sku_id: o.sku_id, expense_head_id: o.expense_head_id, qty: String(o.qty), rate: String(o.rate) }))
          setLines((c) => [...c.filter((l) => l.sku_id), ...add])
          setLinesUpload(false)
        }} />}
    </>
  )
}

interface CorpFields { kind: string; dept: number; vendor: number; note: string; lines: L[]; head: number; inv: { number: string; date: string; amount: string; gst: number } }
