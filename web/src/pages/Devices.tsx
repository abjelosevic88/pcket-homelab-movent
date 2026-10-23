// Devices: every phone, app or integration with its own key, and the guide
// to connect a new one (QR codes for OwnTracks and Overland, the Dawarich app
// unchanged, Traccar, GPSLogger, Home Assistant, your own app).
import { useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { agoTs, fmtStamp } from '../lib/geo'
import type { Device, Setup } from '../lib/types'
import { Icon } from '../components/icons'
import { Card, Confirm, CopyField, Empty, Field, Loading, Modal, PageHeader, Segmented, copyText, errMsg, useAsync, useToast } from '../components/ui'
import '../styles/manage.css'

const KIND_INFO: Record<string, { emoji: string; desc: string }> = {
  dawarich: { emoji: '🧭', desc: 'The official Dawarich app for iOS and Android works unchanged: server URL and key.' },
  owntracks: { emoji: '🦉', desc: 'iOS and Android, open source and battery-friendly. Scan a QR code to set it up.' },
  overland: { emoji: '🛰️', desc: 'iOS, precise background tracking with batched uploads. One tap to configure.' },
  gpslogger: { emoji: '📡', desc: 'Android (F-Droid), logs to a custom URL. Light and simple.' },
  traccar: { emoji: '📍', desc: 'Traccar Client or any OsmAnd-protocol tracker, phones and GPS boxes.' },
  homeassistant: { emoji: '🏠', desc: 'Send positions from Home Assistant device trackers with a REST command.' },
  custom: { emoji: '🧩', desc: 'Your own app or script: a simple JSON API, one point or thousands per request.' },
  integration: { emoji: '🔗', desc: 'Read access for Pocket Travel, Pocket Garage or Home Assistant’s Dawarich integration.' },
  other: { emoji: '📱', desc: 'Anything else that can send positions.' },
}
const KIND_ORDER = ['dawarich', 'owntracks', 'overland', 'gpslogger', 'traccar', 'homeassistant', 'custom', 'integration', 'other']
const SCOPES: { value: Device['scope']; label: string; title: string }[] = [
  { value: 'track', label: 'Send', title: 'Can send positions only' },
  { value: 'read', label: 'Read', title: 'Can read history only' },
  { value: 'full', label: 'Both', title: 'Can send and read' },
]
const scopeLabel = (s: string) => s === 'track' ? 'sends' : s === 'read' ? 'reads' : 'sends & reads'

export default function Devices() {
  const { toast } = useToast()
  const { reloadBoot } = useApp()
  const navigate = useNavigate()
  const loc = useLocation()
  const { data, loading, error, reload } = useAsync(() => api.get<{ devices: Device[]; kinds: Record<string, string> }>(`${V1}/devices`), [])
  const [wizard, setWizard] = useState(() => new URLSearchParams(loc.search).get('add') === '1')
  const [setup, setSetup] = useState<{ device: Device; setup: Setup } | null>(null)
  const [edit, setEdit] = useState<Device | null>(null)
  const [del, setDel] = useState<Device | null>(null)
  const [rotate, setRotate] = useState<Device | null>(null)
  const [flash, setFlash] = useState(0)

  useEffect(() => { if (new URLSearchParams(loc.search).get('add') === '1') setWizard(true) }, [loc.search])
  // #d12 scrolls to that device.
  useEffect(() => {
    const m = /^#d(\d+)$/.exec(loc.hash)
    if (!m || !data) return
    const id = Number(m[1])
    setFlash(id)
    setTimeout(() => document.getElementById(`d${id}`)?.scrollIntoView({ behavior: 'smooth', block: 'center' }), 50)
    const t = setTimeout(() => setFlash(0), 2200)
    return () => clearTimeout(t)
  }, [loc.hash, data])

  const kinds = data?.kinds || {}
  const run = async (fn: () => Promise<unknown>, msg: string) => {
    try { await fn(); await reload(); reloadBoot().catch(() => {}); toast(msg) } catch (e) { toast(errMsg(e), 'err') }
  }
  const showSetup = async (d: Device) => {
    try { setSetup(await api.get<{ device: Device; setup: Setup }>(`${V1}/devices/${d.id}/setup`)) } catch (e) { toast(errMsg(e), 'err') }
  }
  const closeWizard = () => { setWizard(false); if (loc.search.includes('add=1')) navigate('/devices', { replace: true }) }

  const active = (data?.devices || []).filter(d => !d.revoked)
  const revoked = (data?.devices || []).filter(d => d.revoked)
  return (
    <div className="mg-page">
      <PageHeader title="Devices" sub="Every phone, app and integration has its own key, so one can be revoked without re-pairing the others."
        actions={<button type="button" className="btn primary" onClick={() => setWizard(true)}><Icon name="plus" size={16} />Connect a phone or app</button>} />
      {loading && !data && <Loading />}
      {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
      {data && data.devices.length === 0 && (
        <Card><Empty title="Nothing connected yet" icon="smartphone">
          Install OwnTracks, Overland, the Dawarich app or another tracker on your phone, then connect it here. Already have history elsewhere? Import it under Import &amp; export.
          <div className="mt"><button type="button" className="btn primary" onClick={() => setWizard(true)}><Icon name="plus" size={15} />Connect a phone or app</button></div>
        </Empty></Card>
      )}
      {active.length > 0 && <div className="mg-grid">{active.map(d => (
        <DeviceCard key={d.id} d={d} kinds={kinds} flash={flash === d.id} onSetup={() => showSetup(d)} onEdit={() => setEdit(d)} onRotate={() => setRotate(d)} onDelete={() => setDel(d)}
          onRevoke={() => run(() => api.put(`${V1}/devices/${d.id}`, { revoked: true }), `${d.name} can no longer connect`)} />
      ))}</div>}
      {revoked.length > 0 && <>
        <div className="mg-sec-h"><Icon name="lock" size={14} />Revoked</div>
        <div className="mg-grid">{revoked.map(d => (
          <DeviceCard key={d.id} d={d} kinds={kinds} flash={flash === d.id} onSetup={() => showSetup(d)} onEdit={() => setEdit(d)} onRotate={() => setRotate(d)} onDelete={() => setDel(d)}
            onRestore={() => run(() => api.put(`${V1}/devices/${d.id}`, { revoked: false }), `${d.name} is allowed again`)} />
        ))}</div>
      </>}
      <MobileSettings />
      <ApiGlance />

      {wizard && <Wizard kinds={kinds} onClose={closeWizard} onCreated={r => { closeWizard(); setSetup(r); reload(); reloadBoot().catch(() => {}) }} />}
      {setup && <SetupModal device={setup.device} setup={setup.setup} onClose={() => setSetup(null)} />}
      {edit && <EditModal d={edit} kinds={kinds} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload(); reloadBoot().catch(() => {}) }} />}
      {del && <Confirm title={`Delete ${del.name}?`} confirmLabel="Delete" message="Its key stops working at once. The positions it sent stay in your history." onCancel={() => setDel(null)}
        onConfirm={() => { const d = del; setDel(null); run(() => api.del(`${V1}/devices/${d.id}`), 'Device deleted') }} />}
      {rotate && <Confirm title={`New key for ${rotate.name}?`} confirmLabel="Make a new key" message="The old key stops working immediately: update the app with the new key (the setup is shown next)." onCancel={() => setRotate(null)}
        onConfirm={async () => {
          const d = rotate; setRotate(null)
          try { const r = await api.post<{ device: Device; setup: Setup }>(`${V1}/devices/${d.id}/rotate`); setSetup(r); reload(); toast('New key made') } catch (e) { toast(errMsg(e), 'err') }
        }} />}
    </div>
  )
}

