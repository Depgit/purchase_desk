// Talks to the Go API: adds the session token, turns error replies into ApiError, uploads files, downloads CSVs.
const TOKEN_KEY = 'pd-token'

export class ApiError extends Error {
  status: number
  errors: string[]
  constructor(status: number, message: string, errors?: string[]) {
    super(message)
    this.status = status
    this.errors = errors && errors.length ? errors : [message]
  }
}

export function getToken(): string {
  try { return localStorage.getItem(TOKEN_KEY) || '' } catch { return '' }
}
export function setToken(t: string) {
  try { t ? localStorage.setItem(TOKEN_KEY, t) : localStorage.removeItem(TOKEN_KEY) } catch { /* private mode */ }
}

async function handle<T>(res: Response): Promise<T> {
  const text = await res.text()
  let body: any = null
  try { body = text ? JSON.parse(text) : null } catch { body = text }
  if (!res.ok) {
    if (res.status === 401) {
      setToken('')
      window.dispatchEvent(new Event('pd-signed-out'))
    }
    throw new ApiError(res.status, body?.error || `Request failed (${res.status})`, body?.errors)
  }
  return body as T
}

export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  const t = getToken()
  if (t) headers.Authorization = 'Bearer ' + t
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const res = await fetch('/api' + path, { method, headers, body: body !== undefined ? JSON.stringify(body) : undefined })
  return handle<T>(res)
}

export const get = <T = any>(path: string) => api<T>('GET', path)
export const post = <T = any>(path: string, body: unknown = {}) => api<T>('POST', path, body)
export const put = <T = any>(path: string, body: unknown = {}) => api<T>('PUT', path, body)

/** Sends a form with a JSON "data" field and files. */
export async function upload<T = any>(path: string, data: unknown, files: Record<string, File | File[] | null | undefined>, method = 'POST'): Promise<T> {
  const fd = new FormData()
  fd.append('data', JSON.stringify(data))
  for (const [k, v] of Object.entries(files)) {
    if (!v) continue
    for (const f of Array.isArray(v) ? v : [v]) fd.append(k, f)
  }
  const headers: Record<string, string> = {}
  const t = getToken()
  if (t) headers.Authorization = 'Bearer ' + t
  const res = await fetch('/api' + path, { method, headers, body: fd })
  return handle<T>(res)
}

/** Opens an uploaded file (invoice, photo, bank proof) in a new tab. */
export function fileUrl(id: number) {
  return `/api/files/${id}?token=${encodeURIComponent(getToken())}`
}

/** Downloads a file the API generates, such as a CSV export. */
export async function download(path: string, fallbackName: string) {
  const res = await fetch('/api' + path, { headers: { Authorization: 'Bearer ' + getToken() } })
  if (!res.ok) {
    await handle(res)
    return
  }
  const cd = res.headers.get('Content-Disposition') || ''
  const name = /filename="([^"]+)"/.exec(cd)?.[1] || fallbackName
  saveBlob(await res.blob(), name)
}

export function saveBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
