// Formats rupees, dates and times the Indian way.
export function inr(v: number | null | undefined): string {
  if (v == null || isNaN(v)) return '—'
  return '₹' + Math.round(v).toLocaleString('en-IN')
}
export function inr2(v: number | null | undefined): string {
  if (v == null || isNaN(v)) return '—'
  return '₹' + v.toLocaleString('en-IN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}
export function day(d: string | null | undefined): string {
  if (!d) return '—'
  const t = new Date(d.length === 10 ? d + 'T00:00:00' : d)
  return isNaN(+t) ? d : t.toLocaleDateString('en-IN', { day: 'numeric', month: 'short', year: 'numeric' })
}
export function shortDay(d: string | null | undefined): string {
  if (!d) return '—'
  const t = new Date(d.length === 10 ? d + 'T00:00:00' : d)
  return isNaN(+t) ? d : t.toLocaleDateString('en-IN', { day: 'numeric', month: 'short' })
}
export function dateTime(d: string | null | undefined): string {
  if (!d) return '—'
  return new Date(d).toLocaleString('en-IN', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
}
export function month(ym: string): string {
  if (!ym) return '—'
  return new Date(ym + '-01T00:00:00').toLocaleDateString('en-IN', { month: 'short', year: 'numeric' })
}
export function today(): string {
  const n = new Date()
  return `${n.getFullYear()}-${String(n.getMonth() + 1).padStart(2, '0')}-${String(n.getDate()).padStart(2, '0')}`
}
export function thisMonth(): string { return today().slice(0, 7) }
export function addDays(ymd: string, n: number): string {
  const t = new Date(ymd + 'T00:00:00')
  t.setDate(t.getDate() + n)
  return `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, '0')}-${String(t.getDate()).padStart(2, '0')}`
}
export function qty(v: number): string { return Number.isInteger(v) ? String(v) : v.toFixed(3).replace(/0+$/, '') }
export function plural(n: number, one: string, many: string) { return `${n} ${n === 1 ? one : many}` }
export function lineValue(q: number, rate: number, gst: number) { return q * rate * (1 + gst / 100) }

/** Days to ask a document list for so a date range starting on `from` is complete: at least the usual 90. */
export function daysBack(from: string): number {
  if (!from) return 90
  return Math.max(90, Math.ceil((Date.now() - new Date(from + 'T00:00:00').getTime()) / 86400000) + 1)
}
