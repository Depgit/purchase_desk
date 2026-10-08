// A labelled form control with an optional hint, plus small inputs bound to a value.
import type { ReactNode } from 'react'

export function Field({ label, hint, children }: { label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}{hint ? <span className="hint">{hint}</span> : null}</label>
}

export function Select<T extends string | number>({ value, onChange, options, placeholder, disabled }: {
  value: T | '' | null | undefined; onChange: (v: T) => void; options: [T, string][]; placeholder?: string; disabled?: boolean
}) {
  return (
    <select value={value == null ? '' : String(value)} disabled={disabled}
      onChange={(e) => {
        const raw = e.target.value
        const found = options.find((o) => String(o[0]) === raw)
        onChange((found ? found[0] : raw) as T)
      }}>
      {placeholder != null && <option value="">{placeholder}</option>}
      {options.map(([v, l]) => <option key={String(v)} value={String(v)}>{l}</option>)}
    </select>
  )
}

export function Checks<T extends string | number>({ value, onChange, options }: { value: T[]; onChange: (v: T[]) => void; options: [T, string][] }) {
  return (
    <div className="ckrow">
      {options.map(([v, l]) => (
        <label className="ck" key={String(v)}>
          <input type="checkbox" checked={value.includes(v)} onChange={(e) => onChange(e.target.checked ? [...value, v] : value.filter((x) => x !== v))} />{l}
        </label>
      ))}
    </div>
  )
}
