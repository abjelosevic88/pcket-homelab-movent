// The public side of a share link: where someone is (rounded as they chose),
// and today's route when they share it. No sign-in.
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useParams } from 'react-router-dom'
import { V1 } from '../lib/api'
import { agoTs, fmtStamp } from '../lib/geo'
import type { Journey, LivePerson } from '../lib/types'
import { MoveMap } from '../components/MoveMap'
import { Loading } from '../components/ui'
import '../styles/manage.css'

type Shared = LivePerson & { title?: string; today?: string[] }

export default function SharedView() {
  const { token = '' } = useParams()
  const [data, setData] = useState<Shared | null>(null)
  const [gone, setGone] = useState(false)
  const [failed, setFailed] = useState(false)
  const [, tick] = useState(0)
  const load = useCallback(async () => {
    try {
      const r = await fetch(`${V1}/shared/${encodeURIComponent(token)}`, { credentials: 'omit' })
      if (r.status === 404) { setGone(true); setData(null); return }
      if (!r.ok) throw new Error(String(r.status))
      setData(await r.json()); setFailed(false); setGone(false)
    } catch { setFailed(true) }
  }, [token])
  useEffect(() => {
    load()
    const t = setInterval(load, 30000)
    const t2 = setInterval(() => tick(n => n + 1), 15000)
    return () => { clearInterval(t); clearInterval(t2) }
  }, [load])
  useEffect(() => { document.title = data ? `${data.title || data.name} — Pocket Movement` : 'Pocket Movement' }, [data])

  const tracks = useMemo<Journey[]>(() => (data?.today || []).map((p, i) => ({
    id: i + 1, start_ts: 0, end_ts: 0, distance: 0, points: 0, avg_speed: 0, max_speed: 0, elevation_gain: 0, elevation_loss: 0,
    mode: 'unknown', mode_manual: false, start: [0, 0], end: [0, 0], polyline: p, countries: [],
  })), [data?.today])

  if (gone) return (
    <div className="shared-page"><div className="shared-empty">
      <img src="/favicon.svg" alt="" />
      <h2>This link has ended</h2>
      <p className="muted">It expired or was stopped by the person who shared it.</p>
    </div></div>
  )
  if (!data) return <div className="shared-page"><div className="shared-empty">{failed ? <p className="muted">Could not load the location. Retrying…</p> : <Loading />}</div></div>
  const p = data.point
  return (
    <div className="shared-page">
      <MoveMap people={p ? [data] : []} tracks={tracks} colorBy="plain" layers={{ people: true, routes: true, visits: false, places: false }}
        fitKey={p ? `${data.user_id}` : 'none'} bounds={p ? [p.lat - 0.008, p.lng - 0.008, p.lat + 0.008, p.lng + 0.008] : null} />
      <div className="shared-brand"><img src="/favicon.svg" alt="" />Pocket Movement</div>
      <div className="shared-card">
        <h1>{data.title || data.name}</h1>
        {data.title && <div className="meta">{data.name}</div>}
        {p ? (
          <div className="meta">
            {data.place && <span>at <b>{data.place.name || 'a place'}</b></span>}
            <span title={fmtStamp(p.ts)}>updated {agoTs(p.ts)}</span>
            {p.battery != null && <span>🔋 {p.battery}%{p.battery_state === 'charging' ? ' ⚡' : ''}</span>}
            {data.precision_m > 0 && <span>within ~{data.precision_m >= 1000 ? `${data.precision_m / 1000} km` : `${data.precision_m} m`}</span>}
          </div>
        ) : <div className="meta">No position yet.</div>}
        <div className="meta">{data.expires_at ? `Link ends ${fmtStamp(data.expires_at)}` : 'Link does not expire'} · refreshes every 30 s</div>
      </div>
    </div>
  )
}
