// One place: where it is, how often and how long you were there, when you
// usually come, every visit — and editing, hiding, merging.
import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { flag, fmtClock, fmtDateTs, fmtDayShort, fmtDur, fmtHours, isoDateOf, placeEmoji, PLACE_CATEGORIES } from '../lib/geo'
import type { Place, Visit, VisitStatus } from '../lib/types'
import { MoveMap } from '../components/MoveMap'
import { PlaceEditor, boundsAround } from '../components/PlaceEditor'
import { Icon } from '../components/icons'
import { Confirm, Empty, Loading, Modal, type MenuItem, MenuButton, errMsg, useAsync, useDebounce, useToast } from '../components/ui'
import '../styles/places.css'

interface Detail { place: Place; visits: Visit[]; hours: number[]; weekdays: number[] }

export default function PlaceDetail() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { toast } = useToast()
  const { data, loading, error, reload, setData } = useAsync(() => api.get<Detail>(`${V1}/places/${id}`), [id])
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [merging, setMerging] = useState(false)
  const [showAll, setShowAll] = useState(false)

  const p = data?.place
  const counted = useMemo(() => (data?.visits || []).filter(v => v.status !== 'declined'), [data])
  const bounds = useMemo(() => p ? boundsAround(p.lat, p.lng, p.radius) : null, [p?.id]) // eslint-disable-line react-hooks/exhaustive-deps
  const mapPlace = useMemo(() => p ? [{ ...p, kind: 'saved' as const, hidden: false }] : [], [p])

  if (loading && !data) return <Loading />
  if (error || !p) return <Empty title="Place not found" icon="pin">{error || 'It may have been merged into another place.'} <Link to="/places">All places</Link></Empty>

  const update = async (body: Partial<Place>, msg: string) => {
    try { await api.put<Place>(`${V1}/places/${p.id}`, body); toast(msg); reload() } catch (e) { toast(errMsg(e), 'err') }
  }
  const setStatus = async (v: Visit, status: VisitStatus) => {
    try {
      await api.put(`${V1}/visits/${v.id}`, { status })
      setData(d => d ? { ...d, visits: d.visits.map(x => x.id === v.id ? { ...x, status } : x) } : d)
    } catch (e) { toast(errMsg(e), 'err') }
  }
  const remove = async () => {
    try { await api.del(`${V1}/places/${p.id}`); toast('Place deleted'); navigate('/places') } catch (e) { toast(errMsg(e), 'err') }
  }

  const avg = counted.length ? p.total_s / counted.length : 0
  const cat = PLACE_CATEGORIES.find(c => c.id === p.category)
  const menu: MenuItem[] = [
    { label: 'Edit', icon: 'edit', onSelect: () => setEditing(true) },
    p.kind === 'saved'
      ? { label: 'Make it a discovered place', icon: 'pin', onSelect: () => update({ kind: 'discovered' }, 'No longer a saved place') }
      : { label: 'Make it a saved place', icon: 'star', onSelect: () => update({ kind: 'saved' }, 'Saved — visits inside it are confirmed') },
    { label: p.hidden ? 'Show on the map and in reports' : 'Hide from the map and reports', icon: p.hidden ? 'eye' : 'eye-off', onSelect: () => update({ hidden: !p.hidden }, p.hidden ? 'Shown again' : 'Hidden') },
    { label: 'Merge another place into this one', icon: 'layers', onSelect: () => setMerging(true) },
    'sep',
    { label: 'Delete', icon: 'trash', danger: true, onSelect: () => setDeleting(true) },
  ]
  const visits = showAll ? data!.visits : data!.visits.slice(0, 30)

  return (
    <div className="pd-page">
      <Link to="/places" className="pd-back muted small"><Icon name="chevron-left" size={14} />Places</Link>
      <header className="pd-head">
        <span className={`pl-emoji lg ${p.kind}`} aria-hidden>{placeEmoji(p.category)}</span>
        <div className="grow">
          <h1 className="pd-title">
            {p.name || <span className="muted">Unnamed place</span>}
            {p.kind === 'saved' && <span className="badge accent">saved</span>}
            {p.hidden && <span className="badge"><Icon name="eye-off" size={11} /> hidden</span>}
          </h1>
          <div className="pd-sub">
            {cat && <span>{cat.label}</span>}
            {(p.city || p.country) && <span>{p.country && flag(p.country)} {[p.city, p.country_name].filter(Boolean).join(', ')}</span>}
            {p.address && <span className="ellipsis">{p.address}</span>}
          </div>
          {p.notes && <p className="pd-notes">{p.notes}</p>}
        </div>
        <div className="row">
          <button type="button" className="btn" onClick={() => setEditing(true)}><Icon name="edit" size={15} />Edit</button>
          <MenuButton label="More" items={menu} align="right" width={260}><Icon name="more" size={18} /></MenuButton>
        </div>
      </header>

      <div className="pd-grid">
        <div className="pd-map card">
          <MoveMap places={mapPlace} visits={counted} bounds={bounds} fitKey={`pd:${p.id}`} layers={{ places: true, visits: true, routes: false }}
            onSelect={s => { const v = data!.visits.find(x => s && x.id === s.id); if (v) navigate(`/timeline/${isoDateOf(v.start_ts)}`) }} />
        </div>
        <div className="pd-tiles">
          <Tile label="Visits" value={String(p.visits)} />
          <Tile label="Time here" value={p.total_s ? fmtHours(p.total_s) : '—'} />
          <Tile label="Average stay" value={avg ? fmtDur(avg) : '—'} />
          <Tile label="First visit" value={p.first_visit ? fmtDateTs(p.first_visit) : '—'} />
          <Tile label="Last visit" value={p.last_visit ? fmtDateTs(p.last_visit) : '—'} />
          <Tile label="Radius" value={`${Math.round(p.radius)} m`} />
        </div>
      </div>

      {p.total_s > 0 && (
        <section className="card pd-when">
          <div className="card-head"><div className="grow"><h2>When you usually come here</h2><p className="muted small">Time spent by hour of day and by weekday</p></div></div>
          <div className="card-body pd-charts">
            <Bars values={data!.hours} labels={data!.hours.map((_, i) => String(i))} tick={i => i % 6 === 0} title="By hour of day" fmt={s => fmtHours(s)} />
            <Bars values={[1, 2, 3, 4, 5, 6, 0].map(i => data!.weekdays[i])} labels={['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']} tick={() => true} title="By weekday" fmt={s => fmtHours(s)} />
          </div>
        </section>
      )}

      <section className="card">
        <div className="card-head"><div className="grow"><h2>Visits</h2><p className="muted small">{data!.visits.length} recorded{data!.visits.length > counted.length ? `, ${data!.visits.length - counted.length} declined` : ''}</p></div></div>
        <div className="card-body flush">
          {data!.visits.length === 0 ? <Empty title="No visits yet" icon="clock">Visits appear here once you stop inside this place.</Empty> : (
            <ul className="pd-visits">
              {visits.map(v => (
                <li key={v.id} className={`pd-visit ${v.status}`}>
                  <Link to={`/timeline/${isoDateOf(v.start_ts)}`} className="grow pd-visit-main">
                    <span className="pd-visit-date">{fmtDayShort(v.start_ts)} <span className="muted">{isoDateOf(v.start_ts).slice(0, 4)}</span></span>
                    <span className="muted small">{fmtClock(v.start_ts)}–{fmtClock(v.end_ts)} · {fmtDur(v.end_ts - v.start_ts)}{v.own_name ? ` · “${v.own_name}”` : ''}</span>
                  </Link>
                  <span className={`badge ${v.status === 'confirmed' ? 'ok' : v.status === 'declined' ? 'danger' : 'warn'}`}>{v.status}</span>
                  {v.status !== 'confirmed' && <button type="button" className="btn ghost icon-only sm" title="Confirm" aria-label="Confirm visit" onClick={() => setStatus(v, 'confirmed')}><Icon name="check" size={15} /></button>}
                  {v.status !== 'declined' && <button type="button" className="btn ghost icon-only sm" title="Decline — I was not here" aria-label="Decline visit" onClick={() => setStatus(v, 'declined')}><Icon name="x" size={15} /></button>}
                </li>
              ))}
            </ul>
          )}
          {!showAll && data!.visits.length > 30 && <div className="pd-more"><button type="button" className="btn sm" onClick={() => setShowAll(true)}>Show all {data!.visits.length}</button></div>}
        </div>
      </section>

      {editing && <PlaceEditor place={p} onClose={() => setEditing(false)} onSaved={() => { setEditing(false); reload() }} />}
      {deleting && <Confirm title="Delete this place?" confirmLabel="Delete" message={<>Its {p.visits} visits stay in your timeline, without a place. {p.kind === 'saved' ? 'Stops there are detected again as a new place.' : ''}</>} onCancel={() => setDeleting(false)} onConfirm={remove} />}
      {merging && <MergeInto place={p} onClose={() => setMerging(false)} onDone={() => { setMerging(false); toast('Merged'); reload() }} />}
    </div>
  )
}

