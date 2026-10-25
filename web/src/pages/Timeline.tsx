// One day, Google-Timeline style: where you stayed and how you moved, in
// order, to confirm, name and correct — with the day's map beside it.
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { addDaysISO, currentTZ, fmtClock, fmtDist, fmtDur, fmtLongDate, flag, modeOf, todayIn } from '../lib/geo'
import type { DayView, Visit } from '../lib/types'
import { MoveMap } from '../components/MoveMap'
import { EntryList } from '../components/timeline'
import { purchasesByVisit, usePurchases } from '../lib/purchases'
import { Icon } from '../components/icons'
import { Field, Loading, Modal, Spinner, errMsg, useIsMobile, useToast } from '../components/ui'

export default function Timeline() {
  const { date } = useParams()
  const { boot } = useApp()
  const navigate = useNavigate()
  const mobile = useIsMobile()
  const { toast } = useToast()
  const today = todayIn()
  const [day, setDay] = useState<DayView | null>(null)
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState<{ type: 'journey' | 'visit'; id: number } | null>(null)
  const [detail, setDetail] = useState<{ lat: number; lng: number; speed: number | null }[] | undefined>()
  const [adding, setAdding] = useState<{ lat: number; lng: number } | null>(null)
  const [pickOnMap, setPickOnMap] = useState(false)
  const [tick, setTick] = useState(0)
  const reload = useCallback(() => setTick(t => t + 1), [])
  const purchases = usePurchases(date || '', date || '', tick)
  const paid = useMemo(() => purchasesByVisit(day?.entries.filter((e): e is Visit & { type: 'visit' } => e.type === 'visit') || [], purchases?.items || []), [day, purchases])

  useEffect(() => {
    if (!date) return
    let live = true
    setLoading(true)
    api.get<DayView>(`${V1}/day/${date}`).then(d => { if (live) setDay(d) }).catch(() => {}).finally(() => { if (live) setLoading(false) })
    return () => { live = false }
  }, [date, tick])
  useEffect(() => { setSelected(null) }, [date])
  useEffect(() => {
    setDetail(undefined)
    if (selected?.type !== 'journey') return
    let live = true
    api.get<{ points: [number, number, number, number | null][] }>(`${V1}/journeys/${selected.id}/points`).then(r => { if (live) setDetail(r.points.map(p => ({ lat: p[1], lng: p[2], speed: p[3] }))) }).catch(() => {})
    return () => { live = false }
  }, [selected])
  // a pending day refreshes itself while new points are sorted
  useEffect(() => {
    if (!day?.pending) return
    const t = setTimeout(reload, 8000)
    return () => clearTimeout(t)
  }, [day, reload])
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement).closest?.('input, textarea, select') || e.metaKey || e.ctrlKey || e.altKey || document.querySelector('.modal-backdrop, .palette-backdrop, .menu')) return
      if (!date) return
      if (e.key === 'ArrowLeft') { e.preventDefault(); navigate(`/timeline/${addDaysISO(date, -1)}`) }
      else if (e.key === 'ArrowRight' && date < today) { e.preventDefault(); navigate(`/timeline/${addDaysISO(date, 1)}`) }
      else if (e.key === '.') { e.preventDefault(); navigate(`/timeline/${today}`) }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [date, navigate, today])

  // Whatever gets selected (a chip, the map) scrolls into view in the list.
  useEffect(() => {
    if (selected) document.querySelector(`.tlp-list [data-pocket-ask="${selected.type === 'visit' ? 'visit' : 'journey'} ${selected.id}"]`)?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [selected])

  if (!date) return <Navigate to={`/timeline/${boot?.last_day && boot.last_day < today ? boot.last_day : today}`} replace />

  const s = day?.summary
  const entries = day?.entries || []
  const visits = entries.filter(e => e.type === 'visit') as (Visit & { type: 'visit' })[]
  const suggested = visits.filter(v => v.status === 'suggested')
  const tracks = entries.filter(e => e.type === 'journey').map(e => e as Extract<typeof e, { type: 'journey' }>)
  const modes = Object.entries(s?.modes || {}).filter(([, m]) => m > 50).sort((a, b) => b[1] - a[1])
  const confirmAll = async () => {
    try { await api.post(`${V1}/visits/bulk`, { ids: suggested.map(v => v.id), status: 'confirmed' }); toast(`${suggested.length} visits confirmed`); reload() } catch (e) { toast(errMsg(e), 'err') }
  }

  return (
    <div className={`tlp ${mobile ? 'mobile' : ''}`}>
      <section className="tlp-list">
        <header className="tlp-head">
          <button type="button" className="btn ghost icon-only" aria-label="Previous day" title="Previous day (←)" onClick={() => navigate(`/timeline/${addDaysISO(date, -1)}`)}><Icon name="chevron-left" size={18} /></button>
          <label className="tlp-date">
            <span className="tlp-date-main">{date === today ? 'Today' : date === addDaysISO(today, -1) ? 'Yesterday' : fmtLongDate(date)}</span>
            {(date === today || date === addDaysISO(today, -1)) && <span className="muted small">{fmtLongDate(date)}</span>}
            <input type="date" value={date} max={today} onChange={e => e.target.value && navigate(`/timeline/${e.target.value}`)} aria-label="Pick a day" />
          </label>
          <button type="button" className="btn ghost icon-only" aria-label="Next day" title="Next day (→)" disabled={date >= today} onClick={() => navigate(`/timeline/${addDaysISO(date, 1)}`)}><Icon name="chevron-right" size={18} /></button>
        </header>
        {(day?.prev || day?.next) && (
          <div className="tlp-jump muted small">
            {day.prev && day.prev !== addDaysISO(date, -1) && <Link to={`/timeline/${day.prev}`}>← last day with data ({day.prev})</Link>}
            <span className="grow" />
            {day.next && day.next !== addDaysISO(date, 1) && <Link to={`/timeline/${day.next}`}>next with data ({day.next}) →</Link>}
          </div>
        )}

        {loading && !day ? <Loading /> : (
          <>
            <div className="tlp-stats">
              <div><b>{fmtDist(s?.distance || 0)}</b><span>travelled</span></div>
              <div><b>{fmtDur(s?.moving_s || 0)}</b><span>moving</span></div>
              <div><b>{visits.filter(v => v.status !== 'declined').length}</b><span>visits</span></div>
              <div><b>{s?.countries.length ? s.countries.map(flag).join(' ') : '–'}</b><span>{(s?.countries.length || 0) > 1 ? 'countries' : 'country'}</span></div>
            </div>
            {modes.length > 0 && <>
              <div className="mode-bar">{modes.map(([m, d]) => <span key={m} style={{ flex: d, background: modeOf(m).color }} title={`${modeOf(m).label}: ${fmtDist(d)}`} />)}</div>
              <div className="mode-legend">{modes.map(([m, d]) => <span key={m}><i style={{ background: modeOf(m).color }} />{modeOf(m).emoji} {modeOf(m).label} {fmtDist(d)}</span>)}</div>
            </>}
            {day?.pending && <p className="muted small"><Spinner size={11} /> New points are being sorted into journeys and visits…</p>}
            {suggested.length > 1 && (
              <div className="callout tlp-suggest">
                <div className="tlp-suggest-head"><Icon name="sparkles" size={15} /><span className="grow">{suggested.length} visits are suggestions — tap one to see it, then confirm or decline it in the list, or confirm them all.</span><button type="button" className="btn sm" onClick={confirmAll}>Confirm all</button></div>
                <div className="tlp-suggest-list">
                  {suggested.map(v => (
                    <button key={v.id} type="button" className={`tlp-chip ${selected?.type === 'visit' && selected.id === v.id ? 'on' : ''}`} onClick={() => setSelected({ type: 'visit', id: v.id })}>
                      <span className="tnum muted">{fmtClock(v.start_ts)}</span> {v.name || 'Unnamed place'}
                    </button>
                  ))}
                </div>
              </div>
            )}
            {s && s.first_ts > 0 && <p className="muted small tlp-span">Recorded {fmtClock(s.first_ts)}–{fmtClock(s.last_ts)} · {s.points.toLocaleString()} points</p>}
            <EntryList entries={entries} selected={selected} onSelect={setSelected} onChanged={reload} dayStart={day?.from} paid={paid} />
            {!entries.length && !loading && (
              <div className="tlp-empty">
                <Icon name="cloud" size={28} />
                <p>{date === today ? 'Nothing recorded yet today.' : 'Nothing recorded this day.'}</p>
                {boot?.points === 0 && <Link className="btn primary" to="/devices?add=1">Connect a phone</Link>}
              </div>
            )}
            <div className="row gap mt">
              <button type="button" className={`btn sm ${pickOnMap ? 'primary' : ''}`} onClick={() => setPickOnMap(p => !p)}><Icon name="plus" size={14} />{pickOnMap ? 'Click the map where you were…' : 'Add a visit'}</button>
              <Link className="btn sm" to={`/?from=${date}&to=${date}`}><Icon name="map" size={14} />Big map</Link>
              <a className="btn sm" href={`${V1}/export?format=gpx&from=${date}&to=${date}`}><Icon name="download" size={14} />GPX</a>
            </div>
          </>
        )}
      </section>
      <section className="tlp-map">
        <MoveMap tracks={tracks} visits={visits} purchases={purchases?.items} selected={selected} detail={detail} onSelect={setSelected} colorBy="mode"
          onMapClick={pickOnMap ? (lat, lng) => { setPickOnMap(false); setAdding({ lat, lng }) } : undefined}
          fitKey={`${date}|${day ? entries.length : ''}`} className={pickOnMap ? 'picking' : ''} />
      </section>
      {adding && <AddVisit date={date} at={adding} onClose={() => setAdding(null)} onSaved={() => { setAdding(null); reload() }} />}
    </div>
  )
}

