// One Excel per vendor covering every property it serves. Only approved quantities and rates are shared;
// requests still in approval appear as a status. Send by email or WhatsApp, by hand or automatically each day.
import { useState } from 'react'
import { download, post, put } from '../api/client'
import { useApi } from '../api/useApi'
import { useSession } from '../auth/session'
import { dateTime, inr } from '../format/format'
import { DataTable } from '../ui/DataTable'
import { Errors } from '../ui/Errors'
import { Field } from '../ui/Field'
import { Modal } from '../ui/Modal'
import { Pill } from '../ui/Pill'
import { useAction } from '../ui/useAction'
import { useScreen } from '../screenaccess/screen'

interface Row { vendor_id: number; code: string; vendor: string; confirmed_pos: number; properties_confirmed: number; confirmed_value: number; approved_without_po: number; in_approval: number;
  sheet_mode: string; sheet_time: string; sheet_channel: string; last_sent_at: string | null; last_version: number; new_pos: number }
interface Tab { Name: string; Heads: string[]; Rows: unknown[][] }
interface Preview { sheet: { vendor: string; version: number; file_name: string; tabs: Tab[]; confirmed_pos: number; approved_without_po: number; in_approval: number }
  email: string; mobile: string; email_body: string; whatsapp_body: string; sending: { mode: string; time: string; channel: string }; email_ready: boolean; whatsapp_ready: boolean }

export function VendorSheetsPage() {
  const { hasStep } = useSession()
  const sc = useScreen('vendor_sheets')
  const cc = sc.edit && hasStep('cc', 'l2', 'preop', 'purchase') // whoever approves requests looks after the sheets
  const { data, reload } = useApi<Row[]>('/vendor-sheets')
  const [open, setOpen] = useState<number | null>(null)
  const [sending, setSending] = useState<number[] | null>(null)
  const [sel, setSel] = useState<Set<string | number>>(new Set())
  const sendTxt = (r: { sheet_mode: string; sheet_time: string; sheet_channel: string }) => r.sheet_mode === 'auto' ? `Automatic · daily at ${r.sheet_time} · ${r.sheet_channel}` : 'Manual'
  if (open != null) return <SheetView id={open} onBack={() => { setOpen(null); reload() }} canSet={cc} />
  return (
    <>
      <div className="head"><div><h1>Vendor Order Sheets</h1><p className="sub">One Excel per vendor covering every property it serves, so it can plan supply. Quantities and rates only for approved requests; anything in approval shows as a status.</p></div>
        <div className="acts">{cc && <button className="btn" disabled={!sel.size} onClick={() => setSending([...sel].map(Number))}>Set sending for {sel.size || ''} vendors</button>}</div></div>
      <DataTable screen="vendor-sheets" rows={data || []} rowKey={(r) => r.vendor_id} onOpen={(r) => setOpen(r.vendor_id)} selectable={cc ? () => true : undefined} selected={sel} onSelect={setSel}
        search={(r) => r.vendor + ' ' + r.code} empty="No vendor has an open order."
        columns={[
          { key: 'vendor', label: 'Vendor', render: (r) => r.vendor, csv: (r) => r.vendor },
          { key: 'props', label: 'Properties confirmed', right: true, render: (r) => r.properties_confirmed, csv: (r) => r.properties_confirmed },
          { key: 'pos', label: 'Confirmed POs', right: true, render: (r) => r.confirmed_pos, csv: (r) => r.confirmed_pos },
          { key: 'val', label: 'Confirmed value', right: true, render: (r) => inr(r.confirmed_value), sort: (r) => r.confirmed_value, csv: (r) => r.confirmed_value },
          { key: 'appr', label: 'Approved, PO to follow', right: true, render: (r) => r.approved_without_po, csv: (r) => r.approved_without_po },
          { key: 'wait', label: 'In approval, not shared', right: true, render: (r) => r.in_approval, csv: (r) => r.in_approval },
          { key: 'send', label: 'Sending', render: (r) => <>{sendTxt(r)}{cc && <> <button className="link" onClick={() => setSending([r.vendor_id])}>Change</button></>}</>, csv: sendTxt },
          { key: 'last', label: 'Last sent', render: (r) => r.last_sent_at ? `v${r.last_version} · ${dateTime(r.last_sent_at)}` : <Pill tone="warn">Not sent yet</Pill>, csv: (r) => r.last_sent_at || '' },
          { key: 'new', label: 'Unsent POs', render: (r) => r.new_pos ? <Pill tone="warn">{r.new_pos} new</Pill> : <Pill tone="ok">Up to date</Pill>, csv: (r) => r.new_pos },
          { key: 'dl', label: '', render: (r) => <button className="btn ghost small" onClick={() => download(`/vendor-sheets/${r.vendor_id}/xlsx`, 'order-sheet.xlsx')}>Download</button> },
        ]} csvName="vendor-order-sheets" />
      {sending && <SendingModal ids={sending} onClose={() => setSending(null)} onDone={() => { setSending(null); setSel(new Set()); reload() }} />}
    </>
  )
}