function Tile({ label, value }: { label: string; value: string }) {
  return <div className="pd-tile"><span className="muted xsmall">{label}</span><b>{value}</b></div>
}

/** A small accessible bar chart: one bar per value, the peak labelled. */
function Bars({ values, labels, tick, title, fmt }: { values: number[]; labels: string[]; tick: (i: number) => boolean; title: string; fmt: (v: number) => string }) {
  const max = Math.max(1, ...values)
  const peak = values.indexOf(Math.max(...values))
  const W = 100 / values.length
  return (
    <figure className="pd-bars">
      <figcaption className="muted small">{title}{values[peak] > 0 && <> · most at <b>{labels[peak]}</b></>}</figcaption>
      <svg viewBox="0 0 100 44" preserveAspectRatio="none" role="img" aria-label={`${title}: ${values.map((v, i) => `${labels[i]} ${fmt(v)}`).join(', ')}`}>
        {values.map((v, i) => {
          const h = v ? Math.max(1.5, (v / max) * 40) : 0
          return <rect key={i} x={i * W + W * 0.15} y={42 - h} width={W * 0.7} height={h} rx={0.8} className={i === peak ? 'peak' : ''}><title>{`${labels[i]}: ${fmt(v)}`}</title></rect>
        })}
        <line x1="0" x2="100" y1="42.3" y2="42.3" className="axis" />
      </svg>
      <div className="pd-bar-labels">{labels.map((l, i) => <span key={i} style={{ width: `${W}%` }}>{tick(i) ? l : ''}</span>)}</div>
    </figure>
  )
}

