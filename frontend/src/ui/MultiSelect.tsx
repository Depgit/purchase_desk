// The one tick-several dropdown of the app. In list filters nothing ticked means everything (the option with an empty
// value names that, e.g. "All clusters"); in forms (placeholder given) nothing ticked shows the placeholder. Long lists
// get a search box, and "Select all" / "Clear". Drawn on document.body like SearchSelect, so cards and modals do not
// clip it.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

interface Props {
  /** The ticked values. */
  value: string[]
  /** [value, label] pairs; an empty value is the "all" label of a filter and is not offered as a tick-box. */
  options: [string, string][]
  onChange: (value: string[]) => void
  ariaLabel?: string
  /** Form use: shown when nothing is ticked (instead of the filter's "all" label). */
  placeholder?: string
  disabled?: boolean
}

export function MultiSelect({ value, options, onChange, ariaLabel, placeholder, disabled }: Props) {
  const [open, setOpen] = useState(false)
  const [find, setFind] = useState('')
  const [pos, setPos] = useState<{ top: number; left: number; width: number; up: boolean } | null>(null)
  const btn = useRef<HTMLButtonElement>(null)
  const box = useRef<HTMLDivElement>(null)
  const allLabel = placeholder || options.find(([v]) => v === '')?.[1] || 'All'
  const choices = useMemo(() => options.filter(([v]) => v !== ''), [options])
  const names = choices.filter(([v]) => value.includes(v)).map(([, l]) => l)
  const text = !names.length ? allLabel : names.length <= 2 ? names.join(', ') : `${names.slice(0, 2).join(', ')} +${names.length - 2} more`
  const needle = find.trim().toLowerCase()
  const shown = needle ? choices.filter(([, l]) => l.toLowerCase().includes(needle)) : choices

  useLayoutEffect(() => {
    if (!open || !btn.current) return
    const r = btn.current.getBoundingClientRect()
    const below = window.innerHeight - r.bottom
    setPos({ top: below < 360 && r.top > below ? r.top - 4 : r.bottom + 4, left: Math.min(r.left, window.innerWidth - Math.max(r.width, 280) - 8), width: Math.max(r.width, 280), up: below < 360 && r.top > below })
  }, [open])
  useEffect(() => {
    if (!open) { setFind(''); return }
    const shut = (e: Event) => { if (!box.current?.contains(e.target as Node) && !btn.current?.contains(e.target as Node)) setOpen(false) }
    const away = (e: Event) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    // Escape closes only this dropdown, not a popup window it sits in (Modal listens on window, after document).
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.stopPropagation(); setOpen(false); btn.current?.focus() } }
    document.addEventListener('mousedown', shut)
    document.addEventListener('keydown', esc)
    window.addEventListener('scroll', away, true)
    window.addEventListener('resize', away)
    return () => { document.removeEventListener('mousedown', shut); document.removeEventListener('keydown', esc); window.removeEventListener('scroll', away, true); window.removeEventListener('resize', away) }
  }, [open])

  const flip = (v: string) => onChange(value.includes(v) ? value.filter((x) => x !== v) : [...value, v])
  const allShown = shown.length > 0 && shown.every(([v]) => value.includes(v))
  return (
    <>
      <button ref={btn} type="button" className={'ssel msel' + (placeholder ? ' form' : '')} disabled={disabled} aria-haspopup="listbox" aria-expanded={open} aria-label={ariaLabel}
        title={names.join(', ') || allLabel} onClick={() => setOpen(!open)}>
        <span className={names.length ? '' : 'ph'}>{text}</span>{names.length > 0 && placeholder && <b className="mcount">{names.length}</b>}<span aria-hidden="true">▾</span>
      </button>
      {open && pos && createPortal(
        <div ref={box} className="ssel-pop" role="dialog" aria-label={ariaLabel}
          style={{ left: pos.left, width: pos.width, ...(pos.up ? { bottom: window.innerHeight - pos.top } : { top: pos.top }) }}>
          {choices.length > 8 && <input type="search" autoFocus placeholder="Search" value={find} onChange={(e) => setFind(e.target.value)} aria-label={'Search ' + (ariaLabel || '')} />}
          <div className="ssel-list" role="listbox" aria-multiselectable="true">
            {shown.map(([v, l]) => (
              <label key={v} className="ck msel-opt"><input type="checkbox" checked={value.includes(v)} onChange={() => flip(v)} />{l}</label>
            ))}
            {!shown.length && <p className="hint">Nothing matches.</p>}
          </div>
          <div className="msel-acts">
            <button type="button" className="btn ghost small" disabled={!shown.length || allShown}
              onClick={() => onChange([...new Set([...value, ...shown.map(([v]) => v)])])}>{needle ? 'Select these' : 'Select all'}</button>
            <button type="button" className="btn ghost small" disabled={!value.length} onClick={() => onChange([])}>{placeholder ? 'Clear' : `Clear: ${allLabel.toLowerCase()}`}</button>
          </div>
        </div>,
        document.body,
      )}
    </>
  )
}