function SheetView({ id, onBack, canSet }: { id: number; onBack: () => void; canSet: boolean }) {
  const { data, reload } = useApi<Preview>(`/vendor-sheets/${id}`)
  const [tab, setTab] = useState(0)
  const [send, setSend] = useState<'email' | 'whatsapp' | null>(null)
  const [sending, setSending] = useState(false)
  if (!data) return <p className="sub">Loading…</p>
  const sh = data.sheet
  const t = sh.tabs[tab]
  const money = (h: string) => /value|Rate|Amount/.test(h)
  return (
    <>
      <div className="head"><div><button className="link" onClick={onBack}>‹ Vendor Order Sheets</button><h1>{sh.vendor}</h1>
        <p className="sub">Order sheet version {sh.version} · {sh.confirmed_pos} confirmed POs · {sh.approved_without_po} approved with PO to follow · {sh.in_approval} still in approval, shown as status only</p></div>
        <div className="acts"><button className="btn ghost" onClick={() => download(`/vendor-sheets/${id}/xlsx`, sh.file_name)}>Download Excel</button>
          <button className="btn ghost" onClick={() => setSend('email')}>Send by email</button><button className="btn" onClick={() => setSend('whatsapp')}>Send on WhatsApp</button></div></div>
      <section className="card"><div className="head"><div><h2>Sending: {data.sending.mode === 'auto' ? `Automatic, daily at ${data.sending.time} by ${data.sending.channel}` : 'Manual'}</h2>
        <p className="sub">{data.sending.mode === 'auto' ? 'The sheet goes out at that time only on days when a new PO has been issued since the last sheet.' : 'Sheets go out only when someone clicks send.'}</p></div>
        {canSet && <button className="btn ghost" onClick={() => setSending(true)}>Change sending</button>}</div></section>
      <div className="tabs">{sh.tabs.map((x, i) => <button key={i} className={i === tab ? 'on' : ''} onClick={() => setTab(i)}>{x.Name}</button>)}</div>
      <section className="card flush">{t && t.Rows.length ? <div className="scroll"><table><thead><tr>{t.Heads.map((h) => <th key={h}>{h}</th>)}</tr></thead>
        <tbody>{t.Rows.map((r, i) => <tr key={i}>{r.map((v, j) => <td key={j} className={typeof v === 'number' ? 'r' : ''}>{typeof v === 'number' && money(t.Heads[j] || '') ? inr(v) : v === 'Yes' ? <Pill tone="ok">Yes</Pill> : v === 'New since last sheet' ? <Pill tone="warn">{v as string}</Pill> : String(v ?? '')}</td>)}</tr>)}</tbody></table></div>
        : <p className="empty">Nothing on this sheet yet.</p>}</section>
      <p className="hint">Vendor contact: {data.email || 'no email'} · {data.mobile || 'no mobile'}. {!data.email_ready && 'Email is not configured on the server yet, so sends are logged only. '}{!data.whatsapp_ready && 'WhatsApp (MSG91) is not configured yet.'}</p>
      {send && <SendModal id={id} channel={send} data={data} onClose={() => setSend(null)} onDone={() => { setSend(null); reload() }} />}
      {sending && <SendingModal ids={[id]} current={data.sending} onClose={() => setSending(false)} onDone={() => { setSending(false); reload() }} />}
    </>
  )
}

function SendModal({ id, channel, data, onClose, onDone }: { id: number; channel: 'email' | 'whatsapp'; data: Preview; onClose: () => void; onDone: () => void }) {
  const a = useAction()
  return (
    <Modal title={`Send order sheet ${channel === 'email' ? 'by email' : 'on WhatsApp'}`} onClose={onClose} wide>
      <dl className="kv" style={{ gridTemplateColumns: 'auto 1fr' }}><dt>To</dt><dd style={{ textAlign: 'left' }}>{channel === 'email' ? data.email : data.mobile}</dd>
        <dt>Attachment</dt><dd style={{ textAlign: 'left' }}>{channel === 'email' ? data.sheet.file_name : 'Summary message (download the Excel to share it)'}</dd></dl>
      <textarea className="mono" readOnly style={{ whiteSpace: 'pre-wrap' }} value={channel === 'email' ? data.email_body : data.whatsapp_body} />
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(() => post(`/vendor-sheets/${id}/send`, { channel }), (r: any) => { onDone(); return r.message })}>Send</button></div>
    </Modal>
  )
}

function SendingModal({ ids, current, onClose, onDone }: { ids: number[]; current?: { mode: string; time: string; channel: string }; onClose: () => void; onDone: () => void }) {
  const [f, setF] = useState({ mode: current?.mode || 'manual', time: current?.time || '18:00', channel: current?.channel || 'email' })
  const a = useAction()
  return (
    <Modal title={ids.length > 1 ? `Order sheet sending for ${ids.length} vendors` : 'Order sheet sending'} onClose={onClose}>
      <p className="sub">Automatic sends the order sheet once a day at the time you set, only on days when a new PO was issued since the last sheet. Manual sends nothing until someone clicks send.</p>
      <div className="grid3">
        <Field label="Sending"><select value={f.mode} onChange={(e) => setF({ ...f, mode: e.target.value })}><option value="manual">Manual</option><option value="auto">Automatic</option></select></Field>
        {f.mode === 'auto' && <><Field label="Time each day"><input type="time" value={f.time} onChange={(e) => setF({ ...f, time: e.target.value })} /></Field>
          <Field label="Send by"><select value={f.channel} onChange={(e) => setF({ ...f, channel: e.target.value })}><option value="email">Email</option><option value="whatsapp">WhatsApp</option><option value="both">Email and WhatsApp</option></select></Field></>}
      </div>
      <Errors list={a.errors} />
      <div className="btns"><button className="btn ghost" onClick={onClose}>Cancel</button>
        <button className="btn" disabled={a.busy} onClick={() => a.run(async () => { for (const id of ids) await put(`/vendors/${id}/sending`, f); return { message: 'Sending saved' } }, (r: any) => { onDone(); return r.message })}>Save</button></div>
    </Modal>
  )
}
