// Shows exactly what will be sent to Zoho for one invoice, and anything that holds it back.
import { useApi } from '../api/useApi'
import { Modal } from '../ui/Modal'

export function ZohoPreview({ invoiceId, onClose }: { invoiceId: number; onClose: () => void }) {
  const { data } = useApi<{ payload: unknown; problems: string[]; warnings: string[]; unique_key: string; calls: string[] }>(`/invoices/${invoiceId}/zoho-preview`)
  return (
    <Modal title="What is sent to Zoho" wide onClose={onClose}>
      {!data ? <p className="sub">Loading…</p> : <>
        {data.problems.length ? <p className="note bad"><b>Blocked:</b> {data.problems.join('; ')}.</p> : <p className="note ok">Ready to push.</p>}
        {data.warnings.length > 0 && <p className="note warn"><b>Check:</b> {data.warnings.join('; ')}.</p>}
        <p className="sub">Unique key on the Zoho bill: <b>{data.unique_key}</b>. Calls, in order: {data.calls.join(' → ')}.</p>
        <textarea className="mono" readOnly rows={18} value={JSON.stringify(data.payload, null, 1)} />
      </>}
      <div className="btns"><button className="btn ghost" onClick={onClose}>Close</button></div>
    </Modal>
  )
}
