// The movement map: journeys (coloured by speed or mode), the raw line of
// points not yet analysed, visits and saved places, every point, a heat map,
// the fog of war, people who share with you and where you are now.
import { useEffect, useRef } from 'react'
import L from 'leaflet'
import 'leaflet/dist/leaflet.css'
import { useApp } from '../lib/app-context'
import { decodePolyline, fmtClock, fmtDist, fmtDur, modeOf, placeEmoji, speedColor } from '../lib/geo'
import type { Journey, LivePerson, Place, Visit } from '../lib/types'
import { type Purchase, fmtMoney, purchasesByVisit, shopNamesVisit, shopOf } from '../lib/purchases'

const OSM_ATTR = '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors'
const ESRI_ATTR = 'Tiles &copy; Esri'
// All free and keyless (CARTO's basemaps now want an API key).
export const TILES: Record<string, { label: string; url: string; attr: string; overlay?: string; maxZoom?: number; native?: number; dark?: boolean }> = {
  osm: { label: 'OpenStreetMap', url: 'https://tile.openstreetmap.org/{z}/{x}/{y}.png', attr: OSM_ATTR },
  'osm-de': { label: 'OSM Germany (softer colours)', url: 'https://tile.openstreetmap.de/{z}/{x}/{y}.png', attr: OSM_ATTR },
  'osm-fr': { label: 'OSM France (detailed)', url: 'https://{s}.tile.openstreetmap.fr/osmfr/{z}/{x}/{y}.png', attr: OSM_ATTR + ', <a href="https://www.openstreetmap.fr/">OSM France</a>', maxZoom: 20 },
  hot: { label: 'OSM Humanitarian (clean, good labels)', url: 'https://{s}.tile.openstreetmap.fr/hot/{z}/{x}/{y}.png', attr: OSM_ATTR + ', style <a href="https://www.hotosm.org/">HOT</a>' },
  cyclosm: { label: 'CyclOSM (paths, trails, terrain)', url: 'https://{s}.tile-cyclosm.openstreetmap.fr/cyclosm/{z}/{x}/{y}.png', attr: OSM_ATTR + ', <a href="https://www.cyclosm.org/">CyclOSM</a>' },
  topo: { label: 'OpenTopoMap (hiking)', url: 'https://{s}.tile.opentopomap.org/{z}/{x}/{y}.png', attr: OSM_ATTR + ', <a href="https://opentopomap.org">OpenTopoMap</a>', maxZoom: 17 },
  opnv: { label: 'ÖPNVKarte (public transport)', url: 'https://tileserver.memomaps.de/tilegen/{z}/{x}/{y}.png', attr: OSM_ATTR + ', <a href="https://memomaps.de/">memomaps.de</a>', maxZoom: 18 },
  'esri-gray': { label: 'Light grey (Esri, quiet)', url: 'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Light_Gray_Base/MapServer/tile/{z}/{y}/{x}', attr: ESRI_ATTR, overlay: 'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Light_Gray_Reference/MapServer/tile/{z}/{y}/{x}', native: 16 },
  'esri-dark': { label: 'Dark grey (Esri)', url: 'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Dark_Gray_Base/MapServer/tile/{z}/{y}/{x}', attr: ESRI_ATTR, overlay: 'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Dark_Gray_Reference/MapServer/tile/{z}/{y}/{x}', native: 16, dark: true },
  'esri-topo': { label: 'Esri World Topo', url: 'https://server.arcgisonline.com/ArcGIS/rest/services/World_Topo_Map/MapServer/tile/{z}/{y}/{x}', attr: ESRI_ATTR },
  'esri-streets': { label: 'Esri World Street', url: 'https://server.arcgisonline.com/ArcGIS/rest/services/World_Street_Map/MapServer/tile/{z}/{y}/{x}', attr: ESRI_ATTR },
  'esri-satellite': { label: 'Esri Satellite (with labels)', url: 'https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}', attr: ESRI_ATTR + ', Maxar, Earthstar Geographics', overlay: 'https://server.arcgisonline.com/ArcGIS/rest/services/Reference/World_Boundaries_and_Places/MapServer/tile/{z}/{y}/{x}', dark: true },
}

