// Runs a button's action: shows it is busy, collects server errors, shows a message when done.
import { useState } from 'react'
import { ApiError } from '../api/client'
import { useToast } from './Toast'

export function useAction() {
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const [errors, setErrors] = useState<string[]>([])
  async function run<T>(fn: () => Promise<T>, done?: (r: T) => void | string): Promise<T | undefined> {
    setBusy(true); setErrors([])
    try {
      const r = await fn()
      const msg = done ? done(r) : (r as any)?.message
      if (typeof msg === 'string' && msg) toast(msg)
      return r
    } catch (e) {
      const list = e instanceof ApiError ? e.errors : [String(e)]
      setErrors(list)
      return undefined
    } finally {
      setBusy(false)
    }
  }
  return { busy, errors, setErrors, run }
}
