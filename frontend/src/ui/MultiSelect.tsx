// A dropdown of tick-boxes for list filters where more than one value can be chosen (for example several roles).
// Nothing ticked means everything. Drawn on document.body like SearchSelect, so cards and tables do not clip it.
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

interface Props {
  /** The ticked values. */
  value: string[]
  /** [value, label] pairs; an empty value is the "all" label and is not offered as a tick-box. */
  options: [string, string][]
  onChange: (value: string[]) => void
  ariaLabel?: string
}

export function MultiSelect({ value, options, onChange, ariaLabel }: Props) {
  const [open, setOpen] = useState(false)
  const [pos, setPos] = useState<{ top: number; left: number; width: number } | null>(null)
  const btn = useRef<HTMLButtonElement>(null)
  const box = useRef<HTMLDivElement>(null)
  const allLabel = options.find(([v]) => v === '')?.[1] || 'All'
  const choices = options.filter(([v]) => v !== '')
  const names = choices.filter(([v]) => value.includes(v)).map(([, l]) => l)
  const text = !names.length ? allLabel : names.length <= 2 ? names.join(', ') : `${names.length} ${(ariaLabel || 'values').toLowerCase()}s`

  useLayoutEffect(() => {
    if (!open || !btn.current) return
    const r = btn.current.getBoundingClientRect()
    setPos({ top: r.bottom + 4, left: r.left, width: Math.max(r.width, 260) })
  }, [open])
  useEffect(() => {
    if (!open) return
    const shut = (e: Event) => { if (!box.current?.contains(e.target as Node) && !btn.current?.contains(e.target as Node)) setOpen(false) }
    const away = (e: Event) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') { setOpen(false); btn.current?.focus() } }
    document.addEventListener('mousedown', shut)
    document.addEventListener('keydown', esc)
    window.addEventListener('scroll', away, true)
    window.addEventListener('resize', away)
    return () => { document.removeEventListener('mousedown', shut); document.removeEventListener('keydown', esc); window.removeEventListener('scroll', away, true); window.removeEventListener('resize', away) }
  }, [open])

  const flip = (v: string) => onChange(value.includes(v) ? value.filter((x) => x !== v) : [...value, v])
  return (
    <>
      <button ref={btn} type="button" className="ssel msel" aria-haspopup="listbox" aria-expanded={open} aria-label={ariaLabel} title={names.join(', ') || allLabel} onClick={() => setOpen(!open)}>
        <span>{text}</span><span aria-hidden="true">▾</span>
      </button>
      {open && pos && createPortal(
        <div ref={box} className="ssel-pop" role="dialog" aria-label={ariaLabel} style={{ left: pos.left, width: pos.width, top: pos.top }}>
          <div className="ssel-list" role="listbox" aria-multiselectable="true">
            {choices.map(([v, l]) => (
              <label key={v} className="ck msel-opt"><input type="checkbox" checked={value.includes(v)} onChange={() => flip(v)} />{l}</label>
            ))}
          </div>
          <button type="button" className="btn ghost small" disabled={!value.length} onClick={() => onChange([])}>Clear: {allLabel.toLowerCase()}</button>
        </div>,
        document.body,
      )}
    </>
  )
}