// Styles that were removed, and what replaces them.
export const TILE_ALIASES: Record<string, string> = { 'carto-light': 'esri-gray', 'carto-voyager': 'osm-de', 'carto-dark': 'esri-dark' }
export const tileKey = (k?: string) => (k && TILES[k] ? k : (k && TILE_ALIASES[k]) || 'osm')

export type PointRow = [number, number, number, number, number | null, number | null, number | null, number]
export interface Layers { routes: boolean; points: boolean; heat: boolean; fog: boolean; visits: boolean; places: boolean; people: boolean; purchases: boolean }
export const DEFAULT_LAYERS: Layers = { routes: true, points: false, heat: false, fog: false, visits: true, places: true, people: true, purchases: true }

export interface MoveMapProps {
  tracks?: Journey[]
  raw?: string
  visits?: Visit[]
  places?: Place[]
  purchases?: Purchase[]
  points?: PointRow[]
  heat?: [number, number, number][]
  fogCells?: [number, number, number][]
  fogCell?: number
  people?: LivePerson[]
  me?: { lat: number; lng: number; accuracy?: number | null; ts: number } | null
  layers?: Partial<Layers>
  colorBy?: 'speed' | 'mode' | 'plain'
  selected?: { type: 'journey' | 'visit'; id: number } | null
  detail?: { lat: number; lng: number; speed: number | null }[] // the selected journey, point by point
  onSelect?: (s: { type: 'journey' | 'visit'; id: number } | null) => void
  onMapClick?: (lat: number, lng: number) => void
  fitKey?: string
  bounds?: [number, number, number, number] | null
  className?: string
  hover?: [number, number] | null
}

