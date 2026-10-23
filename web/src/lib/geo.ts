// Map helpers: polylines, transport modes, speed colours, distances and times
// in the person's own time zone.

/** Decodes a Google encoded polyline (precision 5) into [lat, lng] pairs. */
export function decodePolyline(s: string): [number, number][] {
  const out: [number, number][] = []
  let lat = 0, lng = 0, i = 0
  while (i < s.length) {
    for (let k = 0; k < 2; k++) {
      let res = 0, shift = 0, c: number
      do { c = s.charCodeAt(i++) - 63; res |= (c & 0x1f) << shift; shift += 5 } while (c >= 0x20 && i < s.length)
      const d = res & 1 ? ~(res >> 1) : res >> 1
      if (k === 0) lat += d; else lng += d
    }
    out.push([lat / 1e5, lng / 1e5])
  }
  return out
}

export const MODES: Record<string, { label: string; icon: string; color: string; emoji: string }> = {
  walking: { label: 'Walking', icon: 'walk', color: '#16a34a', emoji: '🚶' },
  running: { label: 'Running', icon: 'run', color: '#ea580c', emoji: '🏃' },
  cycling: { label: 'Cycling', icon: 'bike', color: '#0891b2', emoji: '🚲' },
  driving: { label: 'Driving', icon: 'car', color: '#2563eb', emoji: '🚗' },
  bus: { label: 'Bus', icon: 'bus', color: '#7c3aed', emoji: '🚌' },
  train: { label: 'Train', icon: 'train', color: '#c026d3', emoji: '🚆' },
  boat: { label: 'Boat', icon: 'boat', color: '#0e7490', emoji: '⛴️' },
  flying: { label: 'Flying', icon: 'plane', color: '#dc2626', emoji: '✈️' },
  unknown: { label: 'Moving', icon: 'route', color: '#64748b', emoji: '•' },
}
export const modeOf = (m: string) => MODES[m] || MODES.unknown

/** Speed (km/h) to a colour from slow green through amber to fast red. */
export function speedColor(kmh: number): string {
  const stops: [number, [number, number, number]][] = [[0, [22, 163, 74]], [8, [101, 163, 13]], [25, [202, 138, 4]], [60, [234, 88, 12]], [110, [220, 38, 38]], [250, [124, 58, 237]]]
  if (kmh <= 0) return rgb(stops[0][1])
  for (let i = 1; i < stops.length; i++) {
    if (kmh <= stops[i][0]) {
      const [a, ca] = stops[i - 1], [b, cb] = stops[i]
      const f = (kmh - a) / (b - a)
      return rgb([0, 1, 2].map(j => Math.round(ca[j] + (cb[j] - ca[j]) * f)) as [number, number, number])
    }
  }
  return rgb(stops[stops.length - 1][1])
}
const rgb = (c: [number, number, number]) => `rgb(${c[0]},${c[1]},${c[2]})`

export function haversine(a: [number, number], b: [number, number]): number {
  const R = 6371008.8, r = Math.PI / 180
  const dLat = (b[0] - a[0]) * r, dLng = (b[1] - a[1]) * r
  const h = Math.sin(dLat / 2) ** 2 + Math.cos(a[0] * r) * Math.cos(b[0] * r) * Math.sin(dLng / 2) ** 2
  return 2 * R * Math.atan2(Math.sqrt(h), Math.sqrt(1 - h))
}

// ---- units ----
let UNIT: 'km' | 'mi' = 'km'
let TZ = Intl.DateTimeFormat().resolvedOptions().timeZone
export function setUnits(unit: 'km' | 'mi', tz: string) { UNIT = unit; if (tz) TZ = tz }
export const currentTZ = () => TZ
export const currentUnit = () => UNIT

/** Metres to "12.4 km", "850 m", "7.7 mi". */
export function fmtDist(m: number, unit = UNIT): string {
  if (!m || m < 0) return unit === 'mi' ? '0 mi' : '0 km'
  if (unit === 'mi') { const mi = m / 1609.344; return mi < 0.2 ? `${Math.round(m * 1.09361)} yd` : `${mi < 10 ? mi.toFixed(1) : Math.round(mi).toLocaleString()} mi` }
  if (m < 1000) return `${Math.round(m)} m`
  const km = m / 1000
  return `${km < 10 ? km.toFixed(1) : Math.round(km).toLocaleString()} km`
}
/** km/h to the unit in use. */
export function fmtSpeed(kmh: number, unit = UNIT): string {
  return unit === 'mi' ? `${Math.round(kmh / 1.609344)} mph` : `${Math.round(kmh)} km/h`
}
/** Seconds to "2 h 15 min", "45 min", "3 d 4 h". */
export function fmtDur(s: number): string {
  s = Math.max(0, Math.round(s))
  if (s < 60) return `${s} s`
  const m = Math.round(s / 60)
  if (m < 60) return `${m} min`
  const h = Math.floor(m / 60), mm = m % 60
  if (h < 48) return mm ? `${h} h ${mm} min` : `${h} h`
  const d = Math.floor(h / 24), hh = h % 24
  return hh ? `${d} d ${hh} h` : `${d} d`
}
/** Hours, rounded, for big totals: "1,240 h". */
export function fmtHours(s: number): string { const h = s / 3600; return h < 10 ? `${h.toFixed(1)} h` : `${Math.round(h).toLocaleString()} h` }

