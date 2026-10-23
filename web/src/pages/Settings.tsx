// Settings: preferences, detection, notifications, privacy zones, profile,
// people, API tokens and the server — outside the app shell, like every
// Pocket app's settings.
import React, { useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp, type ThemeMode } from '../lib/app-context'
import { MODES, SWITCHABLE_MODES, agoTs } from '../lib/geo'
import type { APIToken, ModeSpeeds, Place, PrivacyZone, Settings as SettingsT, User } from '../lib/types'
import { Avatar, Confirm, Field, Loading, Modal, NeedsCore, Segmented, SettingRow, Switch, Tabs, errMsg, useAsync, useIsMobile, useToast } from '../components/ui'
import { Icon } from '../components/icons'
import { Select } from '../components/Select'
import { MoveMap, TILES, tileKey } from '../components/MoveMap'
import { ShortcutsSheet } from '../components/ShortcutsSheet'
import { AskButton } from '../components/AskButton'
import '../styles/manage.css'

type TabId = 'general' | 'detection' | 'notifications' | 'privacy' | 'profile' | 'users' | 'api' | 'about'

const TABS: { id: TabId; label: string; icon: string; sub: string }[] = [
  { id: 'general', label: 'General', icon: 'settings', sub: 'Units, time zone, map tiles, route colours and the look.' },
  { id: 'detection', label: 'Detection', icon: 'route', sub: 'How positions become visits and journeys.' },
  { id: 'notifications', label: 'Notifications', icon: 'bell', sub: 'What Pocket Movement tells you about, through Pocket Core.' },
  { id: 'privacy', label: 'Privacy zones', icon: 'lock', sub: 'Circles kept out of shared views and exports — or never recorded at all.' },
  { id: 'profile', label: 'Profile', icon: 'user', sub: 'Your name, email and password.' },
  { id: 'users', label: 'People', icon: 'users', sub: 'Everyone who has an account on this box.' },
  { id: 'api', label: 'API tokens', icon: 'key', sub: 'Personal tokens for scripts and widgets. Phones use device keys (Devices).' },
  { id: 'about', label: 'Backup & about', icon: 'info', sub: 'Version, numbers, the server and a database backup.' },
]
const ALIAS: Record<string, TabId> = { people: 'users', tokens: 'api', data: 'about', zones: 'privacy' }

interface AccountSettings { settings: SettingsT; effective_timezone: string; server_timezone: string; is_admin: boolean }

