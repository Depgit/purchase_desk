// Loads data from the API for a screen and lets the screen reload it after a change.
import { useCallback, useEffect, useState } from 'react'
import { ApiError, get } from './client'

export function useApi<T>(path: string | null) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [loading, setLoading] = useState(!!path)
  const [tick, setTick] = useState(0)
  const reload = useCallback(() => setTick((t) => t + 1), [])
  useEffect(() => {
    if (!path) { setData(null); setLoading(false); return }
    let live = true
    setLoading(true)
    get<T>(path)
      .then((d) => { if (live) { setData(d); setError(null) } })
      .catch((e) => { if (live) setError(e instanceof ApiError ? e : new ApiError(0, String(e))) })
      .finally(() => { if (live) setLoading(false) })
    return () => { live = false }
  }, [path, tick])
  return { data, error, loading, reload, setData }
}