export function MoveMap(props: MoveMapProps) {
  const ref = useRef<HTMLDivElement>(null)
  const map = useRef<L.Map | null>(null)
  const groups = useRef<Record<string, L.LayerGroup>>({})
  const canvas = useRef<L.Canvas | null>(null)
  const heatCv = useRef<HTMLCanvasElement | null>(null)
  const fogCv = useRef<HTMLCanvasElement | null>(null)
  const lastFit = useRef('')
  const propsRef = useRef(props)
  propsRef.current = props
  const { settings, theme } = useApp()
  const tiles = TILES[tileKey(settings?.map_tiles)]
  const layers = { ...DEFAULT_LAYERS, ...props.layers }

  // ---- create once ----
  useEffect(() => {
    if (!ref.current || map.current) return
    const m = L.map(ref.current, { zoomControl: false, attributionControl: true, preferCanvas: false, worldCopyJump: true })
    L.control.zoom({ position: 'bottomright' }).addTo(m)
    m.setView([44, 17], 5)
    canvas.current = L.canvas({ padding: 0.3 })
    for (const g of ['fogpane', 'heat', 'places', 'raw', 'routes', 'detail', 'points', 'visits', 'purchases', 'people', 'me', 'hover']) groups.current[g] = L.layerGroup().addTo(m)
    // Canvas overlays for the heat map and the fog, redrawn as the map moves.
    // They live in panes inside the map, above the tiles (200) and under the
    // lines and markers (400): appended to the container they sat beneath
    // Leaflet's map pane (z 400) and never showed.
    const mk = (name: string, z: number) => {
      const pane = m.createPane(name)
      pane.style.zIndex = String(z)
      pane.style.pointerEvents = 'none'
      const c = document.createElement('canvas')
      c.className = 'mm-overlay'
      pane.appendChild(c)
      return c
    }
    heatCv.current = mk('mm-heat', 350)
    fogCv.current = mk('mm-fog', 360)
    const redraw = () => { drawHeat(m, heatCv.current!, propsRef.current); drawFog(m, fogCv.current!, propsRef.current) }
    m.on('move zoom resize viewreset', redraw)
    m.on('click', e => {
      const p = propsRef.current
      if (p.onMapClick) p.onMapClick(e.latlng.lat, e.latlng.lng)
      else p.onSelect?.(null)
    })
    map.current = m
    const ro = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(() => { m.invalidateSize(); redraw() }) : null
    ro?.observe(ref.current)
    return () => { ro?.disconnect(); m.remove(); map.current = null }
  }, [])

  // ---- tiles ----
  const tileLayer = useRef<L.TileLayer[]>([])
  useEffect(() => {
    const m = map.current
    if (!m) return
    tileLayer.current.forEach(t => t.remove())
    const zoom = { maxZoom: tiles.native ? 19 : tiles.maxZoom || 19, maxNativeZoom: tiles.native || tiles.maxZoom || 19 }
    const base = L.tileLayer(tiles.url, { attribution: tiles.attr, ...zoom, className: theme === 'dark' && !tiles.dark ? 'tiles-dim' : '' }).addTo(m)
    tileLayer.current = [base]
    if (tiles.overlay) tileLayer.current.push(L.tileLayer(tiles.overlay, zoom).addTo(m))
  }, [tiles.url, tiles.overlay, tiles.attr, tiles.maxZoom, tiles.native, tiles.dark, theme])

  // ---- routes ----
  useEffect(() => {
    const g = groups.current.routes, raw = groups.current.raw
    if (!g) return
    g.clearLayers(); raw.clearLayers()
    if (!layers.routes) return
    const sel = props.selected
    // A journey leaves from the visit before it and arrives at the one after:
    // its line runs to the visit's centre, not just to its own first and last
    // points (those lie somewhere inside the visit's circle).
    const visits = (props.visits || []).filter(v => v.status !== 'declined')
    const near = (a: [number, number], v: Visit) => L.latLng(a).distanceTo([v.lat, v.lng]) <= Math.max(v.radius * 2, 300)
    for (const t of props.tracks || []) {
      const pts = decodePolyline(t.polyline)
      if (pts.length < 2) continue
      const from = visits.find(v => Math.abs(v.end_ts - t.start_ts) <= 600 && near(pts[0], v))
      const to = visits.find(v => Math.abs(v.start_ts - t.end_ts) <= 600 && near(pts[pts.length - 1], v))
      if (from) pts.unshift([from.lat, from.lng])
      if (to) pts.push([to.lat, to.lng])
      const isSel = sel?.type === 'journey' && sel.id === t.id
      const color = props.colorBy === 'mode' ? modeOf(t.mode).color : props.colorBy === 'plain' ? cssVar('--accent', '#4d7c0f') : speedColor(t.avg_speed)
      const dim = !!sel && !isSel
      // A soft halo under each line keeps it readable on any tiles.
      L.polyline(pts, { color: '#fff', weight: isSel ? 9 : 6, opacity: dim ? .25 : .7, interactive: false, lineJoin: 'round', lineCap: 'round' }).addTo(g)
      const line = L.polyline(pts, { color: t.mode === 'flying' ? modeOf('flying').color : color, weight: isSel ? 6 : 4, opacity: dim ? .35 : .95, dashArray: t.mode === 'flying' ? '2 8' : undefined, lineJoin: 'round', lineCap: 'round' }).addTo(g)
      line.bindTooltip(`${modeOf(t.mode).emoji} ${modeOf(t.mode).label} · ${fmtDist(t.distance)} · ${fmtDur(t.end_ts - t.start_ts)}<br><span class="muted">${fmtClock(t.start_ts)}–${fmtClock(t.end_ts)} · avg ${Math.round(t.avg_speed)} km/h</span>`, { sticky: true })
      line.on('click', e => { L.DomEvent.stopPropagation(e); propsRef.current.onSelect?.({ type: 'journey', id: t.id }) })
    }
    if (props.raw) {
      const pts = decodePolyline(props.raw)
      if (pts.length > 1) L.polyline(pts, { color: '#64748b', weight: 3, opacity: .8, dashArray: '4 6', lineJoin: 'round' }).bindTooltip('Recorded, not yet sorted into journeys', { sticky: true }).addTo(raw)
    }
  }, [props.tracks, props.raw, props.visits, props.selected, props.colorBy, layers.routes])

  // ---- the selected journey, speed by speed ----
  useEffect(() => {
    const g = groups.current.detail
    if (!g) return
    g.clearLayers()
    const d = props.detail
    if (!d || d.length < 2 || !layers.routes) return
    for (let i = 1; i < d.length; i++) {
      const sp = d[i].speed ?? d[i - 1].speed ?? 0
      L.polyline([[d[i - 1].lat, d[i - 1].lng], [d[i].lat, d[i].lng]], { color: speedColor(sp), weight: 7, opacity: 1, lineCap: 'round', interactive: false }).addTo(g)
    }
  }, [props.detail, layers.routes])

  // ---- visits and places ----
  useEffect(() => {
    const g = groups.current.visits
    if (!g) return
    g.clearLayers()
    if (!layers.visits) return
    const paid = purchasesByVisit(props.visits || [], props.purchases || [])
    for (const v of props.visits || []) {
      if (v.status === 'declined') continue
      const isSel = props.selected?.type === 'visit' && props.selected.id === v.id
      const mins = (v.end_ts - v.start_ts) / 60
      const r = Math.max(6, Math.min(16, 5 + Math.log2(1 + mins / 10) * 2.2))
      const emoji = v.place ? placeEmoji(v.place.category) : '📍'
      const icon = L.divIcon({ className: '', html: `<div class="mm-visit ${v.status} ${isSel ? 'sel' : ''}" style="--r:${r}px">${v.place?.category ? emoji : ''}</div>`, iconSize: [r * 2, r * 2], iconAnchor: [r, r] })
      const mk = L.marker([v.lat, v.lng], { icon, riseOnHover: true, zIndexOffset: isSel ? 1000 : 0 }).addTo(g)
      const bought = paid.get(v.id) || []
      const paidLine = bought.map(p => `<br>${p.category_icon || '🧾'} ${esc(shopOf(p))} · <b>${fmtMoney(p.amount, p.currency)}</b> · ${p.time}`).join('')
      mk.bindTooltip(`<b>${esc(bought.length && shopNamesVisit(v) ? shopOf(bought[0]) : v.name || 'Unnamed place')}</b><br>${fmtClock(v.start_ts)}–${fmtClock(v.end_ts)} · ${fmtDur(v.end_ts - v.start_ts)}${paidLine}${v.status === 'suggested' ? '<br><span class="muted">suggested — confirm it in the timeline</span>' : ''}`, { direction: 'top', offset: [0, -r] })
      mk.on('click', e => { L.DomEvent.stopPropagation(e); propsRef.current.onSelect?.({ type: 'visit', id: v.id }) })
    }
  }, [props.visits, props.selected, props.purchases, layers.visits])

  // ---- where you paid (Pocket Money) ----
  useEffect(() => {
    const g = groups.current.purchases
    if (!g) return
    g.clearLayers()
    if (!layers.purchases) return
    for (const p of props.purchases || []) {
      const icon = L.divIcon({ className: '', html: `<div class="mm-paid">${p.category_icon || '🧾'}</div>`, iconSize: [24, 24], iconAnchor: [12, 24] })
      const mk = L.marker([p.lat, p.lng], { icon, riseOnHover: true, zIndexOffset: 500 }).addTo(g)
      mk.bindTooltip(`<b>${esc(shopOf(p))}</b> · ${fmtMoney(p.amount, p.currency)}<br><span class="muted">${p.date}${p.time ? ' ' + p.time : ''}${p.category ? ' · ' + esc(p.category) : ''}${p.items ? ` · ${p.items} items` : ''}</span>`, { direction: 'top', offset: [0, -24] })
      mk.on('click', e => { L.DomEvent.stopPropagation(e); window.open(p.url, '_blank', 'noopener') })
    }
  }, [props.purchases, layers.purchases])

  useEffect(() => {
    const g = groups.current.places
    if (!g) return
    g.clearLayers()
    if (!layers.places) return
    for (const p of props.places || []) {
      if (p.kind !== 'saved' || p.hidden) continue
      L.circle([p.lat, p.lng], { radius: p.radius, color: cssVar('--accent', '#4d7c0f'), weight: 1.5, fillOpacity: .07, dashArray: '4 4', interactive: false }).addTo(g)
      L.marker([p.lat, p.lng], { icon: L.divIcon({ className: '', html: `<div class="mm-place" title="${esc(p.name)}">${placeEmoji(p.category)}</div>`, iconSize: [26, 26], iconAnchor: [13, 13] }), interactive: false, keyboard: false }).addTo(g)
    }
  }, [props.places, layers.places])

  // ---- points ----
  useEffect(() => {
    const g = groups.current.points
    if (!g) return
    g.clearLayers()
    if (!layers.points || !props.points) return
    for (const p of props.points) {
      const out = p[7] === 1
      const c = L.circleMarker([p[1], p[2]], { renderer: canvas.current!, radius: 3.2, weight: 1, color: out ? '#dc2626' : '#fff', fillColor: out ? '#fecaca' : p[4] != null ? speedColor(p[4]) : '#475569', fillOpacity: .95 })
      c.bindTooltip(() => `${fmtClock(p[3])}${p[4] != null ? ` · ${Math.round(p[4])} km/h` : ''}${p[5] != null ? ` · ±${p[5]} m` : ''}${p[6] != null ? ` · ${p[6]} m alt` : ''}${out ? '<br><b>outlier</b> (left out of routes)' : ''}`)
      c.addTo(g)
    }
  }, [props.points, layers.points])

  // ---- heat and fog redraw ----
  useEffect(() => {
    const m = map.current
    if (!m) return
    drawHeat(m, heatCv.current!, propsRef.current)
    drawFog(m, fogCv.current!, propsRef.current)
  }, [props.heat, props.fogCells, layers.heat, layers.fog, props.fogCell, theme])

  // ---- people and me ----
  useEffect(() => {
    const g = groups.current.people
    if (!g) return
    g.clearLayers()
    if (!layers.people) return
    for (const p of props.people || []) {
      if (!p.point) continue
      const ini = (p.name || '?').split(/\s+/).map(w => w[0]).join('').slice(0, 2).toUpperCase()
      if (p.precision_m > 0) L.circle([p.point.lat, p.point.lng], { radius: p.precision_m, color: '#7c3aed', weight: 1, fillOpacity: .06, interactive: false }).addTo(g)
      L.marker([p.point.lat, p.point.lng], { icon: L.divIcon({ className: '', html: `<div class="mm-person"><span>${esc(ini)}</span></div>`, iconSize: [34, 34], iconAnchor: [17, 17] }), zIndexOffset: 2000 })
        .bindTooltip(`<b>${esc(p.name)}</b>${p.place ? `<br>at ${esc(p.place.name)}` : ''}<br><span class="muted">${fmtClock(p.point.ts)}${p.point.battery != null ? ` · 🔋 ${p.point.battery}%` : ''}</span>`, { direction: 'top', offset: [0, -16] })
        .addTo(g)
    }
  }, [props.people, layers.people])

  useEffect(() => {
    const g = groups.current.me
    if (!g) return
    g.clearLayers()
    const me = props.me
    if (!me) return
    if (me.accuracy && me.accuracy > 15) L.circle([me.lat, me.lng], { radius: me.accuracy, color: '#2563eb', weight: 1, fillOpacity: .08, interactive: false }).addTo(g)
    L.marker([me.lat, me.lng], { icon: L.divIcon({ className: '', html: '<div class="mm-me"><span></span></div>', iconSize: [22, 22], iconAnchor: [11, 11] }), zIndexOffset: 3000, interactive: false }).addTo(g)
  }, [props.me])

  useEffect(() => {
    const g = groups.current.hover
    if (!g) return
    g.clearLayers()
    if (props.hover) L.circleMarker(props.hover, { radius: 7, color: '#fff', weight: 3, fillColor: '#111827', fillOpacity: 1, interactive: false }).addTo(g)
  }, [props.hover])

  // ---- fit ----
  useEffect(() => {
    const m = map.current
    if (!m) return
    const key = props.fitKey ?? ''
    if (key === lastFit.current) return
    const b = props.bounds
    let pts: [number, number][] = []
    if (b) pts = [[b[0], b[1]], [b[2], b[3]]]
    else {
      for (const t of props.tracks || []) { const d = decodePolyline(t.polyline); if (d.length) pts.push(d[0], d[d.length - 1]) }
      for (const v of props.visits || []) pts.push([v.lat, v.lng])
      if (props.raw) pts.push(...decodePolyline(props.raw))
      for (const h of props.heat || []) pts.push([h[0], h[1]])
      if (!pts.length && props.me) pts.push([props.me.lat, props.me.lng])
    }
    if (!pts.length) return
    lastFit.current = key
    const lb = L.latLngBounds(pts)
    if (lb.getNorthEast().distanceTo(lb.getSouthWest()) < 150) m.setView(lb.getCenter(), 16)
    else m.fitBounds(lb, { padding: [40, 40], maxZoom: 16 })
  }, [props.fitKey, props.bounds, props.tracks, props.visits, props.raw, props.heat, props.me])

  // Fly to the selection.
  useEffect(() => {
    const m = map.current, s = props.selected
    if (!m || !s) return
    if (s.type === 'visit') {
      const v = props.visits?.find(x => x.id === s.id)
      if (v && !m.getBounds().pad(-0.15).contains([v.lat, v.lng])) m.panTo([v.lat, v.lng])
    } else {
      const t = props.tracks?.find(x => x.id === s.id)
      if (t) { const pts = decodePolyline(t.polyline); if (pts.length > 1) m.fitBounds(L.latLngBounds(pts), { padding: [60, 60], maxZoom: 16 }) }
    }
  }, [props.selected]) // eslint-disable-line react-hooks/exhaustive-deps

  return <div ref={ref} className={`mm ${props.className || ''}`} />
}

