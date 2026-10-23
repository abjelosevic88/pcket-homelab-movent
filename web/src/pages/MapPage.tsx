// Home: the map of a day or any range, with every layer, a summary and the
// day's timeline beside it; your position and your family's update live.
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { addDaysISO, agoTs, fmtClock, fmtDist, fmtDur, fmtLongDate, fmtShortDate, flag, modeOf, todayIn } from '../lib/geo'
import type { DayView, LivePerson, MapData, NowView, Place } from '../lib/types'
import { DEFAULT_LAYERS, type Layers, MoveMap, type PointRow } from '../components/MoveMap'
import { EntryList } from '../components/timeline'
import { Icon } from '../components/icons'
import { type Anchor, Popover, Spinner, Switch, Segmented, rectOf, useIsMobile } from '../components/ui'

type Preset = 'day' | 'week' | 'month' | 'year' | 'all' | 'custom'
const LAYERS_KEY = 'pm-layers'

function monthEnd(iso: string): string { const [y, m] = iso.split('-').map(Number); return new Date(Date.UTC(y, m, 0)).toISOString().slice(0, 10) }

export default function MapPage() {
  const { boot, settings } = useApp()
  const navigate = useNavigate()
  const mobile = useIsMobile()
  const [sp, setSp] = useSearchParams()
  const today = todayIn()
  const from = sp.get('from') || sp.get('date') || today
  const to = sp.get('to') || sp.get('date') || from
  const single = from === to
  const setRange = useCallback((f: string, t: string) => setSp(p => { const n = new URLSearchParams(p); n.delete('date'); if (f === today && t === today) { n.delete('from'); n.delete('to') } else { n.set('from', f); n.set('to', t) } return n }, { replace: true }), [setSp, today])

  const [layers, setLayers] = useState<Layers>(() => ({ ...DEFAULT_LAYERS, ...JSON.parse(localStorage.getItem(LAYERS_KEY) || '{}') }))
  const setLayer = (k: keyof Layers, v: boolean) => setLayers(l => { const n = { ...l, [k]: v }; localStorage.setItem(LAYERS_KEY, JSON.stringify(n)); return n })
  const [colorBy, setColorBy] = useState<'speed' | 'mode' | 'plain'>(() => (localStorage.getItem('pm-color') as 'speed') || settings?.route_color || 'speed')

  const [data, setData] = useState<MapData | null>(null)
  const [day, setDay] = useState<DayView | null>(null)
  const [points, setPoints] = useState<PointRow[] | undefined>()
  const [heat, setHeat] = useState<[number, number, number][] | undefined>()
  const [fog, setFog] = useState<[number, number, number][] | undefined>()
  const [places, setPlaces] = useState<Place[]>([])
  const [people, setPeople] = useState<LivePerson[]>([])
  const [now, setNow] = useState<NowView | null>(null)
  const [loading, setLoading] = useState(false)
  const [selected, setSelected] = useState<{ type: 'journey' | 'visit'; id: number } | null>(null)
  const [detail, setDetail] = useState<{ lat: number; lng: number; speed: number | null }[] | undefined>()
  const [tick, setTick] = useState(0)
  const reload = () => setTick(t => t + 1)

  // the range
  useEffect(() => {
    let live = true
    setLoading(true); setSelected(null)
    const q = `from=${from}&to=${to}`
    Promise.all([
      api.get<MapData>(`${V1}/map?${q}`),
      single ? api.get<DayView>(`${V1}/day/${from}`) : Promise.resolve(null),
    ]).then(([m, d]) => { if (live) { setData(m); setDay(d) } }).catch(() => {}).finally(() => { if (live) setLoading(false) })
    return () => { live = false }
  }, [from, to, single, tick])
  // layers that cost more load only when switched on
  useEffect(() => {
    if (!layers.points) { setPoints(undefined); return }
    let live = true
    api.get<{ points: PointRow[] }>(`${V1}/map/points?from=${from}&to=${to}&outliers=1`).then(r => { if (live) setPoints(r.points) }).catch(() => {})
    return () => { live = false }
  }, [layers.points, from, to, tick])
  useEffect(() => {
    if (!layers.heat) { setHeat(undefined); return }
    let live = true
    const days = (Date.parse(to) - Date.parse(from)) / 86400000
    const cell = days <= 2 ? 40 : days <= 40 ? 100 : 250
    api.get<{ cells: [number, number, number][] }>(`${V1}/map/heat?from=${from}&to=${to}&cell=${cell}`).then(r => { if (live) setHeat(r.cells) }).catch(() => {})
    return () => { live = false }
  }, [layers.heat, from, to, tick])
  useEffect(() => {
    if (!layers.fog || fog) return
    api.get<{ cells: [number, number, number][] }>(`${V1}/map/heat?all=1&cell=120`).then(r => setFog(r.cells)).catch(() => {})
  }, [layers.fog, fog])
  useEffect(() => { api.get<Place[]>(`${V1}/places?kind=saved`).then(setPlaces).catch(() => {}) }, [tick])
  useEffect(() => {
    api.get<NowView>(`${V1}/now`).then(setNow).catch(() => {})
    if (boot?.shared_with_me.length) api.get<LivePerson[]>(`${V1}/family`).then(setPeople).catch(() => {})
  }, [boot?.shared_with_me.length, tick])
  // the selected journey, point by point, for the speed colours
  useEffect(() => {
    setDetail(undefined)
    if (selected?.type !== 'journey') return
    let live = true
    api.get<{ points: [number, number, number, number | null][] }>(`${V1}/journeys/${selected.id}/points`).then(r => { if (live) setDetail(r.points.map(p => ({ lat: p[1], lng: p[2], speed: p[3] }))) }).catch(() => {})
    return () => { live = false }
  }, [selected])

  // live positions
  useEffect(() => {
    if (typeof EventSource === 'undefined') return
    const es = new EventSource(`${V1}/live`)
    let pending: ReturnType<typeof setTimeout> | null = null
    es.addEventListener('point', ev => {
      const p = JSON.parse((ev as MessageEvent).data) as { user_id: number; lat: number; lng: number; ts: number; battery: number | null; accuracy?: number | null }
      if (p.user_id === boot?.user.id) {
        setNow(n => ({ ...(n || { visit: null, journey: null, devices: [] }), point: { ...(n?.point || { speed: null, battery_state: '', country: '', country_name: '', source: '' }), lat: p.lat, lng: p.lng, ts: p.ts, accuracy: p.accuracy ?? null, battery: p.battery } }))
        // today's map catches up a little later, once the batch is in
        if (to >= today) { if (pending) clearTimeout(pending); pending = setTimeout(reload, 4000) }
      } else {
        setPeople(ps => ps.map(x => x.user_id === p.user_id ? { ...x, point: { ...(x.point || { battery_state: '', accuracy: 0, country: '' }), lat: p.lat, lng: p.lng, ts: p.ts, battery: p.battery } } : x))
      }
    })
    return () => { es.close(); if (pending) clearTimeout(pending) }
  }, [boot?.user.id, to, today])

  // keys: ← → move a day (or the range), . goes to today, l opens layers
  const [layerPop, setLayerPop] = useState<Anchor | null>(null)
  const shift = useCallback((dir: number) => {
    const span = Math.round((Date.parse(to) - Date.parse(from)) / 86400000) + 1
    setRange(addDaysISO(from, dir * span), addDaysISO(to, dir * span))
  }, [from, to, setRange])
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement).closest?.('input, textarea, select') || e.metaKey || e.ctrlKey || e.altKey || document.querySelector('.modal-backdrop, .palette-backdrop, .menu')) return
      if (e.key === 'ArrowLeft') { e.preventDefault(); shift(-1) }
      else if (e.key === 'ArrowRight') { e.preventDefault(); shift(1) }
      else if (e.key === '.') { e.preventDefault(); setRange(today, today) }
      else if (e.key === 'l') { e.preventDefault(); const b = document.querySelector('.mp-layers-btn'); if (b) setLayerPop(p => p ? null : rectOf(b)) }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [shift, setRange, today])

  const visits = useMemo(() => data?.visits || [], [data])
  const preset: Preset = single ? 'day' : from === addDaysISO(to, -6) ? 'week' : from.slice(8) === '01' && to === monthEnd(from) ? 'month' : from.slice(5) === '01-01' && to.slice(5) === '12-31' ? 'year' : from === (boot?.first_day || '') ? 'all' : 'custom'
  const label = single ? (from === today ? 'Today' : from === addDaysISO(today, -1) ? 'Yesterday' : fmtLongDate(from))
    : preset === 'month' ? new Date(from + 'T12:00:00Z').toLocaleDateString(undefined, { month: 'long', year: 'numeric', timeZone: 'UTC' })
      : preset === 'year' ? from.slice(0, 4) : `${fmtShortDate(from, true)} – ${fmtShortDate(to, true)}`
  const choose = (p: Preset) => {
    const anchor = to > today ? today : to
    if (p === 'day') setRange(anchor, anchor)
    else if (p === 'week') setRange(addDaysISO(anchor, -6), anchor)
    else if (p === 'month') { const f = anchor.slice(0, 8) + '01'; setRange(f, monthEnd(f)) }
    else if (p === 'year') setRange(anchor.slice(0, 4) + '-01-01', anchor.slice(0, 4) + '-12-31')
    else if (p === 'all') setRange(boot?.first_day || anchor, today)
  }

  const empty = !!boot && boot.points === 0
  const me = now?.point && (to >= today) ? { lat: now.point.lat, lng: now.point.lng, accuracy: now.point.accuracy, ts: now.point.ts } : null
  const summary = day?.summary
  const dist = single ? summary?.distance || 0 : data?.distance || 0
  const modes = Object.entries(single ? summary?.modes || {} : data?.modes || {}).filter(([, m]) => m > 50).sort((a, b) => b[1] - a[1])

  return (
    <div className="mp">
      <MoveMap className="mp-map" tracks={data?.tracks} raw={data?.raw} visits={visits} places={places} points={points} heat={heat} fogCells={fog} fogCell={120}
        people={people} me={me} layers={layers} colorBy={colorBy} selected={selected} detail={detail} onSelect={setSelected}
        fitKey={`${from}|${to}|${data ? 'd' : ''}`} bounds={data?.bounds || null} />

      <div className="mp-top">
        <div className="mp-range card-float">
          <button type="button" className="btn ghost icon-only" aria-label="Earlier" title="Earlier (←)" onClick={() => shift(-1)}><Icon name="chevron-left" size={18} /></button>
          <label className="mp-range-label" title="Pick a day">
            <span className="ellipsis">{label}</span>{loading && <Spinner size={12} />}
            <input type="date" value={from} max={today} onChange={e => e.target.value && setRange(e.target.value, e.target.value)} aria-label="Pick a day" />
          </label>
          <button type="button" className="btn ghost icon-only" aria-label="Later" title="Later (→)" disabled={to >= today} onClick={() => shift(1)}><Icon name="chevron-right" size={18} /></button>
        </div>
        {!mobile && <Segmented size="sm" className="card-float" value={preset === 'custom' ? null : preset} onChange={choose}
          options={[{ value: 'day', label: 'Day' }, { value: 'week', label: '7 days' }, { value: 'month', label: 'Month' }, { value: 'year', label: 'Year' }, { value: 'all', label: 'All' }]} />}
        <span className="grow" />
        <button type="button" className="btn card-float mp-layers-btn" aria-haspopup="dialog" onClick={e => setLayerPop(layerPop ? null : rectOf(e.currentTarget))}><Icon name="layers" size={16} />{!mobile && 'Layers'}</button>
      </div>
      {mobile && <div className="mp-presets"><Segmented size="sm" className="card-float" value={preset === 'custom' ? null : preset} onChange={choose}
        options={[{ value: 'day', label: 'Day' }, { value: 'week', label: '7 d' }, { value: 'month', label: 'Month' }, { value: 'year', label: 'Year' }, { value: 'all', label: 'All' }]} /></div>}

      {layerPop && (
        <Popover anchor={layerPop} onClose={() => setLayerPop(null)} prefer="below" width={290} label="Map layers">
          <div className="mp-layer-list">
            {([['routes', 'Journeys', 'route'], ['visits', 'Visits', 'pin'], ['places', 'Saved places', 'home'], ['points', 'Every point', 'dot'], ['heat', 'Heat map', 'fire'], ['fog', 'Fog of war (all time)', 'cloud'], ['people', 'Family', 'users']] as [keyof Layers, string, string][]).map(([k, l, ic]) => (
              <label key={k} className="mp-layer"><Icon name={ic} size={16} /><span className="grow">{l}</span><Switch checked={layers[k]} onChange={v => setLayer(k, v)} size="sm" /></label>
            ))}
            <div className="mp-layer-h">Colour journeys by</div>
            <Segmented size="sm" value={colorBy} onChange={v => { setColorBy(v); localStorage.setItem('pm-color', v) }} options={[{ value: 'speed', label: 'Speed' }, { value: 'mode', label: 'Mode' }, { value: 'plain', label: 'Plain' }]} />
            {colorBy === 'speed' && <div className="speed-legend"><span>slow</span><i /><span>fast</span></div>}
          </div>
        </Popover>
      )}

      {empty ? (
        <div className="mp-panel card-float">
          <div className="mp-empty">
            <h2>Nothing recorded yet</h2>
            <p className="muted">Connect your phone — the Dawarich app, OwnTracks, Overland, GPSLogger or Traccar — or bring your history over from Google Takeout, GPX or another Dawarich.</p>
            <div className="row gap"><button type="button" className="btn primary" onClick={() => navigate('/devices?add=1')}><Icon name="smartphone" size={16} />Connect a phone</button><button type="button" className="btn" onClick={() => navigate('/data')}><Icon name="upload" size={16} />Import</button></div>
          </div>
        </div>
      ) : (
        <aside className={`mp-panel card-float ${mobile ? 'sheet' : ''}`} aria-label="Summary">
          <div className="mp-stats">
            <div className="mp-stat"><b>{fmtDist(dist)}</b><span>travelled</span></div>
            {single ? <div className="mp-stat"><b>{summary ? fmtDur(summary.moving_s) : '–'}</b><span>on the move</span></div>
              : <div className="mp-stat"><b>{data?.tracks.length ?? '–'}</b><span>journeys</span></div>}
            <div className="mp-stat"><b>{visits.filter(v => v.status !== 'declined').length}</b><span>visits</span></div>
            {single && summary?.countries.length ? <div className="mp-stat"><b>{summary.countries.map(flag).join(' ')}</b><span>{summary.countries.length > 1 ? 'countries' : 'country'}</span></div> : null}
          </div>
          {modes.length > 0 && (
            <div className="mode-bar" aria-label="Distance by mode">
              {modes.map(([m, d]) => <span key={m} style={{ flex: d, background: modeOf(m).color }} title={`${modeOf(m).label}: ${fmtDist(d)}`} />)}
            </div>
          )}
          {modes.length > 0 && <div className="mode-legend">{modes.slice(0, 4).map(([m, d]) => <span key={m}><i style={{ background: modeOf(m).color }} />{modeOf(m).emoji} {fmtDist(d)}</span>)}</div>}
          {data?.pending && <p className="muted small"><Spinner size={11} /> New points are being sorted into journeys…</p>}
          {now?.point && to >= today && (
            <button type="button" className="mp-now" onClick={() => setSelected(null)}>
              <span className="mm-me-mini" />
              <span className="grow">{now.visit ? <>At <b>{now.visit.name || 'an unnamed place'}</b> since {fmtClock(now.visit.start_ts)}</> : now.journey ? <>{modeOf(now.journey.mode).emoji} On the move</> : 'Last position'}</span>
              <span className="muted small">{agoTs(now.point.ts)}{now.point.battery != null ? ` · 🔋${now.point.battery}%` : ''}</span>
            </button>
          )}
          {single && day ? (
            <div className="mp-entries">
              <EntryList entries={day.entries.filter(e => e.type === 'journey' || e.status !== 'declined')} selected={selected} onSelect={setSelected} onChanged={reload} compact dayStart={day.from} />
              {!day.entries.length && !loading && <p className="muted small">No journeys or visits {from === today ? 'yet today' : 'this day'}.</p>}
              <div className="row gap mt"><Link className="btn sm" to={`/timeline/${from}`}><Icon name="activity" size={14} />Open the day</Link></div>
            </div>
          ) : !single && data ? (
            <RangeDays from={from} to={to} home={boot?.home?.country || ''} />
          ) : null}
        </aside>
      )}
    </div>
  )
}

/** For a range: the days with data, each a link to its timeline. */
function RangeDays({ from, to, home }: { from: string; to: string; home: string }) {
  const [days, setDays] = useState<{ date: string; distance: number; visits: number; countries: string[] }[] | null>(null)
  useEffect(() => { api.get<typeof days>(`${V1}/calendar?from=${from}&to=${to}`).then(setDays).catch(() => setDays([])) }, [from, to])
  if (!days) return null
  const max = Math.max(1, ...days.map(d => d.distance))
  return (
    <ol className="mp-days">
      {[...days].reverse().slice(0, 60).map(d => (
        <li key={d.date}><Link to={`/timeline/${d.date}`}>
          <span className="mp-day-date">{fmtShortDate(d.date)}</span>
          <span className="mp-day-bar"><i style={{ width: `${(d.distance / max) * 100}%` }} /></span>
          <span className="mp-day-km">{fmtDist(d.distance)}</span>
          <span className="mp-day-flags">{d.countries.length > 1 || (d.countries[0] && d.countries[0] !== home) ? d.countries.map(flag).join('') : ''}</span>
        </Link></li>
      ))}
    </ol>
  )
}
