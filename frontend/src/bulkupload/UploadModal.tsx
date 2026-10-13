// Uploads a CSV file or an edit workbook: check every row, see errors per row, then import only clean rows.
// For master lists, "Download current data" gives an Excel file with the page's list and the masters it depends on
// (Vendor Mapping: mappings and their vendors), each on its own sheet. Edit it and upload it back: rows with a key are
// updated, rows without one are added, and unchanged rows are skipped.
import { useState } from 'react'
import { download, post } from '../api/client'
import { Errors } from '../ui/Errors'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'

// Uploads that can be downloaded with their current data, edited and uploaded back.
const EDITABLE = new Set(['items', 'properties', 'vendors', 'users', 'mappings', 'rates', 'budgets', 'property_pct', 'zoho_tags', 'clusters', 'sub_categories', 'pack_sizes'])

// How rows tie to records, for lists that are not matched by a code column of their own.
const MATCH: Record<string, string> = {
  clusters: 'Cities and clusters are matched by name: a name already on file updates it, a new name is added. Rename them on this page. The Cities sheet is applied first, so a cluster can use a city added in the same file. A CSV file is read as clusters.',
  pack_sizes: 'Pack sizes are matched by name: new names are added, and a name already in the list (in any spelling, for example Box of 500 and box-of-500) is refused as already there. Rename or deactivate one on this page.',
  sub_categories: 'Categories are matched by name (rename them on this page); sub-categories by their 2-letter code, so a row can rename a sub-category or move it to another category. A new code adds a sub-category whose SKU codes start with those letters. The Categories sheet is applied first. A CSV file is read as sub-categories.',
}

interface Row { sheet: string; line: number; values: Record<string, string>; ok: boolean; skip: boolean; message: string; object?: unknown }

// The file to upload: CSV text, or an Excel workbook sent as base64.
type Upload = { text: string } | { xlsx: string; name: string }

async function readFile(f: File): Promise<Upload> {
  if (!/\.xlsx$/i.test(f.name)) return { text: await f.text() }
  const bytes = new Uint8Array(await f.arrayBuffer())
  let bin = ''
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return { xlsx: btoa(bin), name: f.name }
}

// What a result row is about: its key column, or its first few cells.
function label(r: Row) {
  const v = r.values
  const key = v.mapping_id || v.sku_code || v.vendor_code || v.property_code || v.email || v.mobile || v.code
  const name = v.legal_name || v.item_name || v.property_name || v.name || v.sub_category || v.vendor_name || v.cluster || v.city || v.category || v.pack_size || ''
  return [key, name].filter(Boolean).join(' · ') || Object.values(v).filter(Boolean).slice(0, 3).join(' · ')
}

export function UploadModal({ kind, title, query = '', exportQuery = '', onClose, onDone, onRows }: {
  kind: string; title: string; query?: string; exportQuery?: string; onClose: () => void; onDone?: () => void; onRows?: (objects: unknown[]) => void
}) {
  const [file, setFile] = useState<Upload | null>(null)
  const [rows, setRows] = useState<Row[] | null>(null)
  const a = useAction()
  const ok = rows?.filter((r) => r.ok) || []
  const errs = rows?.filter((r) => !r.ok && !r.skip).length || 0
  const same = rows?.filter((r) => r.skip && r.message === 'No change').length || 0
  const shown = (rows || []).filter((r) => !(r.skip && r.message === 'No change'))
  const sheets = new Set((rows || []).map((r) => r.sheet).filter(Boolean)).size > 0
  return (
    <Modal title={title} wide onClose={onClose}>
      <p className="sub">Every row is checked before anything is saved, and only clean rows are imported. Extra columns are ignored.</p>
      {EDITABLE.has(kind) && <p className="note"><b>To edit in bulk:</b> download the current data (an Excel file with this list and the masters it uses, one sheet each), change what you need, save it and upload it here.
        {MATCH[kind] ? MATCH[kind] + ' Rows you did not change are skipped.' : 'Rows with their key filled are updated, rows with the key blank are added, and rows you did not change are skipped.'} The file's "Read me" sheet lists the keys and what is locked.</p>}
      <div className="tools">
        <label className="field" style={{ maxWidth: 360 }}><span>Choose a file (.xlsx or .csv)</span>
          <input type="file" accept=".xlsx,.csv,text/csv,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" onChange={async (e) => { const f = e.target.files?.[0]; if (f) { setFile(await readFile(f)); setRows(null) } }} /></label>
        {EDITABLE.has(kind) && <button className="btn ghost" disabled={a.busy} onClick={() => a.run(() => download(`/uploads/${kind}/workbook${exportQuery}`, `${kind}.xlsx`), () => 'Downloaded')}>Download current data (Excel)</button>}
        <button className="btn ghost" onClick={() => download(`/uploads/${kind}/sample`, `sample-${kind}.csv`)}>Download sample</button>
      </div>
      {file && 'xlsx' in file
        ? <p className="note">Excel file: <b>{file.name}</b>. Every sheet that belongs to this page is checked, in order.</p>
        : <textarea className="mono" value={file && 'text' in file ? file.text : ''} onChange={(e) => { setFile({ text: e.target.value }); setRows(null) }} placeholder="CSV contents appear here. You can also paste rows." />}
      <Errors list={a.errors} />
      {rows && <>
        <p className={'note ' + (errs ? 'warn' : 'ok')}><b>{ok.length} rows ready, {errs} with errors{same ? `, ${same} unchanged` : ''}.</b> Rows with errors are not saved; fix them in the file and upload again.</p>
        <div className="scroll" style={{ maxHeight: 360 }}><table>
          <thead><tr>{sheets && <th>Sheet</th>}<th>Row</th><th>Record</th><th>Result</th></tr></thead>
          <tbody>{shown.slice(0, 300).map((r) => <tr key={r.sheet + r.line}>{sheets && <td>{r.sheet}</td>}<td>{r.line > 0 ? r.line : '—'}</td><td>{label(r)}</td>
            <td><Pill tone={r.ok ? 'ok' : r.skip ? 'off' : 'bad'}>{r.message}</Pill></td></tr>)}</tbody>
        </table></div>
        {!shown.length && <p className="sub">Nothing was changed in this file.</p>}
      </>}
      <div className="btns">
        <button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn ghost" disabled={!file || a.busy} onClick={() => a.run(() => post<{ rows: Row[] }>(`/uploads/${kind}/check${query}`, file), (r) => { setRows(r.rows); return '' })}>Check file</button>
        {onRows
          ? <button className="btn" disabled={!ok.length} onClick={() => onRows(ok.map((r) => r.object))}>Add {ok.length} rows</button>
          : <button className="btn" disabled={!ok.length || a.busy} onClick={() => a.run(() => post(`/uploads/${kind}/import${query}`, file), (r: any) => { onDone?.(); onClose(); return r.message })}>Import {ok.length} rows</button>}
      </div>
    </Modal>
  )
}