/** A CSS custom property's value (Leaflet writes SVG attributes, where var() does not work). */
export function cssVar(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || fallback
}

function esc(s: string) { return s.replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]!)) }

function sizeCanvas(m: L.Map, c: HTMLCanvasElement) {
  const s = m.getSize(), dpr = window.devicePixelRatio || 1
  // The pane moves with the map; keep the canvas over the visible area.
  L.DomUtil.setPosition(c, m.containerPointToLayerPoint([0, 0]))
  if (c.width !== s.x * dpr || c.height !== s.y * dpr) { c.width = s.x * dpr; c.height = s.y * dpr; c.style.width = s.x + 'px'; c.style.height = s.y + 'px' }
  const ctx = c.getContext('2d')!
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, s.x, s.y)
  return { ctx, w: s.x, h: s.y }
}

function metresToPx(m: L.Map, lat: number, metres: number): number {
  const p1 = m.latLngToContainerPoint([lat, 0]), p2 = m.latLngToContainerPoint([lat + metres / 110540, 0])
  return Math.abs(p1.y - p2.y)
}

// ---- heat map: blurred circles into an alpha mask, coloured by a gradient ----
let GRADIENT: Uint8ClampedArray | null = null
function gradient(): Uint8ClampedArray {
  if (GRADIENT) return GRADIENT
  const c = document.createElement('canvas'); c.width = 1; c.height = 256
  const ctx = c.getContext('2d')!
  const g = ctx.createLinearGradient(0, 0, 0, 256)
  g.addColorStop(0.15, '#2563eb'); g.addColorStop(0.4, '#06b6d4'); g.addColorStop(0.6, '#84cc16'); g.addColorStop(0.8, '#facc15'); g.addColorStop(1, '#dc2626')
  ctx.fillStyle = g; ctx.fillRect(0, 0, 1, 256)
  GRADIENT = ctx.getImageData(0, 0, 1, 256).data
  return GRADIENT
}

