// Create or edit a place: search an address or tap the map for the pin,
// pick a category, size the circle and name it.
import { useEffect, useMemo, useState } from 'react'
import { api, V1 } from '../lib/api'
import { PLACE_CATEGORIES } from '../lib/geo'
import type { Place } from '../lib/types'
import { MoveMap } from './MoveMap'
import { Field, Modal, Segmented, Switch, errMsg, useDebounce, useToast } from './ui'
import { Icon } from './icons'
import '../styles/places.css'

interface GeoHit { name: string; display: string; lat: number; lng: number }

/** Bounds around a point big enough to show a circle of `radius` metres. */
export function boundsAround(lat: number, lng: number, radius: number): [number, number, number, number] {
  const r = Math.max(radius * 2.2, 120)
  const dLat = r / 110540, dLng = r / (111320 * Math.max(0.05, Math.cos(lat * Math.PI / 180)))
  return [lat - dLat, lng - dLng, lat + dLat, lng + dLng]
}

export function PlaceEditor({ place, initial, onClose, onSaved }: { place?: Place; initial?: { lat: number; lng: number; name?: string }; onClose: () => void; onSaved: (p: Place) => void }) {
  const { toast } = useToast()
  const [name, setName] = useState(place?.name ?? initial?.name ?? '')
  const [kind, setKind] = useState<'saved' | 'discovered'>(place?.kind ?? 'saved')
  const [category, setCategory] = useState(place?.category ?? '')
  const [radius, setRadius] = useState(place?.radius ?? 100)
  const [pin, setPin] = useState<{ lat: number; lng: number } | null>(place ? { lat: place.lat, lng: place.lng } : initial ? { lat: initial.lat, lng: initial.lng } : null)
  const [address, setAddress] = useState(place?.address ?? '')
  const [notes, setNotes] = useState(place?.notes ?? '')
  const [hidden, setHidden] = useState(place?.hidden ?? false)
  const [fit, setFit] = useState(0)
  const [busy, setBusy] = useState(false)

  const [q, setQ] = useState('')
  const dq = useDebounce(q, 400)
  const [hits, setHits] = useState<GeoHit[]>([])
  const [searching, setSearching] = useState(false)
  useEffect(() => {
    const n = dq.trim()
    if (n.length < 3) { setHits([]); return }
    let live = true
    setSearching(true)
    api.get<GeoHit[]>(`${V1}/geocode?q=${encodeURIComponent(n)}`).then(r => { if (live) setHits(r || []) }).catch(() => { if (live) setHits([]) }).finally(() => { if (live) setSearching(false) })
    return () => { live = false }
  }, [dq])

  const pick = (h: GeoHit) => {
    setPin({ lat: h.lat, lng: h.lng })
    if (!name) setName(h.name)
    if (!address) setAddress(h.display)
    setHits([]); setQ('')
    setFit(f => f + 1)
  }

  // The circle as it will be, drawn by the shared map as a saved place.
  const preview = useMemo<Place[]>(() => pin ? [{
    id: -1, name: name || 'New place', kind: 'saved', category, color: '', lat: pin.lat, lng: pin.lng, radius, address: '', city: '', country: '', country_name: '',
    geocoded: 1, renamed: true, hidden: false, notes: '', visits: 0, total_s: 0, first_visit: 0, last_visit: 0,
  }] : [], [pin, radius, category, name])
  const bounds = useMemo(() => pin ? boundsAround(pin.lat, pin.lng, radius) : null, [pin, fit]) // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    if (!pin) { toast('Search an address or tap the map to set the pin', 'err'); return }
    setBusy(true)
    const body = { name: name.trim(), kind, category, lat: pin.lat, lng: pin.lng, radius, address, notes, hidden }
    try {
      const p = place ? await api.put<Place>(`${V1}/places/${place.id}`, body) : await api.post<Place>(`${V1}/places`, body)
      toast(place ? 'Place saved' : kind === 'saved' ? 'Place saved — visits inside it are being sorted' : 'Place added')
      onSaved(p)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }

  return (
    <Modal title={place ? 'Edit place' : 'Add a place'} onClose={onClose} size="lg" className="pl-editor" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" onClick={save} disabled={busy || !pin}>{busy ? 'Saving…' : place ? 'Save' : 'Add place'}</button>
    </>}>
      <div className="pl-ed-grid">
        <div className="stack tight">
          <div className="pl-search">
            <Icon name="search" size={16} />
            <input value={q} onChange={e => setQ(e.target.value)} placeholder="Search an address or a name…" aria-label="Search an address" />
            {searching && <span className="spinner" />}
          </div>
          {hits.length > 0 && (
            <div className="pl-hits" role="listbox">
              {hits.map((h, i) => (
                <button type="button" key={i} className="pl-hit" onClick={() => pick(h)}>
                  <Icon name="pin" size={15} /><span className="grow"><b>{h.name}</b><span className="muted small ellipsis">{h.display}</span></span>
                </button>
              ))}
            </div>
          )}
          <div className="pl-ed-map">
            <MoveMap places={preview} bounds={bounds} fitKey={`ed:${fit}:${place?.id ?? 'new'}:${pin ? 1 : 0}`}
              onMapClick={(lat, lng) => setPin({ lat, lng })} layers={{ places: true, visits: false, routes: false }} />
            {!pin && <div className="pl-ed-hint"><Icon name="pin" size={15} />Tap the map to drop the pin</div>}
          </div>
          {pin && <p className="muted xsmall">Pin at {pin.lat.toFixed(5)}, {pin.lng.toFixed(5)} — tap elsewhere on the map to move it.</p>}
        </div>
        <div className="stack tight">
          <Field label="Name" help={kind === 'discovered' && !name ? 'Left empty, the geocoder names it' : undefined}><input value={name} onChange={e => setName(e.target.value)} placeholder="Home, Office, Grandma's…" /></Field>
          <Field label="Category" group>
            <div className="pl-cats">
              {PLACE_CATEGORIES.map(c => (
                <button type="button" key={c.id} className={`chip ${category === c.id ? 'active' : ''}`} onClick={() => setCategory(category === c.id ? '' : c.id)}>
                  <span aria-hidden>{c.emoji}</span>{c.label}
                </button>
              ))}
            </div>
          </Field>
          <Field label={`Radius — ${radius} m`} help="Stops within this circle count as being here." group>
            <input type="range" min={15} max={1000} step={5} value={radius} onChange={e => setRadius(Number(e.target.value))} aria-label="Radius in metres" />
          </Field>
          <Field label="Kind" group help={kind === 'saved' ? 'Saved places name and confirm every visit inside them, also past ones.' : 'Discovered places come from your stops; they are suggestions until you confirm visits.'}>
            <Segmented options={[{ value: 'saved', label: 'Saved' }, { value: 'discovered', label: 'Discovered' }]} value={kind} onChange={v => setKind(v as 'saved' | 'discovered')} size="sm" />
          </Field>
          <Field label="Address"><input value={address} onChange={e => setAddress(e.target.value)} placeholder="Optional" /></Field>
          <Field label="Notes"><textarea rows={2} value={notes} onChange={e => setNotes(e.target.value)} placeholder="Optional" /></Field>
          <div className="row"><Switch checked={hidden} onChange={setHidden} label="Hide from the map and reports" /><span className="small">Hide from the map and reports</span></div>
        </div>
      </div>
    </Modal>
  )
}
