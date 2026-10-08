// Uploads a CSV file: download the sample, check every row, see errors per row, then import only clean rows.
import { useState } from 'react'
import { download, post } from '../api/client'
import { Errors } from '../ui/Errors'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'

interface Row { line: number; values: Record<string, string>; ok: boolean; skip: boolean; message: string; object?: unknown }

export function UploadModal({ kind, title, query = '', onClose, onDone, onRows }: {
  kind: string; title: string; query?: string; onClose: () => void; onDone?: () => void; onRows?: (objects: unknown[]) => void
}) {
  const [text, setText] = useState('')
  const [rows, setRows] = useState<Row[] | null>(null)
  const a = useAction()
  const ok = rows?.filter((r) => r.ok) || []
  const errs = rows?.filter((r) => !r.ok && !r.skip).length || 0
  const heads = rows?.length ? Object.keys(rows[0].values).slice(0, 7) : []
  return (
    <Modal title={title} wide onClose={onClose}>
      <p className="sub">Every row is checked before anything is saved, and only clean rows are imported. Extra columns are ignored.</p>
      <div className="tools">
        <label className="field" style={{ maxWidth: 360 }}><span>Choose a .csv file</span>
          <input type="file" accept=".csv,text/csv" onChange={async (e) => { const f = e.target.files?.[0]; if (f) { setText(await f.text()); setRows(null) } }} /></label>
        <button className="btn ghost" onClick={() => download(`/uploads/${kind}/sample`, `sample-${kind}.csv`)}>Download sample</button>
      </div>
      <textarea className="mono" value={text} onChange={(e) => { setText(e.target.value); setRows(null) }} placeholder="File contents appear here. You can also paste rows." />
      <Errors list={a.errors} />
      {rows && <>
        <p className={'note ' + (errs ? 'warn' : 'ok')}><b>{ok.length} rows ready, {errs} with errors.</b> Rows with errors are not saved; fix them in the file and upload again.</p>
        <div className="scroll" style={{ maxHeight: 360 }}><table>
          <thead><tr><th>Row</th>{heads.map((h) => <th key={h}>{h}</th>)}<th>Result</th></tr></thead>
          <tbody>{rows.slice(0, 200).map((r) => <tr key={r.line}><td>{r.line}</td>{heads.map((h) => <td key={h}>{r.values[h]}</td>)}<td><Pill tone={r.ok ? 'ok' : r.skip ? 'off' : 'bad'}>{r.message}</Pill></td></tr>)}</tbody>
        </table></div>
      </>}
      <div className="btns">
        <button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn ghost" disabled={!text || a.busy} onClick={() => a.run(() => post<{ rows: Row[] }>(`/uploads/${kind}/check${query}`, { text }), (r) => { setRows(r.rows); return '' })}>Check file</button>
        {onRows
          ? <button className="btn" disabled={!ok.length} onClick={() => onRows(ok.map((r) => r.object))}>Add {ok.length} rows</button>
          : <button className="btn" disabled={!ok.length || a.busy} onClick={() => a.run(() => post(`/uploads/${kind}/import${query}`, { text }), (r: any) => { onDone?.(); onClose(); return r.message })}>Import {ok.length} valid rows</button>}
      </div>
    </Modal>
  )
}