function drawHeat(m: L.Map, c: HTMLCanvasElement, p: MoveMapProps) {
  const on = !!p.layers?.heat && !!p.heat?.length
  c.style.display = on ? '' : 'none'
  if (!on) return
  const { ctx, w, h } = sizeCanvas(m, c)
  const zoom = m.getZoom()
  const radius = Math.max(6, Math.min(28, 4 + zoom * 1.4))
  const max = Math.max(1, ...p.heat!.map(x => x[2]).sort((a, b) => b - a).slice(0, Math.ceil(p.heat!.length * 0.02) + 1).slice(-1))
  ctx.globalCompositeOperation = 'source-over'
  for (const [lat, lng, n] of p.heat!) {
    const pt = m.latLngToContainerPoint([lat, lng])
    if (pt.x < -radius || pt.y < -radius || pt.x > w + radius || pt.y > h + radius) continue
    const a = Math.min(1, 0.08 + Math.sqrt(n / max) * 0.6)
    const g = ctx.createRadialGradient(pt.x, pt.y, 0, pt.x, pt.y, radius)
    g.addColorStop(0, `rgba(0,0,0,${a})`); g.addColorStop(1, 'rgba(0,0,0,0)')
    ctx.fillStyle = g
    ctx.fillRect(pt.x - radius, pt.y - radius, radius * 2, radius * 2)
  }
  const dpr = window.devicePixelRatio || 1
  const img = ctx.getImageData(0, 0, w * dpr, h * dpr)
  const px = img.data, grad = gradient()
  for (let i = 3; i < px.length; i += 4) {
    const a = px[i]
    if (!a) continue
    const j = a * 4
    px[i - 3] = grad[j]; px[i - 2] = grad[j + 1]; px[i - 1] = grad[j + 2]; px[i] = Math.min(235, a * 1.6)
  }
  ctx.putImageData(img, 0, 0)
}

