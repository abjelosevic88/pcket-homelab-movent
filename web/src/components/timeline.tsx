// Timeline rows: visits and journeys of a day, with the edits people make
// most (confirm, decline, rename, pick the right place, correct the mode).
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { MODES, fmtClock, fmtDist, fmtDur, fmtSpeed, isoDateOf, modeOf, placeEmoji } from '../lib/geo'
import type { Entry, Journey, Visit } from '../lib/types'
import { type Purchase, fmtMoney, shopNamesVisit, shopOf } from '../lib/purchases'
import { Icon } from './icons'
import { Field, type Anchor, Menu, type MenuItem, Modal, errMsg, rectOf, useToast } from './ui'

export function EntryList({ entries, selected, onSelect, onChanged, compact = false, readOnly = false, dayStart = 0, paid }: {
  entries: Entry[]; selected?: { type: string; id: number } | null; onSelect?: (s: { type: 'journey' | 'visit'; id: number }) => void
  onChanged?: () => void; compact?: boolean; readOnly?: boolean; dayStart?: number
  /** purchases from Pocket Money, by the visit they were paid during */
  paid?: Map<number, Purchase[]>
}) {
  if (!entries.length) return null
  return (
    <ol className={`tl ${compact ? 'compact' : ''}`}>
      {entries.map(e => e.type === 'visit'
        ? <VisitRow key={'v' + e.id} v={e} sel={selected?.type === 'visit' && selected.id === e.id} onSelect={onSelect} onChanged={onChanged} compact={compact} readOnly={readOnly} earlier={dayStart > 0 && e.start_ts < dayStart} paid={paid?.get(e.id)} />
        : <JourneyRow key={'j' + e.id} j={e} sel={selected?.type === 'journey' && selected.id === e.id} onSelect={onSelect} onChanged={onChanged} compact={compact} readOnly={readOnly} />)}
    </ol>
  )
}