function DeviceCard({ d, kinds, flash, onSetup, onEdit, onRotate, onDelete, onRevoke, onRestore }: {
  d: Device; kinds: Record<string, string>; flash: boolean; onSetup: () => void; onEdit: () => void; onRotate: () => void; onDelete: () => void; onRevoke?: () => void; onRestore?: () => void
}) {
  const age = d.last_point_at ? Date.now() / 1000 - d.last_point_at : Infinity
  const state = d.scope === 'read' ? '' : age < 3600 ? 'ok' : age < 12 * 3600 ? 'mid' : 'off'
  const info = KIND_INFO[d.kind] || KIND_INFO.other
  return (
    <div id={`d${d.id}`} className={`dev-card ${d.revoked ? 'revoked' : ''} ${flash ? 'flash' : ''}`}>
      <div className="dev-head">
        <span className="dev-ico" aria-hidden="true">{info.emoji}</span>
        <div className="grow" style={{ minWidth: 0 }}>
          <div className="dev-name ellipsis">{d.name}</div>
          <div className="dev-kind ellipsis">{kinds[d.kind] || d.kind}</div>
        </div>
        {d.revoked ? <span className="badge danger">revoked</span> : <span className={`badge ${d.scope === 'read' ? '' : 'accent'}`} title={SCOPES.find(s => s.value === d.scope)?.title}>{scopeLabel(d.scope)}</span>}
      </div>
      <dl className="dev-facts">
        {d.scope !== 'read' && <div><dt>Last position</dt><dd className="row" style={{ gap: 6 }}>{state && <span className={`dev-dot ${state}`} />}{d.last_point_at ? <span title={fmtStamp(d.last_point_at)}>{agoTs(d.last_point_at)}</span> : 'none yet'}</dd></div>}
        {d.scope !== 'read' && <div><dt>Battery</dt><dd>{d.last_battery != null ? `${d.last_battery}%${d.last_battery_state && d.last_battery_state !== 'unknown' ? ` · ${d.last_battery_state}` : ''}` : '—'}</dd></div>}
        {d.scope !== 'read' && <div><dt>Positions sent</dt><dd className="tnum">{d.points.toLocaleString()}</dd></div>}
        <div><dt>Last seen</dt><dd title={d.last_seen_at ? fmtStamp(d.last_seen_at) : ''}>{agoTs(d.last_seen_at)}</dd></div>
        {d.last_ip && <div><dt>From</dt><dd className="mono" title={d.last_ip}>{d.last_ip}</dd></div>}
        {d.tracker_id && <div><dt>Tracker id</dt><dd title={d.tracker_id}>{d.tracker_id}</dd></div>}
        {d.user_agent && <div style={{ gridColumn: '1 / -1' }}><dt>App</dt><dd title={d.user_agent}>{d.user_agent}</dd></div>}
      </dl>
      <div className="dev-actions">
        <button type="button" className="btn sm" onClick={onSetup}><Icon name="qr" size={14} />Setup &amp; key</button>
        <button type="button" className="btn ghost sm" onClick={onEdit}><Icon name="edit" size={14} />Edit</button>
        <span className="grow" />
        {onRevoke && <button type="button" className="btn ghost sm" onClick={onRevoke} title="Stop this key working, keep the device">Revoke</button>}
        {onRestore && <button type="button" className="btn ghost sm" onClick={onRestore}>Allow again</button>}
        <button type="button" className="btn ghost sm" onClick={onRotate} title="Make a new key">New key</button>
        <button type="button" className="btn ghost sm danger-text" onClick={onDelete} aria-label={`Delete ${d.name}`}><Icon name="trash" size={14} /></button>
      </div>
    </div>
  )
}

