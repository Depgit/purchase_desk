// A dialog over the page. Escape or the backdrop closes it. It is drawn on the page body, not where it is used:
// inside a card (whose backdrop blur contains fixed elements) it would otherwise be clipped to that card.
import { useEffect, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

export function Modal({ title, onClose, children, wide }: { title: ReactNode; onClose: () => void; children: ReactNode; wide?: boolean }) {
  useEffect(() => {
    const k = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', k)
    return () => window.removeEventListener('keydown', k)
  }, [onClose])
  return createPortal(
    <div className="overlay" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className={'modal' + (wide ? ' wide' : '')} role="dialog" aria-modal="true">
        <h2>{title}</h2>
        {children}
      </div>
    </div>,
    document.body,
  )
}