// ---- fog of war: everything dark except where you have been ----
function drawFog(m: L.Map, c: HTMLCanvasElement, p: MoveMapProps) {
  const on = !!p.layers?.fog && !!p.fogCells
  c.style.display = on ? '' : 'none'
  if (!on) return
  const { ctx, w, h } = sizeCanvas(m, c)
  ctx.globalCompositeOperation = 'source-over'
  ctx.fillStyle = 'rgba(15, 23, 42, 0.72)'
  ctx.fillRect(0, 0, w, h)
  ctx.globalCompositeOperation = 'destination-out'
  const cell = p.fogCell || 150
  for (const [lat, lng] of p.fogCells!) {
    const pt = m.latLngToContainerPoint([lat, lng])
    const r = Math.max(2.5, metresToPx(m, lat, cell * 1.1))
    if (pt.x < -r || pt.y < -r || pt.x > w + r || pt.y > h + r) continue
    const g = ctx.createRadialGradient(pt.x, pt.y, r * 0.35, pt.x, pt.y, r)
    g.addColorStop(0, 'rgba(0,0,0,1)'); g.addColorStop(1, 'rgba(0,0,0,0)')
    ctx.fillStyle = g
    ctx.beginPath(); ctx.arc(pt.x, pt.y, r, 0, Math.PI * 2); ctx.fill()
  }
  ctx.globalCompositeOperation = 'source-over'
}

/** One tile of a style, for a preview: the tile holding lat/lng at zoom z. */
export function tilePreview(key: string, lat: number, lng: number, z = 13): string {
  const t = TILES[key] || TILES.osm
  const n = 2 ** z
  const x = Math.floor(((lng + 180) / 360) * n)
  const r = (lat * Math.PI) / 180
  const y = Math.floor(((1 - Math.log(Math.tan(r) + 1 / Math.cos(r)) / Math.PI) / 2) * n)
  if (t.native && z > t.native) z = t.native
  return t.url.replace('{s}', 'a').replace('{z}', String(z)).replace('{x}', String(x)).replace('{y}', String(y)).replace('{r}', '')
}