function Wizard({ kinds, onClose, onCreated }: { kinds: Record<string, string>; onClose: () => void; onCreated: (r: { device: Device; setup: Setup }) => void }) {
  const { toast } = useToast()
  const [kind, setKind] = useState('')
  const [name, setName] = useState('')
  const [scope, setScope] = useState<Device['scope'] | ''>('')
  const [busy, setBusy] = useState(false)
  const order = useMemo(() => [...KIND_ORDER.filter(k => kinds[k] || KIND_INFO[k]), ...Object.keys(kinds).filter(k => !KIND_ORDER.includes(k))], [kinds])
  const defScope = (k: string): Device['scope'] => k === 'integration' ? 'read' : ['owntracks', 'overland', 'gpslogger', 'traccar', 'homeassistant'].includes(k) ? 'track' : 'full'
  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!kind) return
    setBusy(true)
    try { onCreated(await api.post<{ device: Device; setup: Setup }>(`${V1}/devices`, { name: name.trim(), kind, scope: scope || defScope(kind) })) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title="Connect a phone or app" onClose={onClose} size="lg">
      <form onSubmit={create} className="stack loose">
        <div className="kind-grid" role="radiogroup" aria-label="What do you want to connect?">
          {order.map(k => (
            <button key={k} type="button" role="radio" aria-checked={kind === k} className={`kind-opt ${kind === k ? 'active' : ''}`}
              onClick={() => { setKind(k); setScope(''); if (!name || Object.values(kinds).includes(name)) setName(kinds[k] || '') }}>
              <span className="kind-emoji" aria-hidden="true">{(KIND_INFO[k] || KIND_INFO.other).emoji}</span>
              <span><b>{kinds[k] || k}</b><span className="d">{(KIND_INFO[k] || KIND_INFO.other).desc}</span></span>
            </button>
          ))}
        </div>
        {kind && <>
          <Field label="Name" help="So you recognise it later, e.g. “Aleksandar’s iPhone”."><input value={name} onChange={e => setName(e.target.value)} autoFocus maxLength={80} /></Field>
          <Field group label="This key may" help="Trackers only need to send; integrations only need to read. Least access is safest.">
            <Segmented value={scope || defScope(kind)} onChange={v => setScope(v)} options={SCOPES.map(s => ({ value: s.value, label: s.label, title: s.title }))} />
          </Field>
        </>}
        <div className="form-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button className="btn primary" disabled={!kind || busy}>{busy ? 'Creating…' : 'Create key and show setup'}</button>
        </div>
      </form>
    </Modal>
  )
}

