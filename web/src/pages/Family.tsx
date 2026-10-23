// Family & sharing: where the people who share with you are now (live), and
// what you share — with someone on this box, or as a link that expires.
import { useEffect, useMemo, useState } from 'react'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { agoTs, fmtStamp } from '../lib/geo'
import type { LivePerson, Share, SharedWithMe } from '../lib/types'
import { Icon } from '../components/icons'
import { MoveMap } from '../components/MoveMap'
import { Avatar, Card, Confirm, CopyField, Empty, Field, Loading, Modal, PageHeader, Segmented, errMsg, useAsync, useToast } from '../components/ui'
import '../styles/manage.css'

const PRECISIONS: { value: number; label: string }[] = [{ value: 0, label: 'Exact' }, { value: 1000, label: '~1 km' }, { value: 10000, label: '~10 km' }]
const EXPIRY: { value: number; label: string }[] = [{ value: 0, label: 'Never' }, { value: 1, label: '1 h' }, { value: 8, label: '8 h' }, { value: 24, label: '24 h' }, { value: 168, label: '7 days' }]

function expiresText(ts: number): string {
  if (!ts) return 'no end'
  const s = ts - Date.now() / 1000
  if (s <= 0) return 'expired'
  if (s < 3600) return `ends in ${Math.round(s / 60)} min`
  if (s < 86400 * 2) return `ends in ${Math.round(s / 3600)} h`
  return `until ${fmtStamp(ts)}`
}
const precisionText = (m: number) => m ? `rounded to ~${m >= 1000 ? `${m / 1000} km` : `${m} m`}` : 'exact position'

export default function Family() {
  return (
    <div className="mg-page">
      <PageHeader title="Family & sharing" sub="See where the people who share with you are, and choose who may see you — for how long and how precisely." />
      <SharingWithMe />
      <MyShares />
    </div>
  )
}

function SharingWithMe() {
  const { data, setData, loading } = useAsync(() => api.get<LivePerson[]>(`${V1}/family`), [])
  // Live positions as they arrive.
  useEffect(() => {
    if (!data?.length || typeof EventSource === 'undefined') return
    const es = new EventSource(`${V1}/live`)
    es.addEventListener('point', ev => {
      try {
        const p = JSON.parse((ev as MessageEvent).data) as { user_id: number; lat: number; lng: number; ts: number; battery?: number | null }
        setData(list => list ? list.map(x => x.user_id === p.user_id ? { ...x, place: null, point: { ...(x.point || { battery_state: '', accuracy: 0, country: '' }), lat: p.lat, lng: p.lng, ts: p.ts, battery: p.battery ?? x.point?.battery ?? null } } : x) : list)
      } catch { /* ignore a bad event */ }
    })
    return () => es.close()
  }, [data?.length, setData])
  // A slow refresh too, for places and anyone who just started sharing.
  useEffect(() => {
    const t = setInterval(() => { api.get<LivePerson[]>(`${V1}/family`).then(setData).catch(() => {}) }, 60000)
    return () => clearInterval(t)
  }, [setData])
  const people = data || []
  const located = people.filter(p => p.point)
  const fitKey = useMemo(() => located.map(p => p.user_id).join(','), [located])
  if (loading && !data) return <Loading />
  return (
    <Card title="Sharing with you" sub={people.length ? 'Updates live while this page is open.' : undefined}>
      {!people.length ? (
        <Empty title="Nobody shares with you yet" icon="users">When someone on this box shares their location with you, they appear here and on your map. OwnTracks shows them as friends too.</Empty>
      ) : (
        <div className="stack loose">
          {located.length > 0 && <div className="fam-map"><MoveMap people={people} layers={{ people: true, routes: false, visits: false, places: false }} fitKey={fitKey}
            bounds={located.length === 1 ? [located[0].point!.lat - 0.01, located[0].point!.lng - 0.01, located[0].point!.lat + 0.01, located[0].point!.lng + 0.01] : null} /></div>}
          <div>
            {people.map(p => (
              <div key={p.user_id} className="person">
                <Avatar name={p.name} email={p.email} size={38} />
                <div className="grow" style={{ minWidth: 0 }}>
                  <div className="row" style={{ gap: 6 }}><b className="ellipsis">{p.name}</b>{p.level === 'history' && <span className="badge">history</span>}</div>
                  <div className="meta">
                    {p.point ? <>
                      {p.place ? <>at <b>{p.place.name || 'a place'}</b> since {agoTs(p.place.since).replace(' ago', '')} · </> : null}
                      <span title={fmtStamp(p.point.ts)}>{agoTs(p.point.ts)}</span>
                      {p.point.battery != null && <> · 🔋 {p.point.battery}%{p.point.battery_state === 'charging' ? ' ⚡' : ''}</>}
                      {p.point.country && <> · {p.point.country}</>}
                    </> : 'no position yet'}
                  </div>
                  <div className="meta">{precisionText(p.precision_m)} · {expiresText(p.expires_at)}</div>
                </div>
                {p.point && <a className="btn ghost sm" href={`https://www.openstreetmap.org/?mlat=${p.point.lat}&mlon=${p.point.lng}#map=16/${p.point.lat}/${p.point.lng}`} target="_blank" rel="noreferrer" title="Open in OpenStreetMap"><Icon name="external" size={14} /></a>}
              </div>
            ))}
          </div>
        </div>
      )}
    </Card>
  )
}