/** Pick another place (nearby first) whose visits move into this one. */
function MergeInto({ place, onClose, onDone }: { place: Place; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const [q, setQ] = useState('')
  const dq = useDebounce(q, 250)
  const { data } = useAsync(() => api.get<Place[]>(`${V1}/places?hidden=1&sort=${dq ? 'visits' : 'name'}${dq ? `&q=${encodeURIComponent(dq)}` : ''}`), [dq])
  const [pick, setPick] = useState<Place | null>(null)
  const list = useMemo(() => {
    const d = (data || []).filter(x => x.id !== place.id)
    const dist = (x: Place) => Math.hypot(x.lat - place.lat, (x.lng - place.lng) * Math.cos(place.lat * Math.PI / 180))
    return dq ? d : [...d].sort((a, b) => dist(a) - dist(b))
  }, [data, dq, place])
  const go = async () => {
    if (!pick) return
    try { await api.post(`${V1}/places/${place.id}/merge`, { ids: [pick.id] }); onDone() } catch (e) { toast(errMsg(e), 'err') }
  }
  if (pick) return <Confirm title="Merge?" danger={false} confirmLabel="Merge" message={<><b>{pick.name || 'Unnamed place'}</b> and its {pick.visits} visits move into <b>{place.name || 'this place'}</b>. This cannot be undone.</>} onCancel={() => setPick(null)} onConfirm={go} />
  return (
    <Modal title="Merge another place into this one" onClose={onClose}>
      <div className="pl-search"><Icon name="search" size={15} /><input autoFocus value={q} onChange={e => setQ(e.target.value)} placeholder="Search places…" aria-label="Search places" /></div>
      <p className="muted xsmall">{dq ? 'Matching places' : 'Nearest first'}</p>
      <ul className="pl-list compact">
        {list.slice(0, 40).map(x => (
          <li key={x.id}><button type="button" className="pl-row" onClick={() => setPick(x)}>
            <span className={`pl-emoji ${x.kind}`} aria-hidden>{placeEmoji(x.category)}</span>
            <span className="grow pl-row-main"><span className="pl-row-name ellipsis">{x.name || 'Unnamed place'}</span><span className="pl-row-sub ellipsis">{[x.city, x.address].filter(Boolean).join(' · ')}</span></span>
            <span className="muted small">{x.visits} visits</span>
          </button></li>
        ))}
      </ul>
    </Modal>
  )
}