function SetupModal({ device, setup, onClose }: { device: Device; setup: Setup; onClose: () => void }) {
  return (
    <Modal title={`Set up ${device.name}`} onClose={onClose} size="lg" footer={<button type="button" className="btn primary" onClick={onClose}>Done</button>}>
      <SetupView device={device} setup={setup} />
    </Modal>
  )
}

export function SetupView({ device, setup }: { device: Device; setup: Setup }) {
  return (
    <div className="setup">
      <p className="mg-hint">{setup.title}{device.revoked ? ' — this key is revoked; allow it again to use it.' : ''}</p>
      {(setup.qr || setup.link) && (
        <div className="setup-qr">
          {setup.qr && <img src={`${V1}/devices/${device.id}/qr.svg?k=${encodeURIComponent((device.api_key || '').slice(-6))}`} alt="Setup QR code" />}
          <div className="stack" style={{ maxWidth: 320 }}>
            {setup.link ? <>
              <p className="mg-hint">Scan with the phone's camera, or open this page on the phone and tap:</p>
              <a className="btn primary" href={setup.link}><Icon name="smartphone" size={15} />Configure the app</a>
            </> : <p className="mg-hint">Scan to copy the key onto the phone instead of typing it.</p>}
          </div>
        </div>
      )}
      <div className="setup-fields">
        {setup.fields.map(f => (
          <div key={f.label}>
            <span className="label">{f.label}</span>
            {f.copy ? <CopyField value={f.value} label={f.label} /> : <div className="mg-muted-box mono">{f.value}</div>}
          </div>
        ))}
      </div>
      {setup.steps.length > 0 && <div><div className="mg-sec-h">Steps</div><ol>{setup.steps.map((s, i) => <li key={i}>{s}</li>)}</ol></div>}
      {setup.code && Object.entries(setup.code).map(([title, code]) => <CodeBlock key={title} title={title} code={code} />)}
      {setup.notes && setup.notes.length > 0 && <ul className="notes">{setup.notes.map((n, i) => <li key={i}>{n}</li>)}</ul>}
    </div>
  )
}

export function CodeBlock({ title, code }: { title?: string; code: string }) {
  const { toast } = useToast()
  return (
    <div className="mg-code">
      {title && <div className="mg-code-title">{title}</div>}
      <pre>{code}</pre>
      <button type="button" className="btn sm copy" onClick={async () => toast(await copyText(code) ? 'Copied' : 'Could not copy')}><Icon name="copy" size={13} />Copy</button>
    </div>
  )
}