function VisitRow({ v, sel, onSelect, onChanged, compact, readOnly, earlier, paid }: { v: Visit; sel: boolean; onSelect?: (s: { type: 'visit'; id: number }) => void; onChanged?: () => void; compact: boolean; readOnly: boolean; earlier?: boolean; paid?: Purchase[] }) {
  const { toast } = useToast()
  const [edit, setEdit] = useState(false)
  const [menu, setMenu] = useState<Anchor | null>(null)
  const set = async (body: Record<string, unknown>, msg?: string) => {
    try { await api.put(`${V1}/visits/${v.id}`, body); if (msg) toast(msg); onChanged?.() } catch (e) { toast(errMsg(e), 'err') }
  }
  const shop = paid?.length ? shopOf(paid[0]) : ''
  const items: MenuItem[] = [
    ...(shop && v.name !== shop && shopNamesVisit(v) ? [{ label: `Name it “${shop}” (from the receipt)`, icon: 'receipt', onSelect: () => set({ name: shop }, `Named ${shop}`) }] : []),
    { label: 'Rename or choose the place…', icon: 'edit', onSelect: () => setEdit(true) },
    ...(v.status !== 'confirmed' ? [{ label: 'Confirm', icon: 'check', onSelect: () => set({ status: 'confirmed' }, 'Confirmed') }] : []),
    ...(v.status !== 'declined' ? [{ label: 'Not a visit (decline)', icon: 'x', onSelect: () => set({ status: 'declined' }, 'Declined') }] : [{ label: 'Restore', icon: 'undo', onSelect: () => set({ status: 'suggested' }) }]),
    ...(v.place_id ? ['sep' as const, { label: 'Open the place', icon: 'pin', onSelect: () => { window.location.href = `/places/${v.place_id}` } }] : []),
  ]
  const dur = v.end_ts - v.start_ts
  return (
    <li className={`tl-row visit ${v.status} ${sel ? 'sel' : ''}`} onClick={() => onSelect?.({ type: 'visit', id: v.id })} data-pocket-ask={`visit ${v.id}`}>
      <div className="tl-time">{earlier ? <span className="tl-earlier" title={`Since ${fmtClock(v.start_ts)} the day before`}>↑ {fmtClock(v.start_ts)}</span> : <span>{fmtClock(v.start_ts)}</span>}{!compact && <span className="muted">{fmtClock(v.end_ts)}</span>}</div>
      <div className="tl-rail"><span className="tl-dot visit" title={v.status === 'suggested' ? 'Suggested: confirm or decline it' : undefined}>{v.place ? placeEmoji(v.place.category) : '📍'}</span></div>
      <div className="tl-body">
        <div className="tl-title">
          <span className="ellipsis">{shop && shopNamesVisit(v) ? <span title={`Named after the shop you paid at (Pocket Money)${v.name ? ` · ${v.name}` : ''}`}>{shop}</span> : v.name || <span className="muted">Unnamed place{v.place?.geocoded === 0 ? ' (naming…)' : ''}</span>}</span>
          {v.status === 'suggested' && !compact && <span className="badge">suggested</span>}
          {v.status === 'declined' && <span className="badge muted">declined</span>}
        </div>
        <div className="tl-sub">{shop && shopNamesVisit(v) && v.name && v.name !== shop ? `${v.name} · ` : ''}{fmtDur(dur)}{v.place?.city ? ` · ${v.place.city}` : ''}{v.place?.kind === 'saved' ? ' · saved place' : ''}{v.notes ? ` · ${v.notes}` : ''}</div>
        {paid?.map(p => (
          <a key={p.id} className="tl-paid" href={p.url} target="_blank" rel="noreferrer" onClick={e => e.stopPropagation()} title="Open in Pocket Money">
            <span>{p.category_icon || '🧾'}</span><span className="ellipsis">{shopOf(p)}</span><b>{fmtMoney(p.amount, p.currency)}</b><span className="muted">{p.time}</span>
          </a>
        ))}
      </div>
      {!readOnly && (
        <div className="tl-actions" onClick={e => e.stopPropagation()}>
          {v.status === 'suggested' && <>
            <button type="button" className="btn sm icon-only ghost ok" title="Confirm" aria-label="Confirm" onClick={() => set({ status: 'confirmed' })}><Icon name="check" size={16} /></button>
            <button type="button" className="btn sm icon-only ghost" title="Not a visit" aria-label="Decline" onClick={() => set({ status: 'declined' })}><Icon name="x" size={16} /></button>
          </>}
          <button type="button" className="btn sm icon-only ghost" aria-label="More" onClick={e => setMenu(menu ? null : rectOf(e.currentTarget))}><Icon name="more" size={16} /></button>
          {menu && <Menu anchor={menu} align="right" items={items} onClose={() => setMenu(null)} />}
        </div>
      )}
      {edit && <VisitEditor v={v} onClose={() => setEdit(false)} onSaved={() => { setEdit(false); onChanged?.() }} />}
    </li>
  )
}