export default function Settings() {
  const { tab: raw } = useParams()
  const navigate = useNavigate()
  const mobile = useIsMobile()
  const tab: TabId = (TABS.find(t => t.id === (ALIAS[raw || ''] || raw))?.id) || 'general'
  const current = TABS.find(t => t.id === tab)!
  const go = (id: TabId) => navigate(`/settings/${id}`, { replace: true })
  useEffect(() => { if (raw && raw !== tab) navigate(`/settings/${tab}`, { replace: true }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { document.querySelector('.set-content')?.scrollTo({ top: 0 }) }, [tab])
  const [shortcuts, setShortcuts] = useState(false)
  useEffect(() => {
    const open = () => setShortcuts(true)
    window.addEventListener('pm:shortcuts', open)
    return () => window.removeEventListener('pm:shortcuts', open)
  }, [])
  useEffect(() => {
    const h = (e: KeyboardEvent) => { if (e.key === 'Escape' && !document.querySelector('.modal-backdrop, .menu, .sel-pop')) navigate('/') }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [navigate])

  return (
    <div className="set-page">
      <header className="set-header">
        <button type="button" className="btn ghost icon-only" aria-label="Back to the map" title="Back to the map (esc)" onClick={() => navigate('/')}><Icon name="chevron-left" size={18} /></button>
        <img src="/favicon.svg" alt="" width={24} height={24} className="set-logo" />
        <h1>Settings</h1>
        <span style={{ flex: 1 }} />
        <AskButton />
      </header>
      {mobile && <div className="set-tabs-mobile"><Tabs tabs={TABS.map(t => ({ id: t.id, label: t.label, icon: t.icon }))} value={tab} onChange={go} /></div>}
      <div className="set-body">
        {!mobile && (
          <nav className="set-nav" aria-label="Settings sections">
            {TABS.map(t => (
              <button key={t.id} type="button" className={`set-nav-item ${t.id === tab ? 'active' : ''}`} aria-current={t.id === tab ? 'page' : undefined} onClick={() => go(t.id)}>
                <Icon name={t.icon} size={16} />{t.label}
              </button>
            ))}
          </nav>
        )}
        <main className="set-content">
          <div className="set-inner" key={tab}>
            <div className="set-title"><h2>{current.label}</h2><p>{current.sub}</p></div>
            {tab === 'general' && <General />}
            {tab === 'detection' && <Detection />}
            {tab === 'notifications' && <Notifications />}
            {tab === 'privacy' && <PrivacyZones />}
            {tab === 'profile' && <Profile />}
            {tab === 'users' && <Users />}
            {tab === 'api' && <Tokens />}
            {tab === 'about' && <About />}
          </div>
        </main>
      </div>
      {shortcuts && <ShortcutsSheet onClose={() => setShortcuts(false)} />}
    </div>
  )
}

/** A settings card: title, explanation, actions on the right. */
function Section({ title, sub, icon, actions, children }: { title: React.ReactNode; sub?: React.ReactNode; icon?: string; actions?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <section className="set-section">
      <div className="set-section-head">
        {icon && <span className="set-app-ico"><Icon name={icon} size={17} /></span>}
        <div className="grow"><h3>{title}</h3>{sub && <p>{sub}</p>}</div>
        {actions && <div className="row">{actions}</div>}
      </div>
      {children && <div className="set-section-body">{children}</div>}
    </section>
  )
}

function useAccountSettings() {
  const { setSettings, user } = useApp()
  const { toast } = useToast()
  const { data } = useAsync(() => api.get<AccountSettings>(`${V1}/account/settings`), [])
  const [f, setF] = useState<SettingsT | null>(null)
  const [busy, setBusy] = useState(false)
  const [rebuilding, setRebuilding] = useState(false)
  useEffect(() => { if (data && !f) setF(data.settings) }, [data]) // eslint-disable-line react-hooks/exhaustive-deps
  const save = async (patch?: Partial<SettingsT>) => {
    if (!f) return
    setBusy(true)
    try {
      const r = await api.put<{ settings: SettingsT; rebuilding: boolean }>(`${V1}/account/settings`, { ...f, ...patch })
      setSettings(r.settings); setF(r.settings); setRebuilding(r.rebuilding)
      toast(r.rebuilding ? 'Saved — rebuilding your history in the background' : 'Saved')
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return { f, setF, save, busy, rebuilding, info: data, isAdmin: user?.role === 'admin' }
}

function NumInput({ value, onChange, unit, min, max, step = 1 }: { value: number; onChange: (n: number) => void; unit: string; min?: number; max?: number; step?: number }) {
  return (
    <span className="num-input-wrap">
      <input className="num-input" type="number" inputMode="numeric" value={Number.isFinite(value) ? value : ''} min={min} max={max} step={step} onChange={e => onChange(Number(e.target.value))} />
      <span>{unit}</span>
    </span>
  )
}

function SaveRow({ busy, onSave, children }: { busy: boolean; onSave: () => void; children?: React.ReactNode }) {
  return <div className="row">{children}<span className="grow" /><button type="button" className="btn primary" disabled={busy} onClick={onSave}>{busy ? 'Saving…' : 'Save changes'}</button></div>
}

// ---------- general ----------

function General() {
  const { f, setF, save, busy, info, isAdmin } = useAccountSettings()
  const { themeMode, setThemeMode, status } = useApp()
  const [title, setTitle] = useState('')
  useEffect(() => { if (f && !title) setTitle(status?.title || f.title) }, [f]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!f) return <Loading />
  return (
    <div className="stack loose">
      <Section title="Look">
        <SettingRow title="Theme"><Segmented size="sm" value={themeMode} onChange={(v: ThemeMode) => setThemeMode(v)} label="Theme" options={[{ value: 'system', label: 'System' }, { value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }]} /></SettingRow>
        {isAdmin && <SettingRow title="App title" help="Shown in the sidebar and the browser tab, for everyone on this box."><input value={title} onChange={e => setTitle(e.target.value)} maxLength={60} style={{ maxWidth: 240 }} /></SettingRow>}
      </Section>
      <Section title="Units & time" sub="Yours alone — everyone on the box picks their own.">
        <SettingRow title="Distances"><Segmented size="sm" value={f.distance_unit} onChange={v => setF({ ...f, distance_unit: v })} label="Distances" options={[{ value: 'km', label: 'km' }, { value: 'mi', label: 'miles' }]} /></SettingRow>
        <SettingRow stack title="Time zone" help={<>Days start at midnight here. Empty uses the server's ({info?.server_timezone || '…'}); now in effect: <b>{info?.effective_timezone}</b>. Changing it re-sorts your history into days.</>}>
          <TimeZonePicker value={f.timezone} onChange={tz => setF({ ...f, timezone: tz })} />
        </SettingRow>
      </Section>
      <Section title="Map">
        <SettingRow title="Map tiles" help="All free and keyless. The dark tiles suit the dark theme.">
          <Select value={tileKey(f.map_tiles)} onChange={v => setF({ ...f, map_tiles: v })} options={Object.entries(TILES).map(([v, t]) => ({ value: v, label: t.label }))} />
        </SettingRow>
        <SettingRow title="Colour routes by" help="Speed: slow green to fast red. Mode: walking, cycling, driving… each its own colour.">
          <Segmented size="sm" value={f.route_color} onChange={v => setF({ ...f, route_color: v })} label="Route colour" options={[{ value: 'speed', label: 'Speed' }, { value: 'mode', label: 'Mode' }, { value: 'plain', label: 'Plain' }]} />
        </SettingRow>
      </Section>
      <SaveRow busy={busy} onSave={() => save(isAdmin && title.trim() ? { title: title.trim() } : undefined)} />
    </div>
  )
}

function TimeZonePicker({ value, onChange }: { value: string; onChange: (tz: string) => void }) {
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  const zones = useMemo(() => {
    try { return (Intl as unknown as { supportedValuesOf: (k: string) => string[] }).supportedValuesOf('timeZone') } catch { return ['UTC', 'Europe/Sarajevo', 'Europe/Berlin', 'Europe/London', 'America/New_York'] }
  }, [])
  const list = useMemo(() => { const n = q.trim().toLowerCase().replace(/\s+/g, '_'); return (n ? zones.filter(z => z.toLowerCase().includes(n)) : zones).slice(0, 200) }, [q, zones])
  const browser = Intl.DateTimeFormat().resolvedOptions().timeZone
  return (
    <div className="stack" style={{ gap: 6, width: '100%' }}>
      <div className="row wrap" style={{ gap: 6 }}>
        <button type="button" className="btn sm" onClick={() => setOpen(o => !o)}><Icon name="globe" size={14} />{value || 'The server’s'}</button>
        {value !== '' && <button type="button" className="btn ghost sm" onClick={() => onChange('')}>Use the server’s</button>}
        {browser && browser !== value && <button type="button" className="btn ghost sm" onClick={() => onChange(browser)}>This device: {browser}</button>}
      </div>
      {open && <div>
        <input value={q} onChange={e => setQ(e.target.value)} placeholder="Search, e.g. Sarajevo" autoFocus aria-label="Search time zones" />
        <div className="tz-list" role="listbox">
          {list.map(z => <button key={z} type="button" role="option" aria-selected={z === value} className={z === value ? 'active' : ''} onClick={() => { onChange(z); setOpen(false); setQ('') }}>{z.replace(/_/g, ' ')}</button>)}
          {!list.length && <div className="muted small" style={{ padding: 10 }}>No match.</div>}
        </div>
      </div>}
    </div>
  )
}

// ---------- detection ----------

function Detection() {
  const { f, setF, save, busy, rebuilding } = useAccountSettings()
  if (!f) return <Loading />
  return (
    <div className="stack loose">
      <Section title="Visits" sub="A visit is a stop: staying near one spot for a while.">
        <SettingRow title="Visit radius" help="How far you may wander and still be “at the same place”. Bigger finds fewer, larger visits (a campus, a mall); smaller tells neighbouring shops apart.">
          <NumInput value={f.visit_radius} onChange={n => setF({ ...f, visit_radius: n })} unit="m" min={25} max={500} step={5} />
        </SettingRow>
        <SettingRow title="Shortest stop" help="Stops shorter than this count as part of the journey (traffic lights, a quick drop-off).">
          <NumInput value={f.visit_min_minutes} onChange={n => setF({ ...f, visit_min_minutes: n })} unit="min" min={1} max={240} />
        </SettingRow>
      </Section>
      <Section title="Journeys" sub="Movement between two visits, with a transport mode guessed from speed and your phone’s activity.">
        <SettingRow title="Gap that splits a journey" help="If the phone goes quiet this long while moving (flight mode, empty battery), the next point starts a new journey.">
          <NumInput value={f.track_gap_minutes} onChange={n => setF({ ...f, track_gap_minutes: n })} unit="min" min={5} max={1440} />
        </SettingRow>
        <SettingRow title="Walk, then ride" help={<>Walking to the car or getting off the bus splits the journey in two when the ride reaches this top speed for a minute, next to at least two minutes at walking pace (under {f.mode_speeds.walk_top} km/h).</>}>
          <NumInput value={f.mode_speeds.ride_from} onChange={n => setF({ ...f, mode_speeds: { ...f.mode_speeds, ride_from: n } })} unit="km/h" min={10} max={80} step={1} />
        </SettingRow>
      </Section>
      <TransportModes f={f} setF={setF} />
      <Section title="Cleaning up" sub="Bad positions are kept but left out of routes, distances and visits.">
        <SettingRow title="Worst accuracy kept" help="Points less accurate than this (cell towers, indoors) are left out. 0 keeps every point.">
          <NumInput value={f.max_accuracy} onChange={n => setF({ ...f, max_accuracy: n })} unit="m" min={0} max={5000} step={10} />
        </SettingRow>
        <SettingRow title="Fastest believable jump" help="A point that jumps away and back faster than this is a GPS glitch. Real flights are recognised and kept.">
          <NumInput value={f.max_speed_kmh} onChange={n => setF({ ...f, max_speed_kmh: n })} unit="km/h" min={50} max={2000} step={10} />
        </SettingRow>
      </Section>
      {rebuilding && <div className="callout info"><Icon name="refresh" size={15} /><span>Your history is being sorted again with these settings. The map shows raw lines until it is done; confirmed and renamed visits stay as they are.</span></div>}
      <SaveRow busy={busy} onSave={() => save()}><span className="mg-hint">Saving a change here rebuilds your whole history in the background.</span></SaveRow>
    </div>
  )
}

// The rules classify() in internal/analyze/segment.go applies, top to bottom.
const MODE_RULES: { mode: string; help: string; speeds: { key: keyof ModeSpeeds; label: string; min: number; max: number }[] }[] = [
  { mode: 'walking', help: 'Top speed and typical speed both under these.', speeds: [{ key: 'walk_top', label: 'top under', min: 3, max: 20 }, { key: 'walk_typical', label: 'typical under', min: 2, max: 20 }] },
  { mode: 'running', help: 'Top speed under this, typical speed in between, under 4 h and 45 km.', speeds: [{ key: 'run_top', label: 'top under', min: 8, max: 30 }, { key: 'run_typical_min', label: 'typical from', min: 3, max: 30 }, { key: 'run_typical_max', label: 'to', min: 5, max: 30 }] },
  { mode: 'cycling', help: 'Top speed and typical speed both under these.', speeds: [{ key: 'cycle_top', label: 'top under', min: 10, max: 60 }, { key: 'cycle_typical', label: 'typical under', min: 5, max: 60 }] },
  { mode: 'train', help: 'Top speed above this, over 40 km.', speeds: [{ key: 'train_top', label: 'top above', min: 60, max: 400 }] },
  { mode: 'bus', help: 'Average and top speed under these, stopping every few hundred metres.', speeds: [{ key: 'bus_avg', label: 'average under', min: 5, max: 150 }, { key: 'bus_top', label: 'top under', min: 20, max: 150 }] },
  { mode: 'driving', help: 'Everything faster that is not a bus or a train.', speeds: [] },
  { mode: 'boat', help: 'Mostly over water, top speed under this. Checked before the rest.', speeds: [{ key: 'boat_top', label: 'top under', min: 20, max: 150 }] },
  { mode: 'flying', help: 'Top speed above this, or a long jump of over 150 km. Checked first.', speeds: [{ key: 'fly_top', label: 'top above', min: 150, max: 1200 }] },
]

function TransportModes({ f, setF }: { f: SettingsT; setF: (s: SettingsT) => void }) {
  const off = f.modes_off || []
  const toggle = (m: string) => setF({ ...f, modes_off: off.includes(m) ? off.filter(x => x !== m) : [...off, m] })
  const setSpeed = (k: keyof ModeSpeeds, n: number) => setF({ ...f, mode_speeds: { ...f.mode_speeds, [k]: n } })
  // zero means "the default" to the server
  const reset = () => setF({ ...f, mode_speeds: Object.fromEntries(Object.keys(f.mode_speeds).map(k => [k, 0])) as unknown as ModeSpeeds })
  return (
    <Section title="Transport modes" sub="Your phone sends no activity, so the mode is guessed from speed. “Top” is the speed a journey stays under 85% of the time, “typical” its middle speed, so one GPS jump or a red light does not decide it."
      actions={<button type="button" className="btn ghost sm" onClick={reset}>Default speeds</button>}>
      <SettingRow stack title="How you travel" help="Turn off what you never use. Its journeys go to the closest mode you do use: a slower one when their typical speed suits it (a walk with a jumpy GPS), otherwise a faster one (a car in slow traffic). Journeys you set by hand keep their mode.">
        <div className="chips">
          {SWITCHABLE_MODES.map(m => <button key={m} type="button" className={`chip ${off.includes(m) ? '' : 'active'}`} aria-pressed={!off.includes(m)} onClick={() => toggle(m)}>{MODES[m].emoji} {MODES[m].label}</button>)}
        </div>
      </SettingRow>
      {MODE_RULES.filter(r => !off.includes(r.mode)).map(r => (
        <SettingRow key={r.mode} stack={r.speeds.length > 1} title={<>{MODES[r.mode].emoji} {MODES[r.mode].label}</>} help={r.help}>
          {r.speeds.length ? <div className="row wrap" style={{ gap: 12 }}>
            {r.speeds.map(s => <label key={s.key} className="row" style={{ gap: 6 }}><span className="muted small">{s.label}</span><NumInput value={f.mode_speeds[s.key]} onChange={n => setSpeed(s.key, n)} unit="km/h" min={s.min} max={s.max} step={0.5} /></label>)}
          </div> : <span className="muted small">no speed to set</span>}
        </SettingRow>
      ))}
    </Section>
  )
}

// ---------- notifications ----------

function Notifications() {
  const { f, setF, save, busy } = useAccountSettings()
  const { boot } = useApp()
  if (!f) return <Loading />
  const core = boot?.core
  return (
    <div className="stack loose">
      {core && !core.configured && <div className="callout warn"><Icon name="info" size={15} /><NeedsCore what="Notices go out through Pocket Core, which this server is not connected to." /></div>}
      {core?.configured && !core.registered && <div className="callout warn"><Icon name="alert" size={15} /><span>Pocket Core does not answer: {core.error || 'not registered yet'}.</span></div>}
      {core?.registered && <div className="callout success"><Icon name="check-circle" size={15} /><span>Connected to Pocket Core: notices reach your phone the way you set it up in Core, and Pocket AI can answer “where was I…”.</span></div>}
      <fieldset className="core-off" disabled={!core?.registered} aria-disabled={!core?.registered || undefined}>
      <Section title="Tell me when">
        <SettingRow title="A phone goes quiet" help="No position from any of your phones for this long (a dead battery, tracking switched off). 0 never tells.">
          <NumInput value={f.notify_stale_hours} onChange={n => setF({ ...f, notify_stale_hours: n })} unit="h" min={0} max={336} />
        </SettingRow>
        <SettingRow title="I enter another country" help="“Welcome to Croatia” when the first position arrives from a new country."><Switch checked={f.notify_countries} onChange={v => setF({ ...f, notify_countries: v })} label="Country notices" /></SettingRow>
      </Section>
      </fieldset>
      <p className="mg-hint">Exact positions never leave this server in a notice: only place names, countries and times.</p>
      <SaveRow busy={busy} onSave={() => save()} />
    </div>
  )
}

// ---------- privacy zones ----------

function PrivacyZones() {
  const { toast } = useToast()
  const { boot } = useApp()
  const { data, reload } = useAsync(() => api.get<PrivacyZone[]>(`${V1}/privacy-zones`), [])
  const [edit, setEdit] = useState<Partial<PrivacyZone> | null>(null)
  const [del, setDel] = useState<PrivacyZone | null>(null)
  const home = boot?.home
  return (
    <div className="stack loose">
      <Section title="Your zones" sub="Inside a zone, people you share with see the zone's centre instead of your position, and exports can leave the zone out. Switch on “don't record” to drop positions inside it the moment they arrive."
        actions={<button type="button" className="btn primary sm" onClick={() => setEdit({ name: home ? 'Home' : '', radius: 200, drop_points: false, ...(home ? { lat: home.lat, lng: home.lng } : {}) })}><Icon name="plus" size={14} />Add a zone</button>}>
        {!data ? <Loading /> : !data.length ? <p className="muted">No zones yet. A zone around home is a good start.</p> : (
          <div>{data.map(z => (
            <div key={z.id} className="zone-row">
              <span className="share-ico"><Icon name="lock" size={15} /></span>
              <div className="grow"><b>{z.name || 'Zone'}</b><div className="muted small">{Math.round(z.radius)} m{z.drop_points ? ' · positions inside are not recorded' : ' · hidden from shared views'}</div></div>
              <button type="button" className="btn ghost sm" onClick={() => setEdit(z)}>Edit</button>
              <button type="button" className="btn ghost sm danger-text" onClick={() => setDel(z)}>Delete</button>
            </div>
          ))}</div>
        )}
      </Section>
      {edit && <ZoneModal zone={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload() }} />}
      {del && <Confirm title={`Delete “${del.name || 'zone'}”?`} confirmLabel="Delete" message="Shared views show your exact position there again." onCancel={() => setDel(null)}
        onConfirm={async () => { const z = del; setDel(null); try { await api.del(`${V1}/privacy-zones/${z.id}`); reload() } catch (e) { toast(errMsg(e), 'err') } }} />}
    </div>
  )
}

function ZoneModal({ zone, onClose, onSaved }: { zone: Partial<PrivacyZone>; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [f, setF] = useState({ name: zone.name || '', lat: zone.lat, lng: zone.lng, radius: zone.radius || 200, drop_points: !!zone.drop_points })
  const [busy, setBusy] = useState(false)
  const preview: Place[] = f.lat != null && f.lng != null ? [{ id: -1, name: f.name, kind: 'saved', category: '', color: '', lat: f.lat, lng: f.lng, radius: f.radius, address: '', city: '', country: '', country_name: '', geocoded: 1, renamed: true, hidden: false, notes: '', visits: 0, total_s: 0, first_visit: 0, last_visit: 0 }] : []
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (f.lat == null || f.lng == null) { toast('Tap the map to place the zone', 'err'); return }
    setBusy(true)
    try {
      if (zone.id) await api.put(`${V1}/privacy-zones/${zone.id}`, f); else await api.post(`${V1}/privacy-zones`, f)
      toast('Zone saved'); onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={zone.id ? 'Edit zone' : 'Add a privacy zone'} onClose={onClose}>
      <form onSubmit={save} className="stack">
        <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} placeholder="Home" maxLength={60} /></Field>
        <div className="zone-map"><MoveMap places={preview} layers={{ places: true, routes: false, visits: false }} onMapClick={(lat, lng) => setF(x => ({ ...x, lat, lng }))}
          fitKey={zone.id ? `z${zone.id}` : f.lat != null ? 'set' : 'none'} bounds={f.lat != null && f.lng != null ? [f.lat - 0.006, f.lng - 0.006, f.lat + 0.006, f.lng + 0.006] : null} /></div>
        <p className="mg-hint">{f.lat == null ? 'Tap the map where the zone should be.' : `Centre ${f.lat.toFixed(5)}, ${f.lng!.toFixed(5)} — tap elsewhere to move it.`}</p>
        <Field label={`Radius: ${f.radius} m`}><input type="range" min={50} max={2000} step={25} value={f.radius} onChange={e => setF({ ...f, radius: Number(e.target.value) })} /></Field>
        <label className="row"><Switch checked={f.drop_points} onChange={v => setF({ ...f, drop_points: v })} label="Don't record inside" /><span>Don’t record positions inside at all <span className="muted small">(they are thrown away on arrival)</span></span></label>
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy}>Save</button></div>
      </form>
    </Modal>
  )
}

// ---------- profile ----------

function Profile() {
  const { user, setUser, status } = useApp()
  const { toast } = useToast()
  const [f, setF] = useState({ name: user?.name || '', email: user?.email || '', current_password: '', new_password: '' })
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { setUser(await api.put<User>(`${V1}/auth/me`, f)); setF(x => ({ ...x, current_password: '', new_password: '' })); toast('Profile saved') } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <form onSubmit={save} className="stack loose">
      <div className="set-profile">
        <Avatar name={user?.name} email={user?.email} size={56} />
        <div className="grow"><h3>{user?.name || user?.email}</h3><p className="muted small">{user?.email} · {user?.role}</p></div>
      </div>
      <Section title="You">
        <div className="stack">
          <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} /></Field>
          <Field label="Email"><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} /></Field>
        </div>
      </Section>
      {status?.auth_mode === 'local' && <Section title="Password" sub="Leave empty to keep the current one."><div className="stack">
        <Field label="Current password"><input type="password" value={f.current_password} onChange={e => setF({ ...f, current_password: e.target.value })} autoComplete="current-password" /></Field>
        <Field label="New password" help="At least 8 characters."><input type="password" value={f.new_password} onChange={e => setF({ ...f, new_password: e.target.value })} autoComplete="new-password" /></Field>
      </div></Section>}
      <div className="row"><button className="btn primary" disabled={busy}>Save changes</button></div>
    </form>
  )
}

// ---------- people ----------

function Users() {
  const { user, users, reloadBoot, status } = useApp()
  const { toast } = useToast()
  const [edit, setEdit] = useState<Partial<User> | null>(null)
  const [del, setDel] = useState<User | null>(null)
  const isAdmin = user?.role === 'admin'
  const run = async (fn: () => Promise<unknown>, msg: string) => { try { await fn(); await reloadBoot(); toast(msg) } catch (e) { toast(errMsg(e), 'err') } }
  return (
    <Section title="People on this box" actions={isAdmin && <button type="button" className="btn primary sm" onClick={() => setEdit({ role: 'member' })}><Icon name="plus" size={14} />Add person</button>}
      sub="Everyone signs in with their own account and has their own history. Nobody — admins included — sees anyone else's movements unless they share them under Family & sharing.">
      <div className="set-people">
        {users.map(u => (
          <div key={u.id} className="set-person">
            <Avatar name={u.name} email={u.email} size={34} />
            <div className="grow">
              <div className="set-person-name">{u.name || <span className="muted">—</span>}{u.id === user?.id && <span className="badge">you</span>}{u.role === 'admin' && <span className="badge accent">admin</span>}</div>
              <div className="muted small ellipsis">{u.email}</div>
            </div>
            {isAdmin && <span className="row" style={{ justifyContent: 'flex-end' }}><button type="button" className="btn ghost sm" onClick={() => setEdit(u)}>Edit</button>{u.id !== user?.id && <button type="button" className="btn ghost sm danger-text" onClick={() => setDel(u)}>Remove</button>}</span>}
          </div>
        ))}
      </div>
      {status?.auth_mode !== 'local' && <p className="muted small mt">Auth mode is <code>{status?.auth_mode}</code>: accounts are created on first visit; passwords are not used here.</p>}
      {edit && <UserModal user={edit} local={status?.auth_mode === 'local'} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reloadBoot() }} />}
      {del && <Confirm confirmLabel="Remove" title={`Remove ${del.name || del.email}?`} message="Their account, devices and their whole location history are deleted." onCancel={() => setDel(null)}
        onConfirm={() => { const d = del; setDel(null); run(() => api.del(`${V1}/users/${d.id}`), 'Person removed') }} />}
    </Section>
  )
}

