// Which release phase is switched on (RELEASE_PHASE on the server) and which phase each feature belongs to.
// Phase N shows that phase and every phase before it. The server blocks later phases too; this only decides what to show.
import type { ReactNode } from 'react'
import { useSession } from '../auth/session'

export const PHASE = {
  core: 1,
  zoho: 2,
  bills: 3,
  budgets: 3,
  corporate: 3,
  warehouse: 4,
  sheets: 4,
  dashboard: 5,
  reports: 5,
} as const

export function usePhase() {
  const { me } = useSession()
  const current = me?.phase || 5
  return { current, on: (phase: number) => current >= phase }
}

/** Shows the screen only when its phase is switched on. */
export function PhaseGate({ phase, children }: { phase: number; children: ReactNode }) {
  const { on } = usePhase()
  if (on(phase)) return <>{children}</>
  return (
    <div className="head"><div><h1>Not open yet</h1>
      <p className="sub">This part of Purchase Desk opens in a later phase of the rollout.</p></div></div>
  )
}
