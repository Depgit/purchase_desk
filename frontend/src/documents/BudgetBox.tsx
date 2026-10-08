// Shows, for a request waiting for approval, how it sits against the month, week and day budget of each sub-category.
import type { Doc } from '../api/types'
import { inr } from '../format/format'

export function BudgetBox({ doc }: { doc: Doc }) {
  if (doc.property_kind !== 'live') return <p className="note">No monthly budget applies to the warehouse or a pre-opening property.</p>
  return (
    <section className="card"><h2>Budget at {doc.property}</h2>
      {doc.budget.map((f) => {
        const name = doc.lines.find((l) => l.sub_category_id === f.sub_category_id)?.sub_category || 'Sub-category'
        const tone = f.over_month > 0 ? 'bad' : f.over_week > 0 ? 'warn' : 'ok'
        return (
          <div key={f.sub_category_id} className={'note ' + tone}>
            <b>{name}</b><br />
            This request is {inr(f.amount)}. {f.over_month > 0 ? `It goes over the month budget by ${inr(f.over_month)}.` : f.over_week > 0 ? `It fits the month, but takes week ${f.week} over its budget by ${inr(f.over_week)}.` : 'It fits the month and the week budget.'}<br />
            Month: {inr(f.month_spent)} of {inr(f.month_budget)} · Week {f.week}: {inr(f.week_spent)} of {inr(f.week_budget)} · Today: {inr(f.day_spent)} of {inr(f.day_budget)}
            {f.is_fb && <><br />Day and week budgets follow the uploaded F&amp;B sales.</>}
          </div>
        )
      })}
    </section>
  )
}
