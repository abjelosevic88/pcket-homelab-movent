// Import & export: files from Google Takeout, GPX, KML, GeoJSON, OwnTracks,
// CSV (or a ZIP of them), a link to (or one-off pull from) a Dawarich server, exports of any
// range, and the dangerous bits (delete a range, rebuild everything).
import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { agoTs, fmtDateTs, fmtStamp, todayIn, addDaysISO } from '../lib/geo'
import type { DawarichLink, Import } from '../lib/types'
import { Icon } from '../components/icons'
import { Card, Confirm, Field, Loading, Modal, NeedsCore, PageHeader, Segmented, Switch, errMsg, useAsync, useToast } from '../components/ui'
import '../styles/manage.css'

const FORMATS: [string, string][] = [
  ['Google Takeout', 'Records.json, Semantic Location History (monthly files) or the whole Takeout ZIP'],
  ['Google Timeline (phone)', 'Timeline.json / location-history.json exported on Android or iOS'],
  ['GPX', 'tracks, routes and way points with times'],
  ['KML', 'gx:Track and timestamped placemarks'],
  ['GeoJSON', 'points with a time, line strings with coordTimes; Dawarich exports'],
  ['OwnTracks .rec', 'the OwnTracks Recorder’s monthly files'],
  ['CSV', 'with latitude, longitude and time columns'],
  ['ZIP', 'any mix of the above'],
]

