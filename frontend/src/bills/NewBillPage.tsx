// Enter a utility or AMC bill with its invoice. A second bill for the same property, type and period is blocked.
// ?clone=<id> starts from an earlier bill: same property, type, vendor, duration and remark, for the period after it
// (next month, or the month after an AMC ends); the invoice is entered fresh. Save draft keeps the half-filled bill
// (invoice file too); ?draft=<id> opens it again, and the person's drafts are listed above an empty form.
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { get, upload } from '../api/client'
import { DraftControls, DraftList, KeptFiles, useDraft, type Draft } from '../formdrafts/drafts'
import { useLookups } from '../api/lookups'
import type { Doc } from '../api/types'
import { useApi } from '../api/useApi'
import { dateTime, thisMonth, today } from '../format/format'
import { Errors } from '../ui/Errors'
import { Seg } from '../ui/Tabs'
import { useAction } from '../ui/useAction'
import { useSession } from '../auth/session'
import { SearchSelect } from '../ui/SearchSelect'

interface FormData { types: { id: number; name: string; section: string; default_gst: number; vendors: { id: number; name: string }[] }[]; amc_durations: number[]; approver: string }

export function NewBillPage() {
  const { section: s0 = 'utility' } = useParams()
  const nav = useNavigate()
  const l = useLookups()
  const { me } = useSession()
  const { data } = useApi<FormData>('/bills/form')
  const [sp] = useSearchParams()
  const cloneId = sp.get('clone')
  const draftId = Number(sp.get('draft')) || 0
  const draft = useDraft('bill')
  const src = useApi<{ document: Doc }>(cloneId ? `/documents/${cloneId}` : null)
  const from = src.data?.document.kind === 'bill' ? src.data.document : null
  const [section, setSection] = useState(s0)
  const [typeId, setTypeId] = useState(0)
  const [pid, setPid] = useState(0)
  const [vid, setVid] = useState(0)
  const [period, setPeriod] = useState(thisMonth())
  const [months, setMonths] = useState(12)
  const [invNo, setInvNo] = useState('')
  const [invDate, setInvDate] = useState(today())
  const [amt, setAmt] = useState('')
  const [gst, setGst] = useState(18)
  const [remark, setRemark] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const a = useAction()
  const types = useMemo(() => (data?.types || []).filter((t) => t.section === section), [data, section])
  const type = types.find((t) => t.id === typeId)
  // Only the properties the person may raise for, the same check the server makes when the bill is submitted.
  const props = useMemo(() => (l?.properties || []).filter((p) => p.active && (me?.is_admin || me?.roles.some((g) => g.rights.includes('raise')
    && (g.scope === 'all' || (g.scope === 'clusters' && g.cluster_ids.includes(p.cluster_id)) || (g.scope === 'properties' && g.property_ids.includes(p.id)))))), [l, me])
  // A clone or a draft sets the section, type, property and period first; when its type's defaults run, its vendor
  // (and a draft's GST) is kept.
  const cloned = useRef<Doc | null>(null)
  const seed = useRef<{ type: number; vendor: number; prop: number; gst?: number } | null>(null)
  useEffect(() => {
    if (!draftId || !data) return
    get<Draft<BillFields>[]>('/drafts?form=bill').then(async (list) => {
      const d = list.find((x) => x.id === draftId)
      if (!d) return
      const f = d.fields
      seed.current = { type: f.typeId, vendor: f.vid, prop: f.pid, gst: f.gst }
      setSection(f.section); setTypeId(f.typeId); setPid(f.pid); setVid(f.vid); setPeriod(f.period); setMonths(f.months)
      setInvNo(f.invNo); setInvDate(f.invDate); setAmt(f.amt); setGst(f.gst); setRemark(f.remark)
      setFile((await draft.open(d)).invoice?.[0] || null)
    }).catch(() => {})
  }, [draftId, data]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!from || !data || cloned.current === from) return
    cloned.current = from
    seed.current = { type: from.bill_type_id || 0, vendor: from.vendor_id, prop: from.property_id }
    setSection(from.bill_section || s0); setTypeId(from.bill_type_id || 0); setPid(from.property_id)
    setPeriod(nextPeriod(from.period_start, from.bill_section === 'amc' ? from.months || 1 : 1)); setMonths(from.months || 12); setRemark(from.remark)
  }, [from, data, s0])
  // Defaults fall back to the cloned bill's type and property when they are on offer, so they never override the clone.
  useEffect(() => {
    if (!types.length || types.some((t) => t.id === typeId)) return
    const c = seed.current
    setTypeId(c && types.some((t) => t.id === c.type) ? c.type : types[0].id)
  }, [types, typeId])
  useEffect(() => {
    if (!type) return
    const c = seed.current
    setGst(c?.type === type.id && c.gst != null ? c.gst : type.default_gst)
    setVid(c && c.type === type.id && type.vendors.some((v) => v.id === c.vendor) ? c.vendor : type.vendors[0]?.id || 0)
  }, [type])
  useEffect(() => {
    if (!props.length || (pid && props.some((p) => p.id === pid))) return
    const c = seed.current
    setPid(c && props.some((p) => p.id === c.prop) ? c.prop : props[0].id)
  }, [props, pid])
  useEffect(() => { if (data?.amc_durations.length && !cloneId && !draftId) setMonths(data.amc_durations[data.amc_durations.length - 1]) }, [data, cloneId])
  const amc = section === 'amc'
  const saveDraft = () => draft.save([type?.name || (amc ? 'AMC bill' : 'Utility bill'), props.find((p) => p.id === pid)?.name, period, invNo && 'invoice ' + invNo].filter(Boolean).join(' · '),
    { section, typeId, pid, vid, period, months, invNo, invDate, amt, gst, remark } satisfies BillFields, { invoice: file })
  return (
    <>
      <div className="head"><div><button className="link" onClick={() => nav('/requests')}>‹ Purchase Requests</button><h1>New {amc ? 'AMC' : 'utility'} bill</h1>
        <p className="sub">{from ? `A copy of ${from.number} (${from.bill_type}, ${from.property}) for the next period. Enter the new invoice and send it for approval; ${from.number} itself is not changed.`
          : 'A bill needs no PO or goods receipt. Enter the invoice and send it for approval.'}</p></div></div>
      {!draftId && !cloneId && <DraftList form="bill" label="Your bill drafts" onOpen={(d) => nav(`/bills/new/${d.fields.section}?draft=${d.id}`)} />}
      {draftId > 0 && draft.savedAt && <p className="note">Your draft from {dateTime(draft.savedAt)} is filled in. Finish it and submit, or save the draft again.</p>}
      {from && l && !props.some((p) => p.id === from.property_id) && <p className="note warn">You cannot raise bills for {from.property}, so pick one of your properties.</p>}
      <div className="cols">
        <section className="card">
          <Seg value={section} onChange={setSection} items={[['utility', 'Utility bill'], ['amc', 'Maintenance AMC']]} />
          <div className="grid2">
            <label className="field"><span>Property</span>{props.length
              ? <SearchSelect value={String(pid)} onChange={(v) => setPid(+v)} placeholder="Search property or code" ariaLabel="Property" items={props.map((p) => ({ value: String(p.id), label: p.name, hint: p.code }))} />
              : <span className="hint">No property is assigned to you for bills. Ask the System Admin to add one under Users & Rights.</span>}</label>
            <label className="field"><span>{amc ? 'AMC for' : 'Utility'}</span><SearchSelect value={String(typeId)} onChange={(v) => setTypeId(+v)} placeholder="Search bill type" ariaLabel="Bill type" items={types.map((t) => ({ value: String(t.id), label: t.name, hint: 'GST ' + t.default_gst + '%' }))} /></label>
            <label className="field"><span>Vendor</span>
              {type?.vendors.length ? <SearchSelect value={String(vid)} onChange={(v) => setVid(+v)} placeholder="Search vendor" ariaLabel="Vendor" items={type.vendors.map((v) => ({ value: String(v.id), label: v.name }))} />
                : <select disabled><option>No verified vendor for this bill type</option></select>}
              {!type?.vendors.length && <span className="hint">Onboard the vendor under Vendors and tick this bill type; Finance verifies the bank details.</span>}</label>
            <label className="field"><span>{amc ? 'Start month' : 'Bill month'}</span><input type="month" value={period} onChange={(e) => setPeriod(e.target.value)} /></label>
            {amc && <label className="field"><span>Duration</span><select value={months} onChange={(e) => setMonths(+e.target.value)}>{data?.amc_durations.map((m) => <option key={m} value={m}>{m} months</option>)}</select></label>}
            <label className="field"><span>Invoice number</span><input value={invNo} onChange={(e) => setInvNo(e.target.value)} placeholder="As printed on the bill" /></label>
            <label className="field"><span>Invoice date</span><input type="date" max={today()} value={invDate} onChange={(e) => setInvDate(e.target.value)} /></label>
            <label className="field"><span>Invoice amount incl. GST (₹)</span><input type="number" min="0" step="0.01" value={amt} onChange={(e) => setAmt(e.target.value)} /></label>
            <label className="field"><span>GST %</span><select value={gst} onChange={(e) => setGst(+e.target.value)}>{[0, 5, 12, 18, 28, 40].map((g) => <option key={g} value={g}>{g}%</option>)}</select></label>
            <label className="field"><span>Invoice file</span><input type="file" accept="image/*,application/pdf" capture="environment" onChange={(e) => setFile(e.target.files?.[0] || null)} /><KeptFiles files={[file]} /></label>
          </div>
          <label className="field"><span>Remark (optional)</span><textarea value={remark} onChange={(e) => setRemark(e.target.value)} /></label>
          <Errors list={a.errors} />
          <div className="btns"><DraftControls draft={draft} onSave={saveDraft} />
            <button className="btn" disabled={a.busy} onClick={() => a.run(() => upload('/bills', {
            bill_type_id: typeId, property_id: pid, vendor_id: vid, period_start: period, months: amc ? months : 1, invoice_number: invNo, invoice_date: invDate, invoice_amount: +amt, gst_pct: gst, remark,
          }, { invoice: file }), (r: any) => { draft.clear(); nav('/requests'); return r.message })}>Submit for approval</button></div>
        </section>
        <section className="card"><h2>Approval route</h2><p>Cluster Manager → the bill type's L1 and L2 at this property (Users &amp; Rights › Approval matrix) → C1 / C2 above the Bills amounts → Finance payment approval → Zoho</p>
          <p className="hint">The system blocks a second bill for the same item, property and period, and a repeated invoice number for the same vendor.</p></section>
      </div>
    </>
  )
}

// nextPeriod gives the month after a bill period of n months: 2026-09 for 1 month gives 2026-10.
function nextPeriod(start: string, n: number): string {
  const [y, m] = start.split('-').map(Number)
  if (!y || !m) return thisMonth()
  const t = new Date(y, m - 1 + n, 1)
  return `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, '0')}`
}

interface BillFields { section: string; typeId: number; pid: number; vid: number; period: string; months: number; invNo: string; invDate: string; amt: string; gst: number; remark: string }
