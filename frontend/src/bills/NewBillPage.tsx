// Enter a utility or AMC bill with its invoice. A second bill for the same property, type and period is blocked.
import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { upload } from '../api/client'
import { useLookups } from '../api/lookups'
import { useApi } from '../api/useApi'
import { thisMonth, today } from '../format/format'
import { Errors } from '../ui/Errors'
import { Seg } from '../ui/Tabs'
import { useAction } from '../ui/useAction'

interface FormData { types: { id: number; name: string; section: string; default_gst: number; vendors: { id: number; name: string }[] }[]; amc_durations: number[]; approver: string }

export function NewBillPage() {
  const { section: s0 = 'utility' } = useParams()
  const nav = useNavigate()
  const l = useLookups()
  const { data } = useApi<FormData>('/bills/form')
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
  const props = (l?.properties || []).filter((p) => p.active)
  useEffect(() => { if (types.length && !types.some((t) => t.id === typeId)) setTypeId(types[0].id) }, [types, typeId])
  useEffect(() => { if (type) { setGst(type.default_gst); setVid(type.vendors[0]?.id || 0) } }, [type])
  useEffect(() => { if (!pid && props.length) setPid(props[0].id) }, [props, pid])
  useEffect(() => { if (data?.amc_durations.length) setMonths(data.amc_durations[data.amc_durations.length - 1]) }, [data])
  const amc = section === 'amc'
  return (
    <>
      <div className="head"><div><button className="link" onClick={() => nav('/bills/' + section)}>‹ {amc ? 'AMC Bills' : 'Utility Bills'}</button><h1>New {amc ? 'AMC' : 'utility'} bill</h1>
        <p className="sub">A bill needs no PO or goods receipt. Enter the invoice and send it for approval.</p></div></div>
      <div className="cols">
        <section className="card">
          <Seg value={section} onChange={setSection} items={[['utility', 'Utility bill'], ['amc', 'Maintenance AMC']]} />
          <div className="grid2">
            <label className="field"><span>Property</span><select value={pid} onChange={(e) => setPid(+e.target.value)}>{props.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>
            <label className="field"><span>{amc ? 'AMC for' : 'Utility'}</span><select value={typeId} onChange={(e) => setTypeId(+e.target.value)}>{types.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}</select></label>
            <label className="field"><span>Vendor</span>
              {type?.vendors.length ? <select value={vid} onChange={(e) => setVid(+e.target.value)}>{type.vendors.map((v) => <option key={v.id} value={v.id}>{v.name}</option>)}</select>
                : <select disabled><option>No verified vendor for this bill type</option></select>}
              {!type?.vendors.length && <span className="hint">Onboard the vendor under Vendors and tick this bill type; Finance verifies the bank details.</span>}</label>
            <label className="field"><span>{amc ? 'Start month' : 'Bill month'}</span><input type="month" value={period} onChange={(e) => setPeriod(e.target.value)} /></label>
            {amc && <label className="field"><span>Duration</span><select value={months} onChange={(e) => setMonths(+e.target.value)}>{data?.amc_durations.map((m) => <option key={m} value={m}>{m} months</option>)}</select></label>}
            <label className="field"><span>Invoice number</span><input value={invNo} onChange={(e) => setInvNo(e.target.value)} placeholder="As printed on the bill" /></label>
            <label className="field"><span>Invoice date</span><input type="date" max={today()} value={invDate} onChange={(e) => setInvDate(e.target.value)} /></label>
            <label className="field"><span>Invoice amount incl. GST (₹)</span><input type="number" min="0" step="0.01" value={amt} onChange={(e) => setAmt(e.target.value)} /></label>
            <label className="field"><span>GST %</span><select value={gst} onChange={(e) => setGst(+e.target.value)}>{[0, 5, 12, 18, 28, 40].map((g) => <option key={g} value={g}>{g}%</option>)}</select></label>
            <label className="field"><span>Invoice file</span><input type="file" accept="image/*,application/pdf" capture="environment" onChange={(e) => setFile(e.target.files?.[0] || null)} /></label>
          </div>
          <label className="field"><span>Remark (optional)</span><textarea value={remark} onChange={(e) => setRemark(e.target.value)} /></label>
          <Errors list={a.errors} />
          <div className="btns"><button className="btn" disabled={a.busy} onClick={() => a.run(() => upload('/bills', {
            bill_type_id: typeId, property_id: pid, vendor_id: vid, period_start: period, months: amc ? months : 1, invoice_number: invNo, invoice_date: invDate, invoice_amount: +amt, gst_pct: gst, remark,
          }, { invoice: file }), (r: any) => { nav('/bills/' + section); return r.message })}>Submit for approval</button></div>
        </section>
        <section className="card"><h2>Approval route</h2><p>{data?.approver === 'cc' ? 'Cost Controller' : 'Cluster Manager'} → Finance payment approval → Zoho</p>
          <p className="hint">The system blocks a second bill for the same item, property and period, and a repeated invoice number for the same vendor.</p></section>
      </div>
    </>
  )
}