const fmtCache = new Map<string, Intl.DateTimeFormat>()
function dtf(opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const key = TZ + JSON.stringify(opts)
  let f = fmtCache.get(key)
  if (!f) { f = new Intl.DateTimeFormat(undefined, { ...opts, timeZone: TZ }); fmtCache.set(key, f) }
  return f
}
/** 17:05 in the person's zone. */
export const fmtClock = (ts: number) => dtf({ hour: 'numeric', minute: '2-digit' }).format(ts * 1000)
/** Tue 21 Oct */
export const fmtDayShort = (ts: number) => dtf({ weekday: 'short', day: 'numeric', month: 'short' }).format(ts * 1000)
/** 21 Oct 2026, 17:05 */
export const fmtStamp = (ts: number) => dtf({ day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' }).format(ts * 1000)
/** 21 Oct 2026 */
export const fmtDateTs = (ts: number) => dtf({ day: 'numeric', month: 'short', year: 'numeric' }).format(ts * 1000)

/** YYYY-MM-DD of an instant in the person's zone. */
export function isoDateOf(ts: number): string {
  const p = dtf({ year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(ts * 1000)
  const g = (t: string) => p.find(x => x.type === t)?.value || ''
  return `${g('year')}-${g('month')}-${g('day')}`
}
export const todayIn = () => isoDateOf(Date.now() / 1000)

/** "Tuesday, 21 October 2026" for a YYYY-MM-DD (no zone maths needed). */
export function fmtLongDate(iso: string): string {
  const [y, m, d] = iso.split('-').map(Number)
  return new Date(Date.UTC(y, m - 1, d, 12)).toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC' })
}
export function fmtShortDate(iso: string, withYear = false): string {
  const [y, m, d] = iso.split('-').map(Number)
  return new Date(Date.UTC(y, m - 1, d, 12)).toLocaleDateString(undefined, { day: 'numeric', month: 'short', ...(withYear ? { year: 'numeric' } : {}), timeZone: 'UTC' })
}
export function addDaysISO(iso: string, n: number): string {
  const [y, m, d] = iso.split('-').map(Number)
  const t = new Date(Date.UTC(y, m - 1, d + n, 12))
  return t.toISOString().slice(0, 10)
}
export function monthName(m: string): string {
  const [y, mm] = m.split('-').map(Number)
  return new Date(Date.UTC(y, mm - 1, 15)).toLocaleDateString(undefined, { month: 'short', timeZone: 'UTC' })
}

/** "5 min ago", "3 h ago", "2 d ago". */
export function agoTs(ts: number): string {
  if (!ts) return 'never'
  const s = Date.now() / 1000 - ts
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.round(s / 60)} min ago`
  if (s < 86400) return `${Math.round(s / 3600)} h ago`
  if (s < 86400 * 60) return `${Math.round(s / 86400)} d ago`
  return fmtDateTs(ts)
}

/** 🇧🇦 for "BA". */
export function flag(code: string): string {
  if (!code || code.length !== 2) return '🏳️'
  return String.fromCodePoint(...[...code.toUpperCase()].map(c => 0x1f1a5 + c.charCodeAt(0)))
}

export const PLACE_CATEGORIES: { id: string; label: string; emoji: string }[] = [
  { id: 'home', label: 'Home', emoji: '🏠' }, { id: 'work', label: 'Work', emoji: '💼' }, { id: 'school', label: 'School', emoji: '🎓' },
  { id: 'family', label: 'Family & friends', emoji: '👪' }, { id: 'food', label: 'Food & drink', emoji: '🍽️' }, { id: 'shop', label: 'Shopping', emoji: '🛒' },
  { id: 'sport', label: 'Sport', emoji: '🏋️' }, { id: 'health', label: 'Health', emoji: '🩺' }, { id: 'travel', label: 'Travel', emoji: '🧳' },
  { id: 'leisure', label: 'Leisure', emoji: '🎭' }, { id: 'nature', label: 'Nature', emoji: '🌲' }, { id: 'other', label: 'Other', emoji: '📍' },
]
export function placeEmoji(cat: string): string { return PLACE_CATEGORIES.find(c => c.id === cat)?.emoji || '📍' }
