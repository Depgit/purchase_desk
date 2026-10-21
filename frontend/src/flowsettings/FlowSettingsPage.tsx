// The rules the application runs on. The Admin changes them here; a change applies to new requests at once and is logged.
import { useEffect, useState } from 'react'
import { get, put } from '../api/client'
import { useSession } from '../auth/session'
import { useToast } from '../ui/Toast'

type S = Record<string, any>
// C1 / C2 limits per request type (Flow Settings amount_approvals).
const AMOUNT_TYPES: [string, string][] = [['property', 'Property purchases'], ['preopening', 'Pre-opening purchases'], ['warehouse', 'Warehouse purchases'], ['corporate', 'Corporate purchases'], ['bills', 'Bills (utility, AMC, corporate)']]
const LISTS: [string, string][] = [['event_reasons', 'Event reasons on a request'], ['return_reasons', 'Reasons to return a request'], ['reject_reasons', 'Reasons to reject'],
  ['over_budget_reasons', 'Reasons to approve over budget'], ['balance_reasons', 'Reasons to reject an undelivered PO balance'], ['advance_reasons', 'Reasons for a vendor advance'], ['wh_back_reasons', 'Reasons the warehouse cannot supply'], ['delivery_slots', 'Delivery time slots']]

export function FlowSettingsPage() {
  const { me } = useSession()
  const toast = useToast()
  const [s, setS] = useState<S | null>(null)
  const [lists, setLists] = useState<Record<string, string[]>>({})
  const ed = !!me?.is_admin
  useEffect(() => { get('/settings').then(setS); get('/pick-lists').then(setLists) }, [])
  if (!s) return <p className="sub">Loading…</p>
  const save = async (patch: S) => {
    try { setS(await put('/settings', patch)); toast('Setting saved') } catch (e: any) { toast(e.message, true) }
  }
  const ck = (k: string, label: string, hint?: string) => <label className="ck" style={{ alignItems: 'flex-start' }}>
    <input type="checkbox" checked={!!s[k]} disabled={!ed} onChange={(e) => save({ [k]: e.target.checked })} /><span>{label}{hint && <><br /><span className="hint">{hint}</span></>}</span></label>
  const num = (k: string, label: string, hint?: string) => <label className="field"><span>{label}</span>
    <input type="number" min="0" defaultValue={s[k]} key={s[k]} disabled={!ed} onBlur={(e) => { const v = +e.target.value; if (v !== s[k]) save({ [k]: v }) }} />{hint && <span className="hint">{hint}</span>}</label>
  return (
    <>
      <div className="head"><div><h1>Flow Settings</h1><p className="sub">{ed ? 'Change a setting and it applies to new requests at once; every change is logged.' : 'Only the System Admin can change these.'}</p></div></div>
      <div className="grid2">
        <section className="card"><h2>Purchase request approval</h2>
          <p className="hint">Every request goes to its <b>L1 then L2</b>: at a property or pre-opening property those of its items' category (Users &amp; Rights › Approval matrix), at the warehouse the Warehouse Approvers L1 / L2, at the corporate office the department's. The same person as L1 and L2 approves once. Then C1 / C2 by amount (below).</p>
          {ck('evt_l2', 'Event requests go to the Cluster Manager first', 'Then L1 and L2.')}
          <p className="hint">A property's order on the Central Warehouse goes to its L1 and L2 (and C1 / C2 by amount), then to a Warehouse Approver (L1 or L2), who approves and dispatches it.</p>
          {ck('qty_up', 'Approvers may increase a quantity', 'They can always reduce it or drop an item.')}
          {ck('week_flag', 'Flag requests that take the week over its budget')}
          {ck('po_on_approval', 'The final approval raises the PO (no Purchase Team step)', 'The last approver confirms rates without a rate card and, for Advance-terms vendors, the advance. Off: only sub-categories ticked in Item Setup get their PO at approval; the rest wait for Purchase.')}
          {num('auto_po_days', 'PO raised at approval: deliver this many days later', 'The date the Approve box starts with; the approver can change it.')}
          {num('po_expiry_days', 'Reject an undelivered PO balance this many days after its delivery date', '0 = the morning after. Nothing delivered: the PO is cancelled; a paid advance is carried forward to the vendor\'s next PO. Purchase or an approver can extend a delivery date once.')}
          {ck('receipt_by_requester', 'Goods are received by the person who raised the request', 'The goods receipt is their step (badge, "next step"). Anyone you give the Receiving / Store role at a property can still receive there; at the warehouse the Warehouse Approver L2 and Warehouse Staff receive, at a pre-opening property the Pre-opening Team too. Off: it is the Receiving / Store team\'s (Pre-opening Team\'s, warehouse\'s) step.')}</section>
        <section className="card"><h2>Amount approvals (C1 / C2)</h2>
          <p className="hint">After L2: above the C1 amount the request also goes to the Amount Approver C1; above the C2 amount to C1 and then C2. Amounts include GST; 0 means off. Give people the Amount Approver C1 / C2 roles under Users &amp; Rights.</p>
          <div className="scroll"><table><thead><tr><th>Request type</th><th className="r">C1 above (₹)</th><th className="r">C2 above (₹)</th></tr></thead>
            <tbody>{AMOUNT_TYPES.map(([k, label]) => { const l = s.amount_approvals?.[k] || { c1: 0, c2: 0 }; const set = (patch: Record<string, number>) => save({ amount_approvals: { ...s.amount_approvals, [k]: { ...l, ...patch } } })
              return <tr key={k}><td>{label}</td>
                <td className="r"><input className="qty" type="number" min="0" step="1000" style={{ width: 120 }} defaultValue={l.c1} key={'c1' + l.c1} disabled={!ed} onBlur={(e) => { if (+e.target.value !== l.c1) set({ c1: +e.target.value }) }} aria-label={label + ' C1'} /></td>
                <td className="r"><input className="qty" type="number" min="0" step="1000" style={{ width: 120 }} defaultValue={l.c2} key={'c2' + l.c2} disabled={!ed} onBlur={(e) => { if (+e.target.value !== l.c2) set({ c2: +e.target.value }) }} aria-label={label + ' C2'} /></td></tr> })}</tbody></table></div></section>
        <section className="card"><h2>Utility and AMC bills</h2>
          <p className="hint">A bill at a property goes to the Cluster Manager, then the L1 and L2 of its bill type there (Approval matrix), then C1 / C2 by the Bills amounts.</p>
          {num('bill_jump_pct', 'Flag a bill this much above the previous bill (%)')}
          {num('amc_warn_days', 'Warn this many days before an AMC ends')}
          <label className="field"><span>AMC durations offered (months)</span><input defaultValue={(s.amc_durations || []).join(', ')} key={(s.amc_durations || []).join()} disabled={!ed}
            onBlur={(e) => save({ amc_durations: e.target.value.split(/[,\s]+/).map(Number).filter((x) => x > 0) })} /><span className="hint">Numbers separated by commas.</span></label></section>
        <section className="card"><h2>Invoice and payment</h2>
          {num('match_tol_pct', 'Invoice may differ from goods received by up to (%)', 'Beyond this, Accounts must accept the difference with a remark or send it back.')}
          {num('msme_days', 'MSME vendors must be paid within (days)')}</section>
        <section className="card"><h2>Warehouse, vendor sheets and corporate</h2>
          {ck('need_schedule', 'A delivery must be scheduled before the warehouse can dispatch')}
          {ck('sheet_approved', 'Vendor order sheets include approved requests whose PO is not issued yet')}
          {num('corp_l1_only_below', 'Corporate: L1 alone is enough below this amount (₹)', '0 means L1 and L2 always approve.')}</section>
      </div>
      <section className="card"><h2>Reason lists</h2><div className="grid3">
        {LISTS.map(([k, label]) => <label key={k} className="field"><span>{label}</span>
          <textarea rows={5} disabled={!ed} defaultValue={(lists[k] || []).join('\n')} key={(lists[k] || []).join('|')}
            onBlur={async (e) => { const entries = e.target.value.split('\n').map((x) => x.trim()).filter(Boolean); if (entries.join('|') === (lists[k] || []).join('|')) return
              try { const r = await put('/pick-lists/' + k, { entries }); setLists({ ...lists, [k]: r }); toast('List saved') } catch (err: any) { toast(err.message, true) } }} />
          <span className="hint">One per line.</span></label>)}
      </div><p className="hint">A reason starting with "Other" makes the remark compulsory.</p></section>
    </>
  )
}