function EditModal({ d, kinds, onClose, onSaved }: { d: Device; kinds: Record<string, string>; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [name, setName] = useState(d.name)
  const [scope, setScope] = useState<Device['scope']>(d.scope)
  const [kind, setKind] = useState(d.kind)
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { await api.put(`${V1}/devices/${d.id}`, { name, scope, kind }); toast('Saved'); onSaved() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title="Edit device" onClose={onClose} size="sm">
      <form onSubmit={save} className="stack">
        <Field label="Name"><input value={name} onChange={e => setName(e.target.value)} required autoFocus maxLength={80} /></Field>
        <Field label="Kind" help="Changes the setup guide shown for it.">
          <select value={kind} onChange={e => setKind(e.target.value)}>{Object.entries(kinds).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select>
        </Field>
        <Field group label="This key may"><Segmented value={scope} onChange={setScope} options={SCOPES.map(s => ({ value: s.value, label: s.label, title: s.title }))} /></Field>
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy}>Save</button></div>
      </form>
    </Modal>
  )
}

/** The tracking settings the Dawarich app keeps on the server, when there are any. */
function MobileSettings() {
  const { data } = useAsync(() => api.get<{ settings: Record<string, unknown>; updated_at: string | null }>(`${V1}/settings/mobile`).catch(() => null), [])
  const entries = Object.entries(data?.settings || {})
  if (!entries.length) return null
  const label = (k: string) => k.replace(/_/g, ' ').replace(/^./, c => c.toUpperCase())
  return (
    <Card title="Dawarich app tracking settings" sub={`What the app synced here${data?.updated_at ? ` · ${new Date(data.updated_at).toLocaleString()}` : ''}. Change them in the app.`}>
      <dl className="dev-facts">{entries.map(([k, v]) => <div key={k}><dt>{label(k)}</dt><dd>{typeof v === 'boolean' ? (v ? 'on' : 'off') : String(v)}</dd></div>)}</dl>
    </Card>
  )
}

function ApiGlance() {
  const { boot } = useApp()
  const base = boot?.base_url || window.location.origin
  const send: [string, string, string][] = [
    ['POST', '/api/v1/locations', 'the simple API: one object, an array or {"locations": […]} of {lat, lon, timestamp, accuracy, speed, battery…} or GeoJSON points'],
    ['POST', '/api/v1/overland/batches', 'Overland batches ({"locations": [GeoJSON Feature…]})'],
    ['POST', '/api/v1/owntracks/points', 'OwnTracks HTTP mode; answers with the friends who share with you'],
    ['POST', '/api/v1/points', 'the Dawarich apps (same GeoJSON batch)'],
    ['GET', '/osmand', 'OsmAnd protocol: Traccar Client and GPS trackers (?id=<key>&lat=&lon=&timestamp=)'],
    ['GET', '/gpslogger', 'GPSLogger custom URL (?api_key=&lat=&lon=&time=…)'],
  ]
  const read: [string, string, string][] = [
    ['GET', '/api/v1/health', 'liveness, with X-Dawarich-Version'],
    ['GET', '/api/v1/users/me', 'the key’s account'],
    ['GET', '/api/v1/points', 'points (start_at, end_at, page, per_page, order, slim=true)'],
    ['GET', '/api/v1/timeline', 'days with visits and journeys (≤ 31 days)'],
    ['GET', '/api/v1/visits', 'visits; PUT /api/v1/visits/:id to confirm or rename'],
    ['GET', '/api/v1/stats', 'distance per year and month, countries, cities'],
    ['GET', '/api/v1/areas', 'saved places'],
  ]
  const curl = `curl -X POST "${base}/api/v1/locations" \\\n  -H "Authorization: Bearer <key>" \\\n  -H "Content-Type: application/json" \\\n  -d '{"lat": 43.8563, "lon": 18.4131, "timestamp": "${new Date().toISOString().slice(0, 19)}Z", "accuracy": 10}'`
  return (
    <Card title="Connect your own app" sub={<>Every key works as <code>Authorization: Bearer &lt;key&gt;</code>, <code>?api_key=&lt;key&gt;</code> or the password of HTTP basic auth. This server speaks the Dawarich API, so anything built for Dawarich works with the address <code>{base}</code>.</>}>
      <div className="stack loose">
        <div><div className="mg-sec-h">Send positions</div><ApiList rows={send} /></div>
        <div><div className="mg-sec-h">Read (Dawarich-compatible)</div><ApiList rows={read} /></div>
        <CodeBlock title="Try it" code={curl} />
        <p className="mg-hint">Re-sending the same point is harmless: duplicates are skipped. Batches of thousands of points per request are fine.</p>
      </div>
    </Card>
  )
}

function ApiList({ rows }: { rows: [string, string, string][] }) {
  return <ul className="api-list">{rows.map(([m, p, w]) => <li key={m + p}><span className={`meth ${m === 'GET' ? 'get' : ''}`}>{m}</span><code>{p}</code><span className="what">— {w}</span></li>)}</ul>
}
