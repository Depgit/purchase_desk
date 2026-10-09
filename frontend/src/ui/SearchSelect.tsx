// A dropdown you can type into: it opens a search box over the list and shows only the matches.
// Used where a plain <select> is too long to scroll (Zoho vendors, accounts, reporting tag options).
// Keyboard: arrows move, Enter picks, Escape closes. Groups appear as headings, like <optgroup>.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

export interface SearchItem { value: string; label: string; group?: string; hint?: string; /** shown greyed out, cannot be picked */ disabled?: boolean }

interface Props {
  value: string
  items: SearchItem[]
  onChange: (value: string) => void
  /** Label of the empty choice ("Not mapped"). Leave out to offer no empty choice. */
  emptyLabel?: string
  placeholder?: string
  disabled?: boolean
  ariaLabel?: string
}

const MAX_SHOWN = 300 // enough for any search; a long list without a search asks the person to type

export function SearchSelect({ value, items, onChange, emptyLabel, placeholder = 'Type to search', disabled, ariaLabel }: Props) {
  const [open, setOpen] = useState(false)
  const [q, setQ] = useState('')
  const [hi, setHi] = useState(0)
  const [pos, setPos] = useState<{ top: number; left: number; width: number; up: boolean } | null>(null)
  const btn = useRef<HTMLButtonElement>(null)
  const box = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLInputElement>(null)

  const current = items.find((i) => i.value === value)
  const list = useMemo(() => {
    const words = q.toLowerCase().split(/\s+/).filter(Boolean)
    const hit = items.filter((i) => words.every((w) => (i.label + ' ' + (i.group || '') + ' ' + (i.hint || '')).toLowerCase().includes(w)))
    const all = emptyLabel !== undefined && !q ? [{ value: '', label: emptyLabel } as SearchItem, ...hit] : hit
    return all.slice(0, MAX_SHOWN)
  }, [items, q, emptyLabel])
  const more = items.length > MAX_SHOWN && !q

  // The list is drawn on document.body with fixed coordinates. Inside a card it would be placed relative to the card
  // (cards use backdrop-filter, which captures position:fixed) and slide under the next card; tables would also clip it.
  useLayoutEffect(() => {
    if (!open || !btn.current) return
    const r = btn.current.getBoundingClientRect()
    const up = r.bottom + 340 > window.innerHeight && r.top > 340
    setPos({ top: up ? r.top - 4 : r.bottom + 4, left: r.left, width: Math.max(r.width, 300), up })
  }, [open])
  useEffect(() => {
    if (!open) return
    const shut = (e: Event) => { if (!box.current?.contains(e.target as Node) && !btn.current?.contains(e.target as Node)) setOpen(false) }
    const away = (e: Event) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', shut)
    window.addEventListener('scroll', away, true)
    window.addEventListener('resize', away)
    return () => { document.removeEventListener('mousedown', shut); window.removeEventListener('scroll', away, true); window.removeEventListener('resize', away) }
  }, [open])
  // Focus the search box once the list is drawn (it appears only after its position is known).
  useEffect(() => { if (open && pos) input.current?.focus() }, [open, pos])
  useEffect(() => { setHi(0) }, [q])

  const pick = (v: string) => { if (list.find((i) => i.value === v)?.disabled) return; setOpen(false); setQ(''); if (v !== value) onChange(v) }
  const key = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setHi((h) => Math.min(h + 1, list.length - 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setHi((h) => Math.max(h - 1, 0)) }
    else if (e.key === 'Enter') { e.preventDefault(); if (list[hi]) pick(list[hi].value) }
    else if (e.key === 'Escape') { setOpen(false); btn.current?.focus() }
  }
  useEffect(() => { box.current?.querySelector('[data-hi="1"]')?.scrollIntoView({ block: 'nearest' }) }, [hi])

  let lastGroup: string | undefined
  return (
    <>
      <button ref={btn} type="button" className="ssel" disabled={disabled} aria-haspopup="listbox" aria-expanded={open} aria-label={ariaLabel}
        onClick={() => setOpen(!open)} onKeyDown={(e) => { if (e.key === 'ArrowDown' && !open) { e.preventDefault(); setOpen(true) } }}>
        <span>{current ? current.label : value ? value : emptyLabel ?? 'Choose…'}</span><span aria-hidden="true">▾</span>
      </button>
      {open && pos && createPortal(
        <div ref={box} className="ssel-pop" role="dialog" style={{ left: pos.left, width: pos.width, ...(pos.up ? { bottom: window.innerHeight - pos.top } : { top: pos.top }) }}>
          <input ref={input} type="search" value={q} placeholder={placeholder} aria-label={placeholder} onChange={(e) => setQ(e.target.value)} onKeyDown={key} />
          <div className="ssel-list" role="listbox">
            {list.length === 0 && <p className="hint">No match for "{q}".</p>}
            {list.map((i, n) => {
              const head = i.group && i.group !== lastGroup ? <div className="ssel-group" key={'g' + i.group}>{i.group}</div> : null
              lastGroup = i.group
              return [head, <button key={i.value || 'empty'} type="button" role="option" aria-selected={i.value === value} data-hi={n === hi ? '1' : '0'}
                aria-disabled={i.disabled || undefined} className={'ssel-opt' + (n === hi ? ' hi' : '') + (i.value === value ? ' on' : '') + (i.disabled ? ' off' : '')} onMouseEnter={() => setHi(n)} onClick={() => pick(i.value)}>
                <span>{i.label}</span>{i.hint && <small>{i.hint}</small>}</button>]
            })}
            {more && <p className="hint">Showing the first {MAX_SHOWN} of {items.length}. Type to find the rest.</p>}
          </div>
        </div>,
        document.body,
      )}
    </>
  )
}