function fmtBytes(n: number): string {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB']
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`
}

export default function Data() {
  const loc = useLocation()
  const { data, reload } = useAsync(() => api.get<{ imports: Import[]; formats: Record<string, string> }>(`${V1}/imports`), [])
  const busy = data?.imports.some(i => i.status === 'queued' || i.status === 'running')
  useEffect(() => {
    if (!busy) return
    const t = setInterval(() => { reload() }, 2000)
    return () => clearInterval(t)
  }, [busy, reload])
  useEffect(() => {
    if (loc.hash === '#export') setTimeout(() => document.getElementById('export')?.scrollIntoView({ behavior: 'smooth' }), 80)
  }, [loc.hash])
  return (
    <div className="mg-page">
      <PageHeader title="Import & export" sub="Bring your history in from Google, Dawarich or any GPS file, and take it out again whenever you like." />
      <div className="mg-two">
        <Upload onDone={reload} />
        <DawarichCard copied={!!data?.imports.some(i => i.format === 'dawarich' && i.status === 'done')} onDone={reload} />
      </div>
      <Imports list={data?.imports} formats={data?.formats || {}} reload={reload} />
      <Export />
      <Danger />
    </div>
  )
}

function Upload({ onDone }: { onDone: () => void }) {
  const { toast } = useToast()
  const input = useRef<HTMLInputElement>(null)
  const [over, setOver] = useState(false)
  const [progress, setProgress] = useState<{ name: string; pct: number } | null>(null)
  const send = (files: FileList | null) => {
    if (!files?.length) return
    const queue = Array.from(files)
    const next = () => {
      const f = queue.shift()
      if (!f) { setProgress(null); onDone(); return }
      setProgress({ name: f.name, pct: 0 })
      const fd = new FormData()
      fd.append('file', f)
      const xhr = new XMLHttpRequest()
      xhr.open('POST', `${V1}/imports`)
      xhr.withCredentials = true
      xhr.upload.onprogress = e => { if (e.lengthComputable) setProgress({ name: f.name, pct: e.loaded / e.total }) }
      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) toast(`${f.name} is queued`)
        else { let m = xhr.statusText; try { m = JSON.parse(xhr.responseText).error || m } catch { /* plain text */ } toast(`${f.name}: ${m}`, 'err') }
        onDone(); next()
      }
      xhr.onerror = () => { toast(`${f.name}: upload failed`, 'err'); next() }
      xhr.send(fd)
    }
    next()
  }
  return (
    <Card title="Import a file" sub="Big files are fine: a multi-gigabyte Takeout streams straight to disk and is read piece by piece.">
      <div className="stack">
        <div className={`drop ${over ? 'over' : ''}`} role="button" tabIndex={0} onClick={() => input.current?.click()} onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') input.current?.click() }}
          onDragOver={e => { e.preventDefault(); setOver(true) }} onDragLeave={() => setOver(false)} onDrop={e => { e.preventDefault(); setOver(false); send(e.dataTransfer.files) }}>
          <span className="drop-ico"><Icon name="upload" size={22} /></span>
          <b>Drop files here or choose</b>
          <span className="muted small">JSON, GPX, KML, GeoJSON, REC, CSV or ZIP</span>
          <input ref={input} type="file" multiple hidden accept=".json,.gpx,.kml,.kmz,.geojson,.rec,.csv,.zip,application/json,application/zip" onChange={e => { send(e.target.files); e.target.value = '' }} />
        </div>
        {progress && <div className="stack" style={{ gap: 4 }}>
          <div className="row small"><span className="grow ellipsis">Uploading {progress.name}</span><span className="tnum">{Math.round(progress.pct * 100)}%</span></div>
          <div className="bar"><span style={{ width: `${progress.pct * 100}%` }} /></div>
        </div>}
        <div className="formats">{FORMATS.map(([t, d]) => <div key={t}><b>{t}</b><span>{d}</span></div>)}</div>
        <details className="howto">
          <summary>How do I get my Google Timeline?</summary>
          <ol>
            <li><b>Android:</b> Settings → Location → Location services → Timeline → <i>Export Timeline data</i>. Save the <code>Timeline.json</code> and upload it here.</li>
            <li><b>iPhone:</b> Google Maps → your profile → Your Timeline → ⋯ → Location and privacy settings → <i>Export Timeline data</i>.</li>
            <li><b>Older history</b> (before Google moved Timeline to the phone): <a href="https://takeout.google.com" target="_blank" rel="noreferrer">takeout.google.com</a> → Location History → download the ZIP and upload it as it is.</li>
            <li>Places Google knew as Home and Work become your saved places if you have none yet, and its place visits arrive as confirmed visits.</li>
          </ol>
        </details>
      </div>
    </Card>
  )
}

interface LinkRes { link: DawarichLink | null; every_minutes: number; core?: { configured: boolean; available?: boolean; url?: string; error?: string } }

// Dawarich: link it (new points keep coming over every few minutes) and/or
// copy everything once.
function DawarichCard({ copied, onDone }: { copied: boolean; onDone: () => void }) {
  const { toast } = useToast()
  const { boot } = useApp()
  const { data, reload } = useAsync(() => api.get<LinkRes>(`${V1}/dawarich-link`), [])
  const [f, setF] = useState({ url: '', api_key: '' })
  const [via, setVia] = useState<'core' | 'direct' | null>(null)
  const [keep, setKeep] = useState(true)
  const [copy, setCopy] = useState(!copied)
  const [busy, setBusy] = useState(false)
  const [unlink, setUnlink] = useState(false)
  useEffect(() => { setCopy(!copied) }, [copied])
  const run = async (fn: () => Promise<unknown>, ok: string) => {
    setBusy(true)
    try { await fn(); toast(ok); reload(); onDone(); return true } catch (e) { toast(errMsg(e), 'err'); return false } finally { setBusy(false) }
  }
  const core = data?.core
  // Core counts once it is set up and this server is registered with it.
  const coreOn = !!core?.configured && boot?.core?.registered !== false
  // Through Core when this server has Core, unless the person picked otherwise.
  const mode = coreOn ? (via ?? 'core') : 'direct'
  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const body = mode === 'core' ? { source: 'core' } : { ...f, source: 'direct' }
    if (!keep) return run(() => api.post(`${V1}/imports/dawarich`, body), 'Copying from Dawarich — it runs in the background').then(ok => { if (ok) setF({ url: f.url, api_key: '' }) })
    return run(() => api.put(`${V1}/dawarich-link`, { ...body, copy_history: copy }), copy ? 'Linked — copying history in the background' : 'Linked to Dawarich')
  }
  const syncNow = () => run(async () => { await api.post(`${V1}/dawarich-link/sync`); setTimeout(reload, 4000) }, 'Pulling from Dawarich')
  if (!data) return <Card title="Dawarich"><Loading /></Card>
  const l = data.link
  if (l) {
    let host = l.url
    try { host = new URL(l.url).host } catch { /* keep as is */ }
    return (
      <Card title="Linked to Dawarich" sub={`New points on ${host} come over every ${data.every_minutes} minutes, so your phone can keep reporting to Dawarich. Dawarich itself is not changed.`}
        actions={<Switch checked={l.enabled} label="Keep pulling" disabled={busy} onChange={v => run(() => api.patch(`${V1}/dawarich-link`, { enabled: v }), v ? 'Link resumed' : 'Link paused')} />}>
        <div className="stack">
          <div className="imp-meta">
            <span><Icon name="link" size={13} /> {l.account || host}</span>
            <span className="muted small">{l.source === 'core' ? 'through Pocket Core' : 'entered by hand'}</span>
            <span className={`badge ${!l.enabled ? '' : l.last_error ? 'danger' : 'ok'}`}>{!l.enabled ? 'paused' : l.last_error ? 'failing' : 'linked'}</span>
          </div>
          <div className="imp-meta">
            <span>{l.last_sync_at ? <>last pulled <span title={fmtStamp(l.last_sync_at)}>{agoTs(l.last_sync_at)}</span> · {l.last_added.toLocaleString()} new</> : 'waiting for the first pull'}</span>
            {l.points_added > 0 && <span className="tnum">{l.points_added.toLocaleString()} points since {fmtDateTs(l.created_at)}</span>}
            {l.cursor_ts > 0 && <span>up to {fmtStamp(l.cursor_ts)}</span>}
          </div>
          {l.last_error && <div className="callout danger"><Icon name="alert" size={14} /><span>{l.last_error}</span></div>}
          {l.source === 'core' && !coreOn && <NeedsCore what="This link goes through Pocket Core, which this server is not connected to, so nothing is pulled. Unlink and enter Dawarich manually, or connect Core." />}
          <div className="row">
            <button type="button" className="btn" disabled={busy || !l.enabled} onClick={syncNow}><Icon name="refresh" size={15} />Pull now</button>
            <button type="button" className="btn ghost" disabled={busy} onClick={() => setUnlink(true)}><Icon name="x" size={15} />Unlink</button>
          </div>
          <p className="mg-hint">{l.source === 'core' ? 'The address and key come from Pocket Core on every pull, so a change there applies here too.' : 'To change the address or key, unlink and link again.'} Points already here are skipped, so nothing is doubled.</p>
        </div>
        {unlink && <Confirm title="Unlink Dawarich?" confirmLabel="Unlink" message="New points there stop coming over. Everything already copied stays here."
          onCancel={() => setUnlink(false)} onConfirm={() => { setUnlink(false); run(() => api.del(`${V1}/dawarich-link`), 'Unlinked') }} />}
      </Card>
    )
  }
  return (
    <Card title="Dawarich" sub="Link a Dawarich server: your history comes across through its API (areas become saved places, confirmed visits keep their names) and new points keep coming. Dawarich itself is not changed.">
      <form onSubmit={submit} className="stack">
        <Field group label="Connect">
          <Segmented value={mode} onChange={setVia} options={[{ value: 'core', label: 'Through Pocket Core', disabled: !coreOn }, { value: 'direct', label: 'Enter manually' }]} />
          {!coreOn && <NeedsCore />}
        </Field>
        {mode === 'core' ? (
          core?.available
            ? <div className="callout"><Icon name="link" size={14} /><span>Uses the Dawarich Pocket Core has for you: <b>{core.url}</b>. No key is kept here.</span></div>
            : <div className="callout danger"><Icon name="alert" size={14} /><span>{core?.error || 'Pocket Core has no Dawarich for you.'} Add Dawarich in Pocket Core (Services), or enter it manually.</span></div>
        ) : <>
          <Field label="Dawarich address" help="e.g. https://dawarich.example.com or http://dawarich:3000"><input type="url" value={f.url} onChange={e => setF({ ...f, url: e.target.value })} required placeholder="https://" /></Field>
          <Field label="API key" help="In Dawarich: Account → API key."><input value={f.api_key} onChange={e => setF({ ...f, api_key: e.target.value })} required autoComplete="off" spellCheck={false} /></Field>
        </>}
        <label className="row"><Switch checked={keep} onChange={setKeep} label="Keep in sync" size="sm" /><span className="grow">Keep in sync <span className="muted small">— pull new points every few minutes</span></span></label>
        {keep && <label className="row"><Switch checked={copy} onChange={setCopy} label="Copy all history first" size="sm" /><span className="grow">Copy all history first{copied && <span className="muted small"> — already copied once</span>}</span></label>}
        <div className="row"><button className="btn primary" disabled={busy || (mode === 'core' && !core?.available)}><Icon name={keep ? 'link' : 'download'} size={15} />{busy ? 'Checking…' : keep ? 'Link Dawarich' : 'Copy once'}</button></div>
        <p className="mg-hint">Points already here are skipped, so linking after a copy (or copying twice) never doubles anything.</p>
      </form>
    </Card>
  )
}

const STATUS: Record<string, [string, string]> = { queued: ['waiting', ''], running: ['importing', 'accent'], done: ['done', 'ok'], failed: ['failed', 'danger'] }

function Imports({ list, formats, reload }: { list: Import[] | undefined; formats: Record<string, string>; reload: () => void }) {
  const { toast } = useToast()
  const [del, setDel] = useState<Import | null>(null)
  const [withPoints, setWithPoints] = useState(false)
  if (!list) return <Loading />
  if (!list.length) return null
  return (
    <Card title="Imports">
      <div>
        {list.map(i => {
          const [label, cls] = STATUS[i.status] || [i.status, '']
          const pct = i.status === 'done' ? 1 : i.progress
          return (
            <div key={i.id} className="imp-row">
              <div className="imp-head">
                <Icon name={i.format === 'dawarich' ? 'refresh' : 'file'} size={16} />
                <span className="imp-name grow">{i.name}</span>
                <span className={`badge ${cls}`}>{label}</span>
                {i.status !== 'running' && <button type="button" className="btn ghost sm" aria-label={`Remove ${i.name}`} onClick={() => { setWithPoints(false); setDel(i) }}><Icon name="trash" size={14} /></button>}
              </div>
              {(i.status === 'running' || i.status === 'queued') && <div className="bar"><span style={{ width: `${Math.max(2, pct * 100)}%` }} /></div>}
              <div className="imp-meta">
                <span>{formats[i.format] || (i.format === 'dawarich' ? 'Dawarich server' : i.format)}</span>
                {i.size > 0 && <span>{fmtBytes(i.size)}</span>}
                {i.points_read > 0 && <span className="tnum">{i.points_read.toLocaleString()} read · {i.points_added.toLocaleString()} new</span>}
                {i.visits_added > 0 && <span className="tnum">{i.visits_added.toLocaleString()} visits</span>}
                {i.first_ts > 0 && <span>{fmtDateTs(i.first_ts)} – {fmtDateTs(i.last_ts)}</span>}
                <span>{fmtStamp(i.created_at)}</span>
              </div>
              {i.error && <div className="callout danger"><Icon name="alert" size={14} /><span>{i.error}</span></div>}
            </div>
          )
        })}
      </div>
      {del && <Modal title={`Remove “${del.name}”?`} onClose={() => setDel(null)} size="sm" footer={<>
        <button type="button" className="btn" onClick={() => setDel(null)}>Cancel</button>
        <button type="button" className="btn danger" onClick={async () => {
          const d = del; setDel(null)
          try { await api.del(`${V1}/imports/${d.id}${withPoints ? '?points=1' : ''}`); toast(withPoints ? 'Import and its points removed' : 'Import removed'); reload() } catch (e) { toast(errMsg(e), 'err') }
        }}>Remove</button>
      </>}>
        <div className="stack">
          <p>The entry leaves this list. The positions it brought stay in your history unless you remove them too.</p>
          <label className="row"><Switch checked={withPoints} onChange={setWithPoints} label="Also delete its points" /><span>Also delete its {del.points_added ? del.points_added.toLocaleString() + ' ' : ''}points and visits</span></label>
        </div>
      </Modal>}
    </Card>
  )
}

function Export() {
  const [all, setAll] = useState(false)
  const [from, setFrom] = useState(() => addDaysISO(todayIn(), -30))
  const [to, setTo] = useState(() => todayIn())
  const [format, setFormat] = useState<'geojson' | 'gpx' | 'csv'>('geojson')
  const [zones, setZones] = useState(false)
  const q = new URLSearchParams({ format })
  if (all) q.set('all', '1'); else { q.set('from', from); q.set('to', to) }
  if (zones) q.set('zones', '1')
  return (
    <div id="export" style={{ scrollMarginTop: 70 }}>
      <Card title="Export" sub="Your points as a file, for maps, watches, spreadsheets or another server. GeoJSON imports back here and into Dawarich.">
        <div className="stack">
          <Field group label="Range"><Segmented value={all ? 'all' : 'range'} onChange={v => setAll(v === 'all')} options={[{ value: 'range', label: 'Dates' }, { value: 'all', label: 'Everything' }]} /></Field>
          {!all && <div className="row wrap">
            <Field label="From"><input type="date" value={from} max={to} onChange={e => setFrom(e.target.value)} /></Field>
            <Field label="To"><input type="date" value={to} min={from} onChange={e => setTo(e.target.value)} /></Field>
          </div>}
          <Field group label="Format"><Segmented value={format} onChange={setFormat} options={[{ value: 'geojson', label: 'GeoJSON' }, { value: 'gpx', label: 'GPX' }, { value: 'csv', label: 'CSV' }]} /></Field>
          <label className="row"><Switch checked={zones} onChange={setZones} label="Leave out privacy zones" /><span>Leave out points inside my privacy zones <span className="muted small">(for a file you share)</span></span></label>
          <div className="row"><a className="btn primary" href={`${V1}/export?${q}`} download><Icon name="download" size={15} />Download</a></div>
        </div>
      </Card>
    </div>
  )
}

function Danger() {
  const { toast } = useToast()
  const { reloadBoot } = useApp()
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [ask, setAsk] = useState<'range' | 'rebuild' | null>(null)
  return (
    <Card title="Clean up" className="danger-zone" sub="Careful: deleting cannot be undone. Export first if you might want it back.">
      <div className="stack loose">
        <div className="stack">
          <b>Delete a date range</b>
          <div className="row wrap">
            <Field label="From"><input type="date" value={from} onChange={e => setFrom(e.target.value)} /></Field>
            <Field label="To"><input type="date" value={to} min={from} onChange={e => setTo(e.target.value)} /></Field>
          </div>
          <div className="row"><button type="button" className="btn danger-text" disabled={!from || !to || to < from} onClick={() => setAsk('range')}><Icon name="trash" size={15} />Delete these days</button></div>
        </div>
        <div className="stack">
          <b>Rebuild journeys and visits</b>
          <p className="mg-hint">Sorts your whole history into journeys, visits and day summaries again, in the background. Confirmed and renamed visits stay as they are. Changing the detection settings does this by itself.</p>
          <div className="row"><button type="button" className="btn" onClick={() => setAsk('rebuild')}><Icon name="refresh" size={15} />Rebuild everything</button></div>
        </div>
      </div>
      {ask === 'range' && <Confirm title={`Delete ${from} – ${to}?`} confirmLabel="Delete" message="Every position recorded on these days is deleted, with the journeys and visits made from them." onCancel={() => setAsk(null)}
        onConfirm={async () => {
          setAsk(null)
          try { const r = await api.del<{ deleted: number }>(`${V1}/history?from=${from}&to=${to}`); toast(`${(r?.deleted || 0).toLocaleString()} points deleted`); reloadBoot().catch(() => {}) } catch (e) { toast(errMsg(e), 'err') }
        }} />}
      {ask === 'rebuild' && <Confirm danger={false} title="Rebuild everything?" confirmLabel="Rebuild" message="It runs in the background; the map shows the raw line meanwhile." onCancel={() => setAsk(null)}
        onConfirm={async () => { setAsk(null); try { await api.post(`${V1}/rebuild`); toast('Rebuilding in the background') } catch (e) { toast(errMsg(e), 'err') } }} />}
    </Card>
  )
}
