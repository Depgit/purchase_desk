// Talks to the vendor portal API with the vendor's own session (kept apart from a staff sign-in in the same browser).
import { ApiError } from '../api/client'

const TOKEN_KEY = 'pd-vendor-token'

export function vendorToken(): string {
  try { return localStorage.getItem(TOKEN_KEY) || '' } catch { return '' }
}
export function setVendorToken(t: string) {
  try { t ? localStorage.setItem(TOKEN_KEY, t) : localStorage.removeItem(TOKEN_KEY) } catch { /* private mode */ }
}

async function send<T>(method: string, path: string, body?: BodyInit, json = false): Promise<T> {
  const headers: Record<string, string> = {}
  const t = vendorToken()
  if (t) headers.Authorization = 'Bearer ' + t
  if (json) headers['Content-Type'] = 'application/json'
  const res = await fetch('/api/vendor-portal' + path, { method, headers, body })
  const text = await res.text()
  let out: any = null
  try { out = text ? JSON.parse(text) : null } catch { out = text }
  if (!res.ok) {
    if (res.status === 401 && path !== '/login') {
      setVendorToken('')
      window.dispatchEvent(new Event('pd-vendor-signed-out'))
    }
    throw new ApiError(res.status, out?.error || `Request failed (${res.status})`, out?.errors)
  }
  return out as T
}

export const vget = <T = any>(path: string) => send<T>('GET', path)
export const vpost = <T = any>(path: string, body: unknown = {}) => send<T>('POST', path, JSON.stringify(body), true)

/** Sends a form with a JSON "data" field and one file. */
export function vupload<T = any>(path: string, data: unknown, field: string, file: File | null) {
  const fd = new FormData()
  fd.append('data', JSON.stringify(data))
  if (file) fd.append(field, file)
  return send<T>('POST', path, fd)
}

/** Opens a file the vendor uploaded. */
export const vfileUrl = (id: number) => `/api/vendor-portal/files/${id}?token=${encodeURIComponent(vendorToken())}`
