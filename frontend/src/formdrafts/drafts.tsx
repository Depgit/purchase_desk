// Drafts of bill forms: "Save draft" keeps a half-filled goods receipt, utility/AMC bill or corporate form on the
// server (private to the person, attached files included) so it can be finished later on any device. useDraft is the
// form's handle on its draft; DraftList shows the person's saved drafts of a form to open or discard.
import { useEffect, useState } from 'react'
import { api, fileUrl, get, upload } from '../api/client'
import { dateTime } from '../format/format'
import { Errors } from '../ui/Errors'
import { useAction } from '../ui/useAction'

export type DraftForm = 'grn' | 'bill' | 'corporate' | 'request'
export interface DraftFile { field: string; id: number; name: string }
export interface Draft<F = any> { id: number; form: DraftForm; ref_id: number | null; title: string; fields: F; files: DraftFile[]; updated_at: string }
type Files = Record<string, File | File[] | null | undefined>

// A file that came back with a draft, or was saved with one, remembers its id there, so saving again keeps it
// instead of uploading it twice.
const kept = new WeakMap<File, number>()
export const isKept = (f: File | null | undefined) => !!f && kept.has(f)

/** The draft's attached files, fetched back as File objects, by form field. */
export async function restoreFiles(d: Draft): Promise<Record<string, File[]>> {
  const out: Record<string, File[]> = {}
  for (const f of d.files) {
    const res = await fetch(fileUrl(f.id))
    if (!res.ok) continue
    const blob = await res.blob()
    const file = new File([blob], f.name, { type: blob.type })
    kept.set(file, f.id)
    ;(out[f.field] ||= []).push(file)
  }
  return out
}

export function useDraft(form: DraftForm, ref?: number) {
  const [id, setId] = useState(0)
  const [savedAt, setSavedAt] = useState('')
  const a = useAction()
  const save = (title: string, fields: unknown, files: Files) => a.run(async () => {
    const keep: number[] = []
    const fresh: Record<string, File[]> = {}
    for (const [k, v] of Object.entries(files)) {
      for (const f of (Array.isArray(v) ? v : v ? [v] : [])) {
        if (kept.has(f)) keep.push(kept.get(f)!)
        else (fresh[k] ||= []).push(f)
      }
    }
    const r = await upload<{ id: number; files: DraftFile[]; message: string }>('/drafts', { id, form, ref_id: ref ?? null, title, fields, keep }, fresh)
    // Match the newly stored files to the ones in the form, so the next save keeps them too.
    const taken = new Set(keep)
    for (const [k, list] of Object.entries(fresh)) {
      for (const f of list) {
        const m = r.files.find((x) => x.field === k && x.name === f.name && !taken.has(x.id))
        if (m) { kept.set(f, m.id); taken.add(m.id) }
      }
    }
    setId(r.id); setSavedAt(new Date().toISOString())
    return r
  })
  /** Takes a saved draft as this form's draft and gives back its files. */
  const open = (d: Draft) => { setId(d.id); setSavedAt(d.updated_at); return restoreFiles(d) }
  /** Removes the draft once its form has been submitted. */
  const clear = () => { if (id) api('DELETE', '/drafts/' + id).catch(() => {}); setId(0); setSavedAt('') }
  const discard = () => a.run(async () => { if (id) await api('DELETE', '/drafts/' + id); setId(0); setSavedAt(''); return { message: 'Draft discarded' } })
  return { id, savedAt, busy: a.busy, errors: a.errors, save, open, clear, discard }
}

export type DraftHandle = ReturnType<typeof useDraft>

/** The Save draft button with when it was last saved, and any error saving it. */
export function DraftControls({ draft, onSave }: { draft: DraftHandle; onSave: () => void }) {
  return (
    <>
      {draft.savedAt && <span className="hint">Draft saved {dateTime(draft.savedAt)} · <button className="link" disabled={draft.busy} onClick={() => draft.discard()}>Discard draft</button></span>}
      <button className="btn ghost" disabled={draft.busy} onClick={onSave} title="Keep what is filled in, attached files included, and finish it later">Save draft</button>
      {draft.errors.length > 0 && <div style={{ flexBasis: '100%' }}><Errors list={draft.errors} /></div>}
    </>
  )
}

/** Shows that a file in the form came back with the draft (a file input cannot show it). */
export function KeptFiles({ files }: { files: (File | null | undefined)[] }) {
  const names = files.filter(isKept).map((f) => f!.name)
  return names.length ? <span className="hint">Kept with the draft: {names.join(', ')}</span> : null
}

/** The person's saved drafts of a form, newest first, to open or discard. */
export function DraftList({ form, only, onOpen, label = 'Your drafts' }: { form: DraftForm; only?: (d: Draft) => boolean; onOpen: (d: Draft) => void; label?: string }) {
  const [list, setList] = useState<Draft[]>([])
  const [n, setN] = useState(0)
  const a = useAction()
  useEffect(() => { get<Draft[]>('/drafts?form=' + form).then(setList).catch(() => setList([])) }, [form, n])
  const shown = list.filter((d) => !only || only(d))
  if (!shown.length) return null
  return (
    <section className="card drafts">
      <h2>{label} · {shown.length}</h2>
      {shown.map((d) => (
        <div key={d.id} className="draftrow">
          <span><b>{d.title}</b><br /><span className="hint">Saved {dateTime(d.updated_at)}{d.files.length ? ` · ${d.files.length} file${d.files.length === 1 ? '' : 's'}` : ''}</span></span>
          <span className="acts"><button className="link" disabled={a.busy} onClick={() => a.run(() => api('DELETE', '/drafts/' + d.id), () => { setN(n + 1); return 'Draft discarded' })}>Discard</button>
            <button className="btn small" onClick={() => onOpen(d)}>Open</button></span>
        </div>))}
      <Errors list={a.errors} />
    </section>
  )
}
