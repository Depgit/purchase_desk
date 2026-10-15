// The Status filter of every list of masters: active, inactive, or both (nothing ticked). DataTable adds it by itself
// to any list whose rows carry an `active` flag; pages with their own tables use useActiveFilter.
import { useState } from 'react'
import { MultiSelect } from './MultiSelect'

export const ACTIVE_OPTIONS: [string, string][] = [['', 'Active and inactive'], ['1', 'Active'], ['0', 'Inactive']]

/** Whether a row with this active flag passes the ticked statuses ('1' active, '0' inactive; none = all). */
export const activePasses = (active: boolean, ticked: string[]) => !ticked.length || ticked.includes(active ? '1' : '0')

/** A Status dropdown and its test, for a page that draws its own table. */
export function useActiveFilter() {
  const [ticked, setTicked] = useState<string[]>([])
  const control = <MultiSelect ariaLabel="Status" value={ticked} onChange={setTicked} options={ACTIVE_OPTIONS} />
  return { control, passes: (active: boolean) => activePasses(active, ticked) }
}
