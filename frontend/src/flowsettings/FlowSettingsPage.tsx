// The rules the application runs on. The Admin changes them here; a change applies to new requests at once and is logged.
import { useEffect, useState } from 'react'
import { get, put } from '../api/client'
import { useSession } from '../auth/session'
import { PHASE, usePhase } from '../releasephase/phase'
import { useToast } from '../ui/Toast'

type S = Record<string, any>
const LISTS: [string, string, number][] = [['event_reasons', 'Event reasons on a request', PHASE.core], ['return_reasons', 'Reasons to return a request', PHASE.core], ['reject_reasons', 'Reasons to reject', PHASE.core],
  ['route_reasons', 'Reasons to send to the Cluster Manager', PHASE.core], ['balance_reasons', 'Reasons to reject an undelivered PO balance', PHASE.core], ['advance_reasons', 'Reasons for a vendor advance', PHASE.core], ['wh_back_reasons', 'Reasons the warehouse cannot supply', PHASE.warehouse], ['delivery_slots', 'Delivery time slots', PHASE.warehouse]]

export function FlowSettingsPage() {
  const { me } = useSession()
  const { on } = usePhase() // settings of later phases appear when RELEASE_PHASE reaches them
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
          {ck('evt_l2', 'Event requests go to the Cluster Manager first')}
          {num('l2_above', 'Also send to the Cluster Manager above this amount (₹)', '0 means never by amount.')}
          {ck('cc_all', 'The Cost Controller approves every request', 'If off, a request that already went to the Cluster Manager skips the Cost Controller.')}
          {on(PHASE.warehouse) && ck('wh_approval', 'Property orders on the warehouse need approval', 'If off, they become a transfer order as soon as they are raised.')}
          {ck('qty_up', 'Approvers may increase a quantity', 'They can always reduce it or drop an item.')}
          {on(PHASE.budgets) && ck('week_flag', 'Flag requests that take the week over its budget')}</section>
        {on(PHASE.bills) && <section className="card"><h2>Utility and AMC bills</h2>
          <label className="field"><span>Bills are approved by</span><select value={s.bill_approver} disabled={!ed} onChange={(e) => save({ bill_approver: e.target.value })}><option value="l2">Cluster Manager</option><option value="cc">Cost Controller</option></select></label>
          {num('bill_jump_pct', 'Flag a bill this much above the previous bill (%)')}
          {num('amc_warn_days', 'Warn this many days before an AMC ends')}
          <label className="field"><span>AMC durations offered (months)</span><input defaultValue={(s.amc_durations || []).join(', ')} key={(s.amc_durations || []).join()} disabled={!ed}
            onBlur={(e) => save({ amc_durations: e.target.value.split(/[,\s]+/).map(Number).filter((x) => x > 0) })} /><span className="hint">Numbers separated by commas.</span></label></section>}
        <section className="card"><h2>Invoice and payment</h2>
          {num('match_tol_pct', 'Invoice may differ from goods received by up to (%)', 'Beyond this, Accounts must accept the difference with a remark or send it back.')}
          {num('msme_days', 'MSME vendors must be paid within (days)')}</section>
        {on(PHASE.corporate) && <section className="card"><h2>{on(PHASE.warehouse) ? 'Warehouse, vendor sheets and corporate' : 'Corporate'}</h2>
          {on(PHASE.warehouse) && ck('need_schedule', 'A delivery must be scheduled before the warehouse can dispatch')}
          {on(PHASE.sheets) && ck('sheet_approved', 'Vendor order sheets include approved requests whose PO is not issued yet')}
          {num('corp_l1_only_below', 'Corporate: L1 alone is enough below this amount (₹)', '0 means L1 and L2 always approve.')}</section>}
      </div>
      <section className="card"><h2>Reason lists</h2><div className="grid3">
        {LISTS.filter(([, , ph]) => on(ph)).map(([k, label]) => <label key={k} className="field"><span>{label}</span>
          <textarea rows={5} disabled={!ed} defaultValue={(lists[k] || []).join('\n')} key={(lists[k] || []).join('|')}
            onBlur={async (e) => { const entries = e.target.value.split('\n').map((x) => x.trim()).filter(Boolean); if (entries.join('|') === (lists[k] || []).join('|')) return
              try { const r = await put('/pick-lists/' + k, { entries }); setLists({ ...lists, [k]: r }); toast('List saved') } catch (err: any) { toast(err.message, true) } }} />
          <span className="hint">One per line.</span></label>)}
      </div><p className="hint">A reason starting with "Other" makes the remark compulsory.</p></section>
    </>
  )
}