function UserModal({ user, local, onClose, onSaved }: { user: Partial<User>; local: boolean; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [f, setF] = useState({ name: user.name || '', email: user.email || '', password: '', role: user.role || 'member' })
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { if (user.id) await api.put(`${V1}/users/${user.id}`, f); else await api.post(`${V1}/users`, f); toast(user.id ? 'Saved' : 'Person added'); onSaved() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={user.id ? 'Edit person' : 'Add a person'} onClose={onClose} size="sm">
      <form onSubmit={save} className="grid" style={{ gap: 14 }}>
        <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} autoFocus required maxLength={60} /></Field>
        <Field label="Email" help="Used to sign in (also with Pocket Core)."><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} required /></Field>
        {local && <Field label={user.id ? 'New password (leave empty to keep)' : 'Password (optional with Pocket Core sign-in)'} help="At least 8 characters."><input type="password" value={f.password} onChange={e => setF({ ...f, password: e.target.value })} minLength={f.password ? 8 : 0} autoComplete="new-password" /></Field>}
        <Field group label="Role"><Segmented value={f.role} onChange={v => setF({ ...f, role: v })} options={[{ value: 'member', label: 'Member' }, { value: 'admin', label: 'Admin' }]} /></Field>
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy}>{user.id ? 'Save' : 'Add person'}</button></div>
      </form>
    </Modal>
  )
}

