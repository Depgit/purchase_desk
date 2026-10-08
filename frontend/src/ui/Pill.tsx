// A small coloured label for statuses and flags.
import type { ReactNode } from 'react'

export type Tone = '' | 'ok' | 'warn' | 'bad' | 'off'
export function Pill({ tone = '', children }: { tone?: Tone; children: ReactNode }) {
  return <span className={'pill ' + tone}>{children}</span>
}

/** Colour for a document or invoice status. */
export function statusTone(status: string): Tone {
  if (['REJECTED', 'CANCELLED', 'WH_BACK', 'VOID'].includes(status)) return 'bad'
  if (['PENDING', 'RETURNED', 'WH_SENT', 'PUSHED'].includes(status)) return 'warn'
  if (['CLOSED', 'PAID', 'WH_DONE'].includes(status)) return 'ok'
  return ''
}
