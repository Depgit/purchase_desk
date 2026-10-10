// The per-item outcome of a bulk action: which went through and why the others did not.
import { Pill } from './Pill'

export interface BulkResult { id: number; ok: boolean; message: string }

export function BulkResults({ title, results, label, onClose }: { title: string; results: BulkResult[]; label: (id: number) => string; onClose: () => void }) {
  return (
    <section className="card">
      <div className="shead"><h2>{title}</h2><button className="link" onClick={onClose}>Hide</button></div>
      {results.map((r) => <p key={r.id}><Pill tone={r.ok ? 'ok' : 'bad'}>{r.ok ? 'Done' : 'Not done'}</Pill> {label(r.id)}: {r.message}</p>)}
    </section>
  )
}