// ---------- tokens ----------

function Tokens() {
  const { toast } = useToast()
  const { boot } = useApp()
  const { data, reload } = useAsync(() => api.get<APIToken[]>(`${V1}/tokens`), [])
  const [name, setName] = useState('')
  const [fresh, setFresh] = useState('')
  const [del, setDel] = useState<APIToken | null>(null)
  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    try { const r = await api.post<{ token: string }>(`${V1}/tokens`, { name: name || 'token' }); setFresh(r.token); setName(''); reload() } catch (e) { toast(errMsg(e), 'err') }
  }
  const base = boot?.base_url || window.location.origin
  return (
    <div className="stack loose">
      <Section title="Personal tokens" icon="key" sub={<>A token acts as you, everywhere in the API: <code>Authorization: Bearer &lt;token&gt;</code>. Try <code>{base}/api/v1/summary?date=…</code> or <code>/api/v1/where?at=2026-10-20T14:30</code>. For phones and trackers use a device key under Devices instead — it can be limited to sending.</>}>
        <form onSubmit={create} className="row mb"><input value={name} onChange={e => setName(e.target.value)} placeholder="Token name (e.g. Homepage widget)" aria-label="Token name" /><button className="btn primary"><Icon name="plus" size={15} />Create</button></form>
        {fresh && <div className="set-fresh mb"><div className="row"><Icon name="check-circle" size={16} /><b>Copy it now — it is shown once.</b></div><pre className="mono" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{fresh}</pre></div>}
        {data && data.length > 0 ? (
          <table className="table compact">
            <thead><tr><th>Name</th><th>Prefix</th><th>Last used</th><th /></tr></thead>
            <tbody>{data.map(t => <tr key={t.id}><td>{t.name}</td><td className="mono">{t.prefix}…</td><td className="muted">{t.last_used_at ? agoTs(Date.parse(t.last_used_at) / 1000) : 'never'}</td><td className="right"><button type="button" className="btn ghost sm danger-text" onClick={() => setDel(t)}>Revoke</button></td></tr>)}</tbody>
          </table>
        ) : <p className="muted">No tokens yet.</p>}
        {del && <Confirm confirmLabel="Revoke" title={`Revoke “${del.name}”?`} message="Anything using this token stops working immediately." onCancel={() => setDel(null)} onConfirm={async () => { await api.del(`${V1}/tokens/${del.id}`); setDel(null); reload() }} />}
      </Section>
    </div>
  )
}

