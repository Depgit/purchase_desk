// The property counts what arrived from the warehouse. Anything short of what was dispatched is booked as pilferage.
import { useState } from 'react'
import { upload } from '../api/client'
import type { Doc } from '../api/types'
import { qty } from '../format/format'
import { Errors } from '../ui/Errors'
import { useAction } from '../ui/useAction'

export function ReceiveTransferPanel({ doc, onDone }: { doc: Doc; onDone: () => void }) {
  const [q, setQ] = useState<Record<number, string>>(Object.fromEntries(doc.lines.map((l) => [l.id, String(l.dispatched_qty ?? 0)])))
  const [photos, setPhotos] = useState<File[]>([])
  const a = useAction()
  return (
    <section className="card act">
      <h2>Receive goods from the warehouse</h2>
      <p className="hint">Count what arrived. Anything short of what the warehouse dispatched is booked as pilferage. There is no invoice for a warehouse transfer.</p>
      <div className="scroll"><table><thead><tr><th>Item</th><th className="r">Dispatched</th><th className="r">Received</th></tr></thead>
        <tbody>{doc.lines.filter((l) => (l.dispatched_qty ?? 0) > 0).map((l) => <tr key={l.id}><td>{l.item_name}<br /><span className="hint">{l.sku_code} · {l.pack_size}</span></td>
          <td className="r">{qty(l.dispatched_qty ?? 0)}</td>
          <td className="r"><input className="qty" type="number" min="0" max={l.dispatched_qty ?? 0} value={q[l.id]} onChange={(e) => setQ({ ...q, [l.id]: e.target.value })} aria-label={'Received ' + l.item_name} /></td></tr>)}</tbody></table></div>
      <label className="field"><span>Photos of items</span><input type="file" accept="image/*" capture="environment" multiple onChange={(e) => setPhotos(Array.from(e.target.files || []))} /></label>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn" disabled={a.busy} onClick={() => a.run(() => upload(`/documents/${doc.id}/receive-transfer`, { quantities: Object.fromEntries(Object.entries(q).map(([k, v]) => [k, +v || 0])) }, { photos }),
        (r: any) => { onDone(); return r.message })}>Save receipt</button></div>
    </section>
  )
}