/** A visit added by hand: somewhere the phone missed. */
function AddVisit({ date, at, onClose, onSaved }: { date: string; at: { lat: number; lng: number }; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [name, setName] = useState('')
  const [start, setStart] = useState('12:00')
  const [end, setEnd] = useState('13:00')
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    try {
      const iso = (hm: string) => zonedISO(date, hm)
      await api.post(`${V1}/visits`, { started_at: iso(start), ended_at: iso(end), latitude: at.lat, longitude: at.lng, name })
      onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title="Add a visit" onClose={onClose} size="sm" footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="button" className="btn primary" disabled={busy || !name.trim()} onClick={save}>Add</button></>}>
      <div className="stack">
        <Field label="Where"><input value={name} onChange={e => setName(e.target.value)} placeholder="e.g. Dentist" autoFocus /></Field>
        <div className="row gap">
          <Field label="From"><input type="time" value={start} onChange={e => setStart(e.target.value)} /></Field>
          <Field label="To"><input type="time" value={end} onChange={e => setEnd(e.target.value)} /></Field>
        </div>
        <p className="muted small">At {at.lat.toFixed(5)}, {at.lng.toFixed(5)} on {fmtLongDate(date)}.</p>
      </div>
    </Modal>
  )
}

/** "2026-10-20" + "14:30" in the person's time zone → an ISO instant. */
function zonedISO(date: string, hm: string): string {
  const tz = currentTZ()
  const [y, m, d] = date.split('-').map(Number), [hh, mm] = hm.split(':').map(Number)
  // Guess as UTC, then correct by the zone's offset at that moment (twice, for DST edges).
  let t = Date.UTC(y, m - 1, d, hh, mm)
  for (let i = 0; i < 2; i++) {
    const parts = new Intl.DateTimeFormat('en-US', { timeZone: tz, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).formatToParts(t)
    const g = (k: string) => Number(parts.find(p => p.type === k)?.value)
    const shown = Date.UTC(g('year'), g('month') - 1, g('day'), g('hour'), g('minute'))
    t -= shown - Date.UTC(y, m - 1, d, hh, mm)
  }
  return new Date(t).toISOString()
}