// ---------- backup & about ----------

function fmtBytes(n: number): string {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB']
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`
}

function About() {
  const { user, status } = useApp()
  const isAdmin = user?.role === 'admin'
  const { data } = useAsync(() => api.get<Record<string, unknown>>(`${V1}/system`), [])
  const counts = (data?.counts || {}) as Record<string, number>
  const core = (data?.core || {}) as { configured?: boolean; registered?: boolean; error?: string; url?: string }
  const tiles: [string, number | undefined, string][] = [['Points', counts.points, 'dot'], ['Journeys', counts.tracks, 'route'], ['Visits', counts.visits, 'pin'], ['Places', counts.places, 'home'], ['Days', counts.days, 'calendar'], ['Devices', counts.devices, 'smartphone']]
  const rows: [string, React.ReactNode][] = data ? [
    ['Version', <code key="v">{String(data.version)}</code>],
    ['Dawarich API', <span key="d">compatible with Dawarich {String(data.dawarich_api)} clients</span>],
    ['Uptime', String(data.uptime)],
    ['Runtime', `${String(data.go)} · ${fmtBytes(Number(data.memory))} in use`],
    ['Database', fmtBytes(Number(data.db_size))],
    ['Sign-in', String(data.auth_mode)],
    ['Place names', data.geocoder === 'off' ? 'off' : `${String(data.geocoder)} (${String(data.geocoder_url)})`],
    ['Pocket Core', core.configured ? (core.registered ? <span key="c"><span className="set-status ok" /> connected · {core.url}</span> : <span key="c">not answering: {core.error}</span>) : 'not connected'],
    ['Address', <code key="u">{String(data.base_url)}</code>],
  ] : []
  return (
    <div className="stack loose">
      <div className="set-about-hero">
        <img src="/favicon.svg" alt="" width={48} height={48} />
        <div>
          <h3>Pocket Movement{status?.version && <span className="muted small"> · {/^v?\d/.test(status.version) ? 'v' + status.version.replace(/^v/, '') : status.version}</span>}</h3>
          <p className="muted small">Your location history, kept at home: where you went, how and how far, the places you stop and the countries you have been. Speaks the Dawarich API, so the Dawarich app, OwnTracks, Overland, Traccar, GPSLogger and Home Assistant all work. One binary, one SQLite file, one volume.</p>
        </div>
      </div>
      {isAdmin && data && <div className="set-stats">
        {tiles.map(([label, n, icon]) => (
          <div key={label} className="set-stat"><Icon name={icon} size={16} /><span className="set-stat-n tnum">{(n ?? 0).toLocaleString()}</span><span className="set-stat-l">{label}</span></div>
        ))}
      </div>}
      {data ? <Section title="This server"><dl className="set-dl">{rows.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}</dl></Section> : <Loading />}
      {isAdmin && <Section title="Database backup" icon="database" sub="Admins only.">
        <p className="muted">A consistent copy of the SQLite database (<code>VACUUM INTO</code>) with everyone's history, places and devices. Restore by stopping the app and replacing <code>pocket-movement.db</code> in the data directory. Your own history as GeoJSON, GPX or CSV is under Import &amp; export.</p>
        <a className="btn primary" href={`${V1}/backup`}><Icon name="download" size={15} />Download database</a>
      </Section>}
      <p className="muted small">Open source (MIT). Maps © OpenStreetMap contributors; country borders from Natural Earth; place names from OpenStreetMap Nominatim.</p>
      <div className="row wrap">
        <button type="button" className="btn" onClick={() => window.dispatchEvent(new CustomEvent('pm:shortcuts'))}><Icon name="keyboard" size={15} />Keyboard shortcuts</button>
        <a className="btn" href="https://github.com/abjelosevic88/pocket-homelab-movement" target="_blank" rel="noreferrer"><Icon name="external" size={15} />Project on GitHub</a>
      </div>
    </div>
  )
}
