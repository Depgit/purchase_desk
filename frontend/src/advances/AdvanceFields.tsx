// The fields of an advance asked of Finance: how much (a % of the PO value with quick picks, or an amount), the reason,
// a note and the vendor's proforma, with a preview of what the vendor gets. Used inside the PO form when the payment
// terms are Advance, and on the PO page for a further advance.
import { useEffect, useState } from 'react'
import { get } from '../api/client'
import { inr } from '../format/format'
import { Seg } from '../ui/Tabs'

export interface AdvanceDraft { mode: 'pct' | 'amount'; val: string; reason: string; note: string; file: File | null }

const PICKS = [25, 50, 75, 100]

export function newAdvance(): AdvanceDraft { return { mode: 'pct', val: '50', reason: '', note: '', file: null } }

/** The advance in rupees for a PO value. */
export const advanceAmount = (a: AdvanceDraft, poValue: number) => a.mode === 'pct' ? Math.round(poValue * (+a.val || 0)) / 100 : Math.round((+a.val || 0) * 100) / 100

/** What is still wrong with the advance, or '' when it can be sent. room is how much more can be asked on the PO. */
export function advanceProblem(a: AdvanceDraft, poValue: number, room: number) {
  const amt = advanceAmount(a, poValue)
  if (amt <= 0) return 'Enter the advance as a % of the PO value or an amount.'
  if (amt > room + 0.01) return `At most ${inr(room)} can be asked as advance.`
  if (!a.reason) return 'Choose the reason for the advance.'
  if (a.reason.startsWith('Other') && a.note.trim().length < 3) return 'Write a note for "Other".'
  return ''
}

/** The body the API takes: pct or amount, the reason and the note. */
export const advanceBody = (a: AdvanceDraft, poValue: number) => a.mode === 'pct'
  ? { pct: +a.val, reason: a.reason, note: a.note } : { amount: advanceAmount(a, poValue), reason: a.reason, note: a.note }

export function AdvanceFields({ a, onChange, poValue, room, poTDS = 0, asked = 0, poLabel }: {
  a: AdvanceDraft; onChange: (a: AdvanceDraft) => void; poValue: number; room: number; poTDS?: number; asked?: number; poLabel: string
}) {
  const [reasons, setReasons] = useState<string[]>([])
  useEffect(() => {
    get<Record<string, string[]>>('/pick-lists').then((l) => {
      const list = l.advance_reasons || []
      setReasons(list)
      if (!a.reason && list[0]) onChange({ ...a, reason: list[0] })
    }).catch(() => {})
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const amount = advanceAmount(a, poValue)
  const pct = poValue > 0 ? Math.round((amount / poValue) * 1000) / 10 : 0
  const tds = poValue > 0 && poTDS > 0 ? Math.round(amount * poTDS / poValue) : 0
  const over = amount > room + 0.01
  const other = a.reason.startsWith('Other')
  return (
    <>
      <div className="field"><span>How much</span>
        <Seg label="Advance as" value={a.mode} onChange={(m) => onChange({ ...a, mode: m, val: m === 'pct' ? '50' : '' })} items={[['pct', '% of PO value'], ['amount', 'Amount in ₹']]} /></div>
      {a.mode === 'pct'
        ? <div className="field"><span>Percent of the PO value</span>
          <div className="picks">{PICKS.map((p) => <button key={p} className={+a.val === p ? 'on' : ''} disabled={poValue * p / 100 > room + 0.01} title={poValue * p / 100 > room + 0.01 ? 'More than can still be asked' : undefined}
            onClick={() => onChange({ ...a, val: String(p) })}>{p}%</button>)}
            <input type="number" min="1" max="100" style={{ width: 110 }} value={a.val} onChange={(e) => onChange({ ...a, val: e.target.value })} aria-label="Percent" /></div></div>
        : <label className="field"><span>Amount including GST (₹)</span>
          <input type="number" min="0" step="0.01" inputMode="decimal" value={a.val} onChange={(e) => onChange({ ...a, val: e.target.value })} placeholder={`Up to ${inr(room)}`} /></label>}
      {amount > 0 && <div className={'outcome' + (over ? ' bad' : '')}>
        {over ? <div><b>Too much.</b> At most {inr(room)} can be asked on {poLabel}.</div> : <>
          <div><b>Advance {inr(amount)}</b> · {pct}% of the PO</div>
          {tds > 0 && <div>TDS {inr(tds)} is held back on the service items, so the vendor gets <b>{inr(amount - tds)}</b>.</div>}
          <div>Left to pay after delivery: about {inr(poValue - asked - amount)}{asked > 0 ? ', counting the advances already asked' : ''}.</div></>}
      </div>}
      <div className="grid2">
        <label className="field"><span>Reason</span><select value={a.reason} onChange={(e) => onChange({ ...a, reason: e.target.value })}>{reasons.map((r) => <option key={r}>{r}</option>)}</select></label>
        <label className="field"><span>Note{other ? ' (needed for Other)' : ' (optional)'}</span><input value={a.note} onChange={(e) => onChange({ ...a, note: e.target.value })} placeholder="e.g. vendor's terms on the quotation" /></label>
      </div>
      <label className="field"><span>Vendor's proforma invoice or quotation</span>
        <input type="file" accept="application/pdf,image/*" onChange={(e) => onChange({ ...a, file: e.target.files?.[0] || null })} />
        <span className="hint">PDF or photo. Finance sees it when approving.</span></label>
    </>
  )
}
