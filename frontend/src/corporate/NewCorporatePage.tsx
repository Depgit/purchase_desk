// Raise a corporate purchase (something still to buy: free-text lines, a PO follows) or a corporate bill (an invoice already received).
// Save draft keeps the half-filled form (invoice and quotation files too); ?draft=<id> opens it again.
import { useEffect, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { get, upload } from '../api/client'
import { DraftControls, DraftList, KeptFiles, useDraft, type Draft } from '../formdrafts/drafts'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { dateTime, inr, lineValue, today } from '../format/format'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Seg } from '../ui/Tabs'
import { useAction } from '../ui/useAction'

interface Form {
  office: string; l1_only_below: number
  departments: { id: number; name: string; l1_user_id: number; l1: string; l2: string; backup: string; active: boolean }[]
  expense_heads: { id: number; name: string; active: boolean }[]
  vendors: { id: number; name: string; vendor_type: string }[]
}
interface L { description: string; expense_head_id: number; qty: string; unit: string; rate: string; gst_pct: number }
const blank = (h = 0): L => ({ description: '', expense_head_id: h, qty: '1', unit: 'piece', rate: '', gst_pct: 18 })

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
  const [head, setHead] = useState(0)
  const [inv, setInv] = useState({ number: '', date: today(), amount: '', gst: 18 })
  const [file, setFile] = useState<File | null>(null)
  const [quote, setQuote] = useState<File | null>(null)
  const a = useAction()
  const [sp] = useSearchParams()
  const draftId = Number(sp.get('draft')) || 0
  const draft = useDraft('corporate')
  useEffect(() => {
    if (!draftId) return
    get<Draft<CorpFields>[]>('/drafts?form=corporate').then(async (list) => {
      const x = list.find((y) => y.id === draftId)
      if (!x) return
      const f = x.fields
      setKind(f.kind); setDept(f.dept); setVendor(f.vendor); setNote(f.note); setLines(f.lines.length ? f.lines : [blank()]); setHead(f.head); setInv(f.inv)
      const files = await draft.open(x)
      setFile(files.invoice?.[0] || null); setQuote(files.quote?.[0] || null)
    }).catch(() => {})
  }, [draftId]) // eslint-disable-line react-hooks/exhaustive-deps
  const deps = (data?.departments || []).filter((d) => d.active)
  const heads = (data?.expense_heads || []).filter((h) => h.active)
  useEffect(() => { if (!dept && deps.length) setDept(deps[0].id) }, [deps, dept])
  const d = deps.find((x) => x.id === dept)
  const total = kind === 'purchase' ? lines.reduce((t, l) => t + lineValue(+l.qty || 0, +l.rate || 0, l.gst_pct), 0) : +inv.amount || 0
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
              <div className="scroll"><table><thead><tr><th>Description</th><th>Expense head</th><th className="r">Qty</th><th>Unit</th><th className="r">Rate before GST</th><th>GST</th><th className="r">Amount</th><th></th></tr></thead>
                <tbody>{lines.map((l, i) => <tr key={i}>
                  <td><input value={l.description} onChange={(e) => set(i, { description: e.target.value })} placeholder="What is being bought" style={{ minWidth: 240 }} aria-label={'Description ' + (i + 1)} /></td>
                  <td><select value={l.expense_head_id} onChange={(e) => set(i, { expense_head_id: +e.target.value })} aria-label={'Expense head ' + (i + 1)}><option value={0}>Choose</option>{heads.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}</select></td>
                  <td className="r"><input className="qty" style={{ width: 70 }} type="number" min="0" value={l.qty} onChange={(e) => set(i, { qty: e.target.value })} aria-label={'Quantity ' + (i + 1)} /></td>
                  <td><input style={{ width: 90 }} value={l.unit} onChange={(e) => set(i, { unit: e.target.value })} aria-label={'Unit ' + (i + 1)} /></td>
                  <td className="r"><input className="qty" type="number" min="0" value={l.rate} onChange={(e) => set(i, { rate: e.target.value })} aria-label={'Rate ' + (i + 1)} /></td>
                  <td><select value={l.gst_pct} onChange={(e) => set(i, { gst_pct: +e.target.value })} aria-label={'GST ' + (i + 1)}>{[0, 5, 12, 18, 28, 40].map((g) => <option key={g} value={g}>{g}%</option>)}</select></td>
                  <td className="r">{inr(lineValue(+l.qty || 0, +l.rate || 0, l.gst_pct))}</td>
                  <td><button className="link" onClick={() => setLines(lines.length > 1 ? lines.filter((_, j) => j !== i) : [blank()])}>Remove</button></td></tr>)}</tbody></table></div>
              <div className="btns" style={{ padding: '10px 14px', justifyContent: 'space-between' }}><button className="btn ghost" onClick={() => setLines([...lines, blank(lines[lines.length - 1]?.expense_head_id)])}>+ Add line</button><b>Total {inr(total)}</b></div>
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
            {d.backup && <p className="hint">Backup if either is away: {d.backup}</p>}</>}
          <p className="hint">Then: {kind === 'purchase' ? 'Purchase issues the PO, you confirm receipt with the invoice, Accounts matches, Finance pays and pushes to Zoho.' : 'Finance approves payment and pushes to Zoho.'}</p>
          <Errors list={a.errors} />
          <div className="btns"><DraftControls draft={draft} onSave={saveDraft} />
          <button className="btn" disabled={a.busy} onClick={() => a.run(() => upload('/corporate/requests', {
            kind, department_id: dept, vendor_id: vendor, note,
            lines: kind === 'purchase' ? lines.map((l) => ({ ...l, qty: +l.qty, rate: +l.rate })) : [],
            expense_head_id: head, invoice_number: inv.number, invoice_date: inv.date, invoice_amount: +inv.amount, gst_pct: inv.gst,
          }, { invoice: file, quote }), (r: any) => { draft.clear(); nav('/documents/' + r.id); return r.message })}>Send for approval · {inr(total)}</button></div>
        </section>
      </div>
    </>
  )
}

interface CorpFields { kind: string; dept: number; vendor: number; note: string; lines: L[]; head: number; inv: { number: string; date: string; amount: string; gst: number } }
