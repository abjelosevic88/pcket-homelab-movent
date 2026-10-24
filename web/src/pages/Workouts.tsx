// Workouts from Pocket Health, each drawn from the positions recorded here
// while it ran: the list by day on the left, the map on the right.
import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { fmtClock, fmtDayShort, fmtDist, fmtDur, isoDateOf, modeOf, todayIn, addDaysISO } from '../lib/geo'
import type { Journey, WorkoutsView, Workout } from '../lib/types'
import { MoveMap } from '../components/MoveMap'
import { Icon } from '../components/icons'
import { Loading, NeedsCore, Segmented, useIsMobile } from '../components/ui'

const RANGES = [['7', '7 days'], ['30', '30 days'], ['90', '3 months'], ['365', 'Year']] as const
const KIND_MODE: Record<string, string> = { running: 'running', walking: 'walking', hiking: 'walking', cycling: 'cycling', wheelchair: 'walking' }

export default function Workouts() {
  const [sp, setSp] = useSearchParams()
  const { boot } = useApp()
  const mobile = useIsMobile()
  const days = sp.get('days') || '30'
  const [view, setView] = useState<WorkoutsView | null>(null)
  const [selected, setSelected] = useState<number | null>(null)
  const [detail, setDetail] = useState<{ lat: number; lng: number; speed: number | null }[] | undefined>()
  const [loading, setLoading] = useState(true)
  const today = todayIn()
  const from = addDaysISO(today, -(Number(days) - 1))
  useEffect(() => {
    let live = true
    setLoading(true)
    api.get<WorkoutsView>(`${V1}/workouts?from=${from}&to=${today}`).then(v => { if (live) { setView(v); setSelected(s => s && v.workouts.some(w => w.id === s) ? s : (v.workouts[0]?.id ?? null)) } }).catch(() => {}).finally(() => { if (live) setLoading(false) })
    return () => { live = false }
  }, [days]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    setDetail(undefined)
    if (!selected) return
    let live = true
    api.get<{ points: [number, number, number, number | null][] }>(`${V1}/workouts/${selected}/points`).then(r => { if (live) setDetail(r.points.map(p => ({ lat: p[1], lng: p[2], speed: p[3] }))) }).catch(() => {})
    return () => { live = false }
  }, [selected])
  const coreOn = !!boot?.core?.configured && boot?.core?.registered !== false
  if (loading && !view) return <Loading />
  const workouts = view?.workouts || []
  const tracks: Journey[] = workouts.filter(w => w.polyline).map(w => ({
    id: w.id, start_ts: w.start_ts, end_ts: w.end_ts, distance: w.route_distance || w.distance_m, points: w.route_points, avg_speed: w.duration_s ? ((w.route_distance || w.distance_m) / w.duration_s) * 3.6 : 0,
    max_speed: 0, elevation_gain: w.elevation_m, elevation_loss: 0, mode: KIND_MODE[w.kind] || 'unknown', mode_manual: true, start: w.start, end: w.end, polyline: w.polyline, countries: [],
  }))
  const byDay = new Map<string, Workout[]>()
  for (const w of workouts) { const d = isoDateOf(w.start_ts); byDay.set(d, [...(byDay.get(d) || []), w]) }
  const sel = workouts.find(w => w.id === selected)
  const list = (
    <section className="tlp-list">
      <header className="tlp-head" style={{ gap: 10 }}>
        <div className="grow"><div className="tlp-date-main">Workouts</div><span className="muted small">{view?.connected ? `${workouts.length} from Pocket Health · ${workouts.filter(w => w.polyline).length} with a route` : 'Pocket Health is not connected'}</span></div>
        <Segmented size="sm" value={days} onChange={v => setSp({ days: v })} options={RANGES.map(([v, l]) => ({ value: v, label: l }))} />
      </header>
      {!view?.connected && <div className="pad stack">
        <p className="muted">Workouts come from Pocket Health (your watch and phone send them there). Connect it under <Link to="/settings/apps">Settings → Connected apps</Link> and every workout appears here, drawn from the positions recorded while it ran.{view?.detail ? ` (${view.detail})` : ''}</p>
        {!coreOn && <NeedsCore />}
      </div>}
      {view?.connected && workouts.length === 0 && <p className="muted pad">No workouts in this range. Pocket Health gets them from the phone; log one there or wait for the next sync.</p>}
      {[...byDay.entries()].map(([d, ws]) => (
        <div key={d}>
          <div className="tlp-day-h">{d === today ? 'Today' : fmtDayShort(ws[0].start_ts)}</div>
          {ws.map(w => {
            const m = modeOf(KIND_MODE[w.kind] || 'unknown')
            return (
              <button key={w.id} type="button" className={`wk-row ${w.id === selected ? 'active' : ''}`} onClick={() => setSelected(w.id)}>
                <span className="wk-ico" style={{ background: m.color }}><Icon name={w.kind === 'running' ? 'run' : w.kind === 'cycling' ? 'bike' : w.kind === 'walking' || w.kind === 'hiking' ? 'walk' : 'heart'} size={15} /></span>
                <span className="grow">
                  <b>{w.name || w.kind.replace('_', ' ')}</b>
                  <span className="muted small block">{fmtClock(w.start_ts)} · {fmtDur(w.duration_s)}{w.distance_m ? ` · ${fmtDist(w.distance_m)}` : ''}{w.energy_kcal ? ` · ${Math.round(w.energy_kcal)} kcal` : ''}{w.avg_hr ? ` · ♥ ${Math.round(w.avg_hr)}` : ''}</span>
                  <span className="muted small block">{w.polyline ? `route ${fmtDist(w.route_distance)} from ${w.route_points.toLocaleString()} positions` : 'no positions recorded during it'}</span>
                </span>
                {view?.open_url && <a className="btn ghost icon-only sm" href={`${view.open_url}/workouts/${w.id}`} target="_blank" rel="noreferrer" title="Open in Pocket Health" aria-label="Open in Pocket Health" onClick={e => e.stopPropagation()}><Icon name="external" size={14} /></a>}
              </button>
            )
          })}
        </div>
      ))}
    </section>
  )
  const map = <MoveMap className="tlp-map" tracks={tracks} selected={selected ? { type: 'journey', id: selected } : null} detail={detail} colorBy="mode" layers={{ visits: false, places: false, people: false }} onSelect={s => setSelected(s?.type === 'journey' ? s.id : null)} fitKey={`w${days}${selected}`} bounds={sel?.polyline ? null : null} />
  return <div className={`tlp ${mobile ? 'mobile' : ''}`}>{list}{map}</div>
}