function MyShares() {
  const { toast } = useToast()
  const { user, users } = useApp()
  const { data, reload } = useAsync(() => api.get<{ mine: Share[]; with_me: SharedWithMe[] }>(`${V1}/shares`), [])
  const [add, setAdd] = useState<'person' | 'link' | null>(null)
  const [edit, setEdit] = useState<Share | null>(null)
  const [stop, setStop] = useState<Share | null>(null)
  const others = users.filter(u => u.id !== user?.id)
  const mine = data?.mine || []
  const people = mine.filter(s => !s.link)
  const links = mine.filter(s => s.link)
  return (
    <Card title="What you share" sub="Shared views never show points inside your privacy zones (Settings → Privacy zones): the zone's centre is shown instead."
      actions={<>
        <button type="button" className="btn sm" onClick={() => setAdd('link')}><Icon name="link" size={14} />Make a link</button>
        <button type="button" className="btn primary sm" disabled={!others.length} title={others.length ? '' : 'Nobody else has an account here yet'} onClick={() => setAdd('person')}><Icon name="plus" size={14} />Share with someone</button>
      </>}>
      {!data ? <Loading /> : !mine.length ? (
        <p className="muted">You are not sharing your location. {others.length ? 'Share with someone on this box, or make a link for anyone.' : 'Make a link anyone can open, or add family under Settings → People.'}</p>
      ) : (
        <div>
          {people.map(s => <ShareRow key={s.id} s={s} onEdit={() => setEdit(s)} onStop={() => setStop(s)} />)}
          {links.map(s => <ShareRow key={s.id} s={s} onEdit={() => setEdit(s)} onStop={() => setStop(s)} />)}
        </div>
      )}
      {add && <ShareModal kind={add} onClose={() => setAdd(null)} onSaved={() => { setAdd(null); reload() }} />}
      {edit && <ShareModal kind={edit.link ? 'link' : 'person'} share={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload() }} />}
      {stop && <Confirm title={stop.link ? 'Stop this link?' : `Stop sharing with ${stop.viewer_name || stop.viewer_email}?`} confirmLabel="Stop sharing" message={stop.link ? 'Anyone with the link sees nothing from now on.' : 'They no longer see where you are.'}
        onCancel={() => setStop(null)} onConfirm={async () => { const s = stop; setStop(null); try { await api.del(`${V1}/shares/${s.id}`); toast('Stopped'); reload() } catch (e) { toast(errMsg(e), 'err') } }} />}
    </Card>
  )
}

function ShareRow({ s, onEdit, onStop }: { s: Share; onEdit: () => void; onStop: () => void }) {
  const { toast } = useToast()
  const share = async () => {
    if (!s.url) return
    if (navigator.share) { try { await navigator.share({ title: s.name || 'My location', url: s.url }) } catch { /* cancelled */ } }
  }
  return (
    <div className="share-row">
      {s.link ? <span className="share-ico"><Icon name="link" size={16} /></span> : <Avatar name={s.viewer_name} email={s.viewer_email} size={34} />}
      <div className="grow">
        <div className="row" style={{ gap: 6 }}>
          <b className="ellipsis">{s.link ? (s.name || 'Share link') : (s.viewer_name || s.viewer_email)}</b>
          <span className="badge">{s.level === 'history' ? 'live + today' : 'live'}</span>
          {s.expired && <span className="badge danger">expired</span>}
        </div>
        <div className="meta muted small">{precisionText(s.precision_m)} · {expiresText(s.expires_at)}</div>
        {s.link && s.url && !s.expired && <div className="mt" style={{ marginTop: 6 }}><CopyField value={s.url} label="Share link" /></div>}
      </div>
      <div className="row" style={{ gap: 4 }}>
        {s.link && s.url && typeof navigator.share === 'function' && !s.expired && <button type="button" className="btn ghost sm" onClick={share}><Icon name="share" size={14} />Send</button>}
        {s.link && s.url && !s.expired && <a className="btn ghost sm" href={s.url} target="_blank" rel="noreferrer" onClick={() => toast('Opening the public view')}><Icon name="external" size={14} /></a>}
        <button type="button" className="btn ghost sm" onClick={onEdit}>Edit</button>
        <button type="button" className="btn ghost sm danger-text" onClick={onStop}>Stop</button>
      </div>
    </div>
  )
}