function JourneyRow({ j, sel, onSelect, onChanged, compact, readOnly }: { j: Journey; sel: boolean; onSelect?: (s: { type: 'journey'; id: number }) => void; onChanged?: () => void; compact: boolean; readOnly: boolean }) {
  const { toast } = useToast()
  const { settings } = useApp()
  const [menu, setMenu] = useState<Anchor | null>(null)
  const m = modeOf(j.mode)
  const off = settings?.modes_off || []
  const setMode = async (mode: string) => {
    try { await api.patch(`${V1}/journeys/${j.id}`, { mode }); onChanged?.() } catch (e) { toast(errMsg(e), 'err') }
  }
  const items: MenuItem[] = [{ heading: 'How did you travel?' }, ...Object.entries(MODES).filter(([k]) => k !== 'unknown' && (!off.includes(k) || j.mode === k)).map(([k, x]) => ({ label: `${x.emoji}  ${x.label}`, checked: j.mode === k, onSelect: () => setMode(k) }))]
  return (
    <li className={`tl-row journey ${sel ? 'sel' : ''}`} onClick={() => onSelect?.({ type: 'journey', id: j.id })} style={{ '--mode': m.color } as React.CSSProperties} data-pocket-ask={`journey ${j.id}`}>
      <div className="tl-time"><span>{fmtClock(j.start_ts)}</span>{!compact && <span className="muted">{fmtClock(j.end_ts)}</span>}</div>
      <div className="tl-rail"><span className="tl-dot journey">{m.emoji}</span></div>
      <div className="tl-body">
        <div className="tl-title"><span>{m.label}</span><span className="tl-km">{fmtDist(j.distance)}</span>{j.mode_manual && <span className="muted small" title="You set this">✓</span>}</div>
        <div className="tl-sub">{fmtDur(j.end_ts - j.start_ts)} · avg {fmtSpeed(j.avg_speed)}{j.max_speed > j.avg_speed * 1.2 ? ` · max ${fmtSpeed(j.max_speed)}` : ''}{j.elevation_gain > 20 ? ` · ↑${j.elevation_gain} m` : ''}</div>
      </div>
      {!readOnly && (
        <div className="tl-actions" onClick={e => e.stopPropagation()}>
          <button type="button" className="btn sm ghost" title="Change the transport mode" onClick={e => setMenu(menu ? null : rectOf(e.currentTarget))}>{m.emoji}<Icon name="chevron-down" size={13} /></button>
          {menu && <Menu anchor={menu} align="right" items={items} onClose={() => setMenu(null)} width={210} />}
        </div>
      )}
    </li>
  )
}

interface PossiblePlace { id: number; name: string; lat: number; lng: number; kind: string; category: string; address: string; distance: number }

/** Rename a visit, pick the right place nearby, add a note. */
export function VisitEditor({ v, onClose, onSaved }: { v: Visit; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [name, setName] = useState(v.place?.name || v.name || '')
  const [onlyThis, setOnlyThis] = useState(false)
  const [notes, setNotes] = useState(v.notes)
  const [placeId, setPlaceId] = useState<number | null>(v.place_id)
  const [near, setNear] = useState<PossiblePlace[]>([])
  const [busy, setBusy] = useState(false)
  useEffect(() => { api.get<PossiblePlace[]>(`${V1}/visits/${v.id}/possible_places`).then(setNear).catch(() => {}) }, [v.id])
  const save = async () => {
    setBusy(true)
    try {
      const body: Record<string, unknown> = { notes, status: 'confirmed' }
      if (placeId !== v.place_id) body.place_id = placeId ?? 0
      if (name !== (v.place?.name || v.name)) { body.name = name; body.only_this = onlyThis }
      await api.put(`${V1}/visits/${v.id}`, body)
      onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title="Edit this visit" onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={busy} onClick={save}>Save</button>
    </>}>
      <div className="stack">
        <p className="muted small">{fmtClock(v.start_ts)}–{fmtClock(v.end_ts)} · {fmtDur(v.end_ts - v.start_ts)} · <Link to={`/timeline/${isoDateOf(v.start_ts)}`} onClick={onClose}>{isoDateOf(v.start_ts)}</Link></p>
        {near.length > 1 && (
          <Field label="Which place was it?">
            <div className="choice-list">
              {near.slice(0, 8).map(p => (
                <button key={p.id} type="button" className={`choice ${placeId === p.id ? 'on' : ''}`} onClick={() => { setPlaceId(p.id); setName(p.name) }}>
                  <span>{placeEmoji(p.category)}</span><span className="grow ellipsis">{p.name || 'Unnamed place'}</span><span className="muted small">{p.distance} m</span>
                </button>
              ))}
            </div>
          </Field>
        )}
        <Field label="Name" help={onlyThis ? 'Only this visit gets the name.' : 'Names the place, so every visit there shows it.'}>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="e.g. Grandma's, Café Tito" autoFocus />
        </Field>
        <label className="check-row"><input type="checkbox" checked={onlyThis} onChange={e => setOnlyThis(e.target.checked)} /> Just this visit</label>
        <Field label="Note"><input value={notes} onChange={e => setNotes(e.target.value)} placeholder="Optional" /></Field>
      </div>
    </Modal>
  )
}
