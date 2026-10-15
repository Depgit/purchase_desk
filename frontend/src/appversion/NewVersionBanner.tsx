// A tab left open keeps running the app it loaded. When the person comes back to it (window focus, at most once a
// minute), the page is fetched again; if it now loads a different build, a bar offers to reload.
import { useEffect, useState } from 'react'

const BUILD = /assets\/index-[\w-]+\.js/

function current(): string {
  const s = document.querySelector<HTMLScriptElement>('script[src*="/assets/index-"]')
  return s?.src.match(BUILD)?.[0] || '' // empty under the dev server, which reloads by itself
}

export function NewVersionBanner() {
  const [stale, setStale] = useState(false)
  useEffect(() => {
    const mine = current()
    if (!mine) return
    let last = 0
    const check = async () => {
      if (Date.now() - last < 60_000) return
      last = Date.now()
      try {
        const html = await (await fetch('/', { cache: 'no-store' })).text()
        const live = html.match(BUILD)?.[0]
        if (live && live !== mine) setStale(true)
      } catch { /* offline: try again later */ }
    }
    window.addEventListener('focus', check)
    const t = window.setInterval(check, 10 * 60_000)
    return () => { window.removeEventListener('focus', check); window.clearInterval(t) }
  }, [])
  if (!stale) return null
  return (
    <div className="newver" role="status">
      <span>A new version of Purchase Desk is ready.</span>
      <button className="btn small" onClick={() => window.location.reload()}>Reload</button>
    </div>
  )
}