function ShareModal({ kind, share, onClose, onSaved }: { kind: 'person' | 'link'; share?: Share; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const { user, users } = useApp()
  const others = users.filter(u => u.id !== user?.id)
  const [viewer, setViewer] = useState<number>(share?.viewer_id || others[0]?.id || 0)
  const [name, setName] = useState(share?.name || '')
  const [level, setLevel] = useState<'live' | 'history'>(share?.level || 'live')
  const [precision, setPrecision] = useState(share?.precision_m ?? 0)
  const [hours, setHours] = useState<number>(share ? -1 : kind === 'link' ? 24 : 0) // -1 = keep as it is
  const [busy, setBusy] = useState(false)
  const [made, setMade] = useState<Share | null>(null)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    const body: Record<string, unknown> = { name, level, precision_m: precision }
    if (hours >= 0) body.hours = hours
    try {
      if (share) { await api.put(`${V1}/shares/${share.id}`, body); toast('Saved'); onSaved(); return }
      if (kind === 'link') { const r = await api.post<Share>(`${V1}/shares`, { ...body, link: true }); setMade(r); return }
      await api.post(`${V1}/shares`, { ...body, viewer_id: viewer }); toast('Shared'); onSaved()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  if (made) return (
    <Modal title="Your link" onClose={onSaved} size="sm" footer={<button type="button" className="btn primary" onClick={onSaved}>Done</button>}>
      <div className="stack">
        <p className="mg-hint">Anyone with this link sees {precisionText(made.precision_m)}{made.level === 'history' ? ' and today’s route' : ''}, {expiresText(made.expires_at)}. The server is reachable on your tailnet only unless you expose it.</p>
        {made.url && <CopyField value={made.url} label="Share link" />}
        {made.url && typeof navigator.share === 'function' && <button type="button" className="btn" onClick={() => navigator.share({ title: made.name || 'My location', url: made.url }).catch(() => {})}><Icon name="share" size={15} />Send it</button>}
      </div>
    </Modal>
  )
  return (
    <Modal title={share ? 'Edit sharing' : kind === 'link' ? 'Make a share link' : 'Share your location'} onClose={onClose} size="sm">
      <form onSubmit={save} className="stack">
        {kind === 'person' && !share && <Field label="With">
          <select value={viewer} onChange={e => setViewer(Number(e.target.value))} required>{others.map(u => <option key={u.id} value={u.id}>{u.name || u.email}{u.name ? ` (${u.email})` : ''}</option>)}</select>
        </Field>}
        {kind === 'link' && <Field label="Name" help="Shown on the page, e.g. “On my way home”."><input value={name} onChange={e => setName(e.target.value)} maxLength={80} /></Field>}
        <Field group label="What they see"><Segmented value={level} onChange={setLevel} options={[{ value: 'live', label: 'Where I am' }, { value: 'history', label: 'Also my day' }]} /></Field>
        <Field group label="How precisely" help={precision ? 'They see a circle, not your street.' : 'Your exact position, with the place you are at.'}><Segmented value={precision} onChange={setPrecision} options={PRECISIONS} /></Field>
        <Field group label={share ? 'Ends' : 'For'} help={share ? `Now: ${expiresText(share.expires_at)}` : undefined}>
          <Segmented value={hours} onChange={setHours} options={[...(share ? [{ value: -1, label: 'As is' }] : []), ...EXPIRY]} />
        </Field>
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy || (kind === 'person' && !share && !viewer)}>{share ? 'Save' : kind === 'link' ? 'Make the link' : 'Share'}</button></div>
      </form>
    </Modal>
  )
}
