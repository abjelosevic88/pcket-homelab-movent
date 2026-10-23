// Places: the spots you stop at — saved ones you named and discovered ones
// from your stops — on a list and a map.
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { agoTs, flag, fmtDur, placeEmoji } from '../lib/geo'
import type { Place } from '../lib/types'
import { MoveMap } from '../components/MoveMap'
import { PlaceEditor } from '../components/PlaceEditor'
import { Icon } from '../components/icons'
import { Confirm, Empty, Loading, Modal, PageHeader, Segmented, errMsg, useAsync, useDebounce, useIsMobile, useToast } from '../components/ui'
import '../styles/places.css'

type Filter = 'all' | 'saved' | 'discovered' | 'hidden'
type Sort = 'visits' | 'time' | 'recent' | 'name'

export function placesBounds(ps: Place[]): [number, number, number, number] | null {
  if (!ps.length) return null
  let a = 90, b = 180, c = -90, d = -180
  for (const p of ps) { a = Math.min(a, p.lat); b = Math.min(b, p.lng); c = Math.max(c, p.lat); d = Math.max(d, p.lng) }
  return [a, b, c, d]
}

export default function Places() {
  const navigate = useNavigate()
  const { toast } = useToast()
  const mobile = useIsMobile()
  const [filter, setFilter] = useState<Filter>('all')
  const [sort, setSort] = useState<Sort>('visits')
  const [q, setQ] = useState('')
  const dq = useDebounce(q, 250)
  const [adding, setAdding] = useState<{ lat: number; lng: number } | true | null>(null)
  const [selecting, setSelecting] = useState(false)
  const [picked, setPicked] = useState<number[]>([])
  const [merging, setMerging] = useState(false)
  const [view, setView] = useState<'list' | 'map'>('list')

  const query = useMemo(() => {
    const p = new URLSearchParams({ sort })
    if (filter === 'saved' || filter === 'discovered') p.set('kind', filter)
    if (filter === 'hidden') p.set('hidden', '1')
    if (dq.trim()) p.set('q', dq.trim())
    return p.toString()
  }, [filter, sort, dq])
  const { data, loading, error, reload } = useAsync(() => api.get<Place[]>(`${V1}/places?${query}`), [query])
  const places = useMemo(() => (data || []).filter(p => filter === 'hidden' ? p.hidden : true), [data, filter])
  const mapPlaces = useMemo(() => places.map(p => ({ ...p, kind: 'saved' as const, hidden: false, radius: p.kind === 'saved' ? p.radius : Math.min(p.radius, 60) })), [places])
  const bounds = useMemo(() => placesBounds(places), [places])

  const toggle = (id: number) => setPicked(p => p.includes(id) ? p.filter(x => x !== id) : [...p, id])
  const counts = useMemo(() => ({ saved: (data || []).filter(p => p.kind === 'saved').length }), [data])

  return (
    <div className="pl-page">
      <PageHeader title="Places" sub={data ? `${places.length} ${places.length === 1 ? 'place' : 'places'}${filter === 'all' && counts.saved ? ` · ${counts.saved} saved` : ''}` : undefined}
        actions={<>
          {selecting
            ? <>
              <button type="button" className="btn" onClick={() => { setSelecting(false); setPicked([]) }}>Cancel</button>
              <button type="button" className="btn primary" disabled={picked.length < 2} onClick={() => setMerging(true)}><Icon name="layers" size={15} />Merge {picked.length || ''}</button>
            </>
            : <>
              <button type="button" className="btn" onClick={() => setSelecting(true)} title="Pick places that are really the same and merge them"><Icon name="check-circle" size={15} />Select</button>
              <button type="button" className="btn primary" onClick={() => setAdding(true)}><Icon name="plus" size={16} />Add a place</button>
            </>}
        </>} />

      <div className="callout info pl-info"><Icon name="info" size={15} /><span>Saved places name and confirm every visit inside them, also past ones. Discovered places come from where you stopped and are named by the map.</span></div>

      <div className="pl-toolbar">
        <Segmented options={[{ value: 'all', label: 'All' }, { value: 'saved', label: 'Saved' }, { value: 'discovered', label: 'Discovered' }, { value: 'hidden', label: 'Hidden' }]} value={filter} onChange={v => setFilter(v as Filter)} size="sm" label="Which places" />
        <div className="pl-search grow">
          <Icon name="search" size={15} />
          <input value={q} onChange={e => setQ(e.target.value)} placeholder="Search name, address, city…" aria-label="Search places" />
          {q && <button type="button" className="btn ghost icon-only xs" onClick={() => setQ('')} aria-label="Clear"><Icon name="x" size={13} /></button>}
        </div>
        <label className="pl-sort">
          <span className="muted small">Sort</span>
          <select value={sort} onChange={e => setSort(e.target.value as Sort)} aria-label="Sort places">
            <option value="visits">Most visits</option>
            <option value="time">Most time</option>
            <option value="recent">Recently visited</option>
            <option value="name">Name</option>
          </select>
        </label>
        {mobile && <Segmented options={[{ value: 'list', label: 'List' }, { value: 'map', label: 'Map' }]} value={view} onChange={v => setView(v as 'list' | 'map')} size="sm" label="View" />}
      </div>

      {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}

      <div className={`pl-split ${mobile ? `m-${view}` : ''}`}>
        <div className="pl-list-col">
          {loading && !data ? <Loading /> : places.length === 0 ? (
            <Empty title={dq ? 'Nothing matches' : filter === 'saved' ? 'No saved places yet' : 'No places yet'} icon="pin">
              {filter === 'saved' || filter === 'all' ? 'Add home and work so visits there are named and confirmed for you.' : 'Places appear here as soon as you stop somewhere for a few minutes.'}
            </Empty>
          ) : (
            <ul className="pl-list">
              {places.map(p => (
                <li key={p.id}>
                  <button type="button" className={`pl-row ${picked.includes(p.id) ? 'picked' : ''}`} onClick={() => selecting ? toggle(p.id) : navigate(`/places/${p.id}`)}>
                    {selecting && <span className={`pl-check ${picked.includes(p.id) ? 'on' : ''}`}>{picked.includes(p.id) && <Icon name="check" size={13} strokeWidth={2.6} />}</span>}
                    <span className={`pl-emoji ${p.kind}`} aria-hidden>{placeEmoji(p.category)}</span>
                    <span className="grow pl-row-main">
                      <span className="pl-row-name">
                        <span className="ellipsis">{p.name || <span className="muted">Unnamed place</span>}</span>
                        {p.kind === 'saved' && <span className="badge accent pl-kind">saved</span>}
                        {p.hidden && <Icon name="eye-off" size={13} className="muted" />}
                      </span>
                      <span className="pl-row-sub ellipsis">
                        {p.country && <span aria-hidden>{flag(p.country)} </span>}{[p.city, p.country_name].filter(Boolean).join(', ') || p.address || `${p.lat.toFixed(4)}, ${p.lng.toFixed(4)}`}
                      </span>
                    </span>
                    <span className="pl-row-stats">
                      <span><b>{p.visits}</b> {p.visits === 1 ? 'visit' : 'visits'}</span>
                      <span className="muted">{p.total_s ? fmtDur(p.total_s) : '—'}{p.last_visit ? ` · ${agoTs(p.last_visit)}` : ''}</span>
                    </span>
                    {!selecting && <Icon name="chevron-right" size={15} className="faint" />}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="pl-map-col">
          <MoveMap places={mapPlaces} bounds={bounds} fitKey={`pl:${query}:${places.length}`} layers={{ places: true, routes: false, visits: false }}
            onMapClick={(lat, lng) => !selecting && setAdding({ lat, lng })} className="pl-map" />
          <div className="pl-map-hint muted xsmall">Tap the map to add a place there</div>
        </div>
      </div>

      {adding && <PlaceEditor initial={adding === true ? undefined : adding} onClose={() => setAdding(null)} onSaved={p => { setAdding(null); reload(); navigate(`/places/${p.id}`) }} />}
      {merging && <MergeModal places={(data || []).filter(p => picked.includes(p.id))} onClose={() => setMerging(false)}
        onDone={into => { setMerging(false); setSelecting(false); setPicked([]); reload(); toast('Places merged'); navigate(`/places/${into}`) }}
        onError={e => toast(e, 'err')} />}
    </div>
  )
}

/** Choose which of the picked places survives; the others fold into it. */
function MergeModal({ places, onClose, onDone, onError }: { places: Place[]; onClose: () => void; onDone: (into: number) => void; onError: (e: string) => void }) {
  const best = [...places].sort((a, b) => (a.kind === 'saved' ? -1 : 0) - (b.kind === 'saved' ? -1 : 0) || b.visits - a.visits)[0]
  const [into, setInto] = useState(best?.id)
  const [busy, setBusy] = useState(false)
  const [confirm, setConfirm] = useState(false)
  const go = async () => {
    setBusy(true)
    try { await api.post(`${V1}/places/${into}/merge`, { ids: places.filter(p => p.id !== into).map(p => p.id) }); onDone(into) } catch (e) { onError(errMsg(e)) } finally { setBusy(false) }
  }
  if (confirm) return <Confirm title="Merge places?" danger={false} confirmLabel="Merge" message={<>The other {places.length - 1} {places.length === 2 ? 'place' : 'places'} and their visits move into <b>{places.find(p => p.id === into)?.name || 'this place'}</b>. This cannot be undone.</>}
    onCancel={() => setConfirm(false)} onConfirm={go} />
  return (
    <Modal title="Merge places" onClose={onClose} size="sm" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy} onClick={() => setConfirm(true)}>Merge</button>
    </>}>
      <p className="muted small" style={{ marginTop: 0 }}>Keep this one; the others and all their visits fold into it.</p>
      <div className="stack tight">
        {places.map(p => (
          <label key={p.id} className={`pl-merge-opt ${into === p.id ? 'on' : ''}`}>
            <input type="radio" name="into" checked={into === p.id} onChange={() => setInto(p.id)} />
            <span aria-hidden>{placeEmoji(p.category)}</span>
            <span className="grow"><b>{p.name || 'Unnamed place'}</b><span className="muted small"> · {p.visits} visits{p.kind === 'saved' ? ' · saved' : ''}</span></span>
          </label>
        ))}
      </div>
    </Modal>
  )
}
