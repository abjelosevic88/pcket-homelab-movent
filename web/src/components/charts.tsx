// Small hand-drawn SVG charts for Insights: columns, horizontal bars, one
// stacked bar and a year calendar. Thin marks, 4px rounded data ends, a 2px
// surface gap between touching fills, values in text colours (never the
// series colour), and a tooltip on hover and keyboard focus.
import React, { useCallback, useEffect, useRef, useState } from 'react'

/** Width of an element, kept in step as it resizes (charts draw at real pixels so text stays 11px). */
export function useWidth<T extends HTMLElement>(): [React.RefObject<T | null>, number] {
  const ref = useRef<T>(null)
  const [w, setW] = useState(0)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    setW(el.clientWidth)
    const ro = new ResizeObserver(() => setW(el.clientWidth))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return [ref, w]
}

// ---- tooltip ----

export interface TipContent { title: React.ReactNode; value: React.ReactNode; color?: string; note?: React.ReactNode }
interface TipState extends TipContent { x: number; y: number }

/** One tooltip per chart, positioned inside the chart's wrapper. */
export function useTip() {
  const wrap = useRef<HTMLDivElement>(null)
  const [tip, setTip] = useState<TipState | null>(null)
  const show = useCallback((e: React.PointerEvent | React.FocusEvent, c: TipContent) => {
    const box = wrap.current?.getBoundingClientRect()
    if (!box) return
    let x: number, y: number
    if ('clientX' in e && e.type.startsWith('pointer')) { x = e.clientX - box.left; y = e.clientY - box.top }
    else { const r = (e.target as Element).getBoundingClientRect(); x = r.left + r.width / 2 - box.left; y = r.top - box.top }
    setTip({ ...c, x, y })
  }, [])
  const hide = useCallback(() => setTip(null), [])
  const node = tip ? (
    <div className="ch-tip" role="status" style={{ left: Math.max(8, Math.min(tip.x, (wrap.current?.clientWidth || 0) - 8)), top: tip.y }}>
      <div className="ch-tip-value">{tip.color && <span className="ch-key" style={{ background: tip.color }} />}{tip.value}</div>
      <div className="ch-tip-title">{tip.title}</div>
      {tip.note && <div className="ch-tip-note">{tip.note}</div>}
    </div>
  ) : null
  return { wrap, show, hide, node }
}

/** Props that give a mark hover + focus tooltips and keyboard activation. */
function markProps(tip: ReturnType<typeof useTip>, c: TipContent, onClick?: () => void) {
  return {
    tabIndex: 0,
    role: onClick ? 'button' : 'img',
    'aria-label': `${typeof c.title === 'string' ? c.title : ''}: ${typeof c.value === 'string' ? c.value : ''}`,
    onPointerMove: (e: React.PointerEvent) => tip.show(e, c),
    onPointerLeave: tip.hide,
    onFocus: (e: React.FocusEvent) => tip.show(e, c),
    onBlur: tip.hide,
    onClick,
    onKeyDown: (e: React.KeyboardEvent) => { if (onClick && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); onClick() } },
    style: onClick ? { cursor: 'pointer' } : undefined,
  }
}

/** A rectangle with 4px rounded top corners and a square base. */
function colPath(x: number, y: number, w: number, h: number, r = 4): string {
  if (h <= 0) return ''
  r = Math.min(r, h, w / 2)
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`
}

/** Clean axis maximum: 1, 2, 2.5, 5 × 10^n just above v. */
export function niceMax(v: number): number {
  if (v <= 0) return 1
  const p = Math.pow(10, Math.floor(Math.log10(v)))
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p
  return 10 * p
}

/** 1,284 · 12.9K · 4.2M */
export function compact(n: number): string {
  if (Math.abs(n) < 1000) return Math.round(n).toLocaleString()
  return new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

// ---- columns (one series: no legend, the card title names it) ----

export interface ColumnDatum { key: string; label: string; value: number; display: string; note?: string; onClick?: () => void }

export function Columns({ data, height = 180, unitTick }: { data: ColumnDatum[]; height?: number; unitTick: (v: number) => string }) {
  const [ref, width] = useWidth<HTMLDivElement>()
  const tip = useTip()
  const left = 38, bottom = 22, top = 18
  const max = niceMax(Math.max(0, ...data.map(d => d.value)))
  const plotW = Math.max(0, width - left - 4), plotH = height - top - bottom
  const band = data.length ? plotW / data.length : 0
  const bw = Math.min(24, Math.max(4, band * 0.62))
  const peak = data.reduce((best, d, i) => (d.value > (data[best]?.value ?? -1) ? i : best), 0)
  const ticks = [0, max / 2, max]
  return (
    <div className="ch-wrap" ref={(el) => { (ref as React.MutableRefObject<HTMLDivElement | null>).current = el; (tip.wrap as React.MutableRefObject<HTMLDivElement | null>).current = el }}>
      {width > 0 && (
        <svg width={width} height={height} className="ch-svg">
          {ticks.map(t => {
            const y = top + plotH - (t / max) * plotH
            return <g key={t}><line x1={left} x2={width - 4} y1={y} y2={y} className="ch-grid" /><text x={left - 6} y={y + 3.5} textAnchor="end" className="ch-axis">{unitTick(t)}</text></g>
          })}
          {data.map((d, i) => {
            const h = (d.value / max) * plotH
            const x = left + i * band + (band - bw) / 2
            const y = top + plotH - h
            return (
              <g key={d.key} className="ch-mark" {...markProps(tip, { title: d.label, value: d.display, note: d.note }, d.onClick)}>
                <rect x={left + i * band} y={top} width={band} height={plotH + bottom} fill="transparent" />
                {h > 0 ? <path d={colPath(x, y, bw, h)} className="ch-col" /> : <rect x={x} y={top + plotH - 1} width={bw} height={1} className="ch-zero" />}
                {i === peak && d.value > 0 && <text x={x + bw / 2} y={y - 5} textAnchor="middle" className="ch-val">{d.display}</text>}
                <text x={left + i * band + band / 2} y={height - 6} textAnchor="middle" className="ch-axis">{d.label}</text>
              </g>
            )
          })}
        </svg>
      )}
      {tip.node}
    </div>
  )
}

// ---- horizontal bars (each row labelled, so colour is never the only key) ----

export interface BarDatum { key: string; label: React.ReactNode; name: string; value: number; display: string; color: string; note?: string; onClick?: () => void }

export function HBars({ data }: { data: BarDatum[] }) {
  const tip = useTip()
  const max = Math.max(1, ...data.map(d => d.value))
  return (
    <div className="ch-wrap" ref={tip.wrap}>
      <div className="hbars">
        {data.map(d => (
          <div key={d.key} className="hbar-row">
            <div className="hbar-label">{d.label}</div>
            <div className="hbar-track" {...markProps(tip, { title: d.name, value: d.display, color: d.color, note: d.note }, d.onClick)}>
              <span className="hbar-fill" style={{ width: `${Math.max(1, (d.value / max) * 100)}%`, background: d.color }} />
            </div>
            <div className="hbar-value tnum">{d.display}</div>
          </div>
        ))}
      </div>
      {tip.node}
    </div>
  )
}

// ---- one stacked bar with a legend that carries the values ----

export interface Segment { key: string; label: string; value: number; display: string; color: string; pattern?: boolean }

export function StackBar({ segments }: { segments: Segment[] }) {
  const tip = useTip()
  const [ref, width] = useWidth<HTMLDivElement>()
  const total = segments.reduce((s, x) => s + x.value, 0) || 1
  const shown = segments.filter(s => s.value > 0)
  const gap = 2, h = 22
  const usable = Math.max(0, width - gap * Math.max(0, shown.length - 1))
  let x = 0
  return (
    <div className="ch-wrap" ref={tip.wrap}>
      <div ref={ref} className="stack-host">
        {width > 0 && (
          <svg width={width} height={h} className="ch-svg" role="img" aria-label="How the time was spent">
            <defs>
              <pattern id="ch-hatch" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
                <rect width="6" height="6" className="ch-hatch-bg" /><line x1="0" y1="0" x2="0" y2="6" className="ch-hatch-line" />
              </pattern>
            </defs>
            {shown.map((s, i) => {
              const w = (s.value / total) * usable
              const first = i === 0, last = i === shown.length - 1
              const r = 4
              const x0 = x
              x += w + gap
              // Round only the outer ends of the whole bar.
              const d = `M${x0 + (first ? r : 0)},0H${x0 + w - (last ? r : 0)}${last ? `Q${x0 + w},0 ${x0 + w},${r}V${h - r}Q${x0 + w},${h} ${x0 + w - r},${h}` : `V${h}`}H${x0 + (first ? r : 0)}${first ? `Q${x0},${h} ${x0},${h - r}V${r}Q${x0},0 ${x0 + r},0` : `V0`}Z`
              return <path key={s.key} d={d} fill={s.pattern ? 'url(#ch-hatch)' : s.color} className="ch-mark" {...markProps(tip, { title: s.label, value: `${s.display} · ${Math.round((s.value / total) * 100)}%`, color: s.color })} />
            })}
          </svg>
        )}
      </div>
      <ul className="ch-legend">
        {segments.map(s => (
          <li key={s.key}><span className={`ch-swatch ${s.pattern ? 'hatch' : ''}`} style={{ background: s.pattern ? undefined : s.color }} /><span className="ch-legend-label">{s.label}</span><span className="ch-legend-value tnum">{s.display}</span><span className="muted small tnum">{Math.round((s.value / total) * 100)}%</span></li>
        ))}
      </ul>
      {tip.node}
    </div>
  )
}

// ---- the year as a calendar (weeks × weekdays), one hue light → dark ----

export interface CalDay { date: string; value: number; display: string; note?: string }

/** Monday-first weekday index of a YYYY-MM-DD. */
function dow(iso: string): number {
  const [y, m, d] = iso.split('-').map(Number)
  return (new Date(Date.UTC(y, m - 1, d)).getUTCDay() + 6) % 7
}

export function YearCalendar({ year, days, onPick, levels }: { year: number; days: Map<string, CalDay>; onPick?: (date: string) => void; levels: (v: number) => number }) {
  const tip = useTip()
  const cell = 12, gap = 3, left = 26, top = 18
  const start = `${year}-01-01`
  const firstDow = dow(start)
  const all: string[] = []
  for (let t = Date.UTC(year, 0, 1); new Date(t).getUTCFullYear() === year; t += 86400000) all.push(new Date(t).toISOString().slice(0, 10))
  const weeks = Math.ceil((all.length + firstDow) / 7)
  const width = left + weeks * (cell + gap)
  const height = top + 7 * (cell + gap)
  const monthLabels: { x: number; label: string }[] = []
  all.forEach((d, i) => {
    if (d.endsWith('-01')) {
      const col = Math.floor((i + firstDow) / 7)
      monthLabels.push({ x: left + col * (cell + gap), label: new Date(d + 'T12:00:00Z').toLocaleDateString(undefined, { month: 'short', timeZone: 'UTC' }) })
    }
  })
  const today = new Date().toISOString().slice(0, 10)
  return (
    <div className="ch-wrap cal-scroll" ref={tip.wrap}>
      <svg width={width} height={height} className="ch-svg cal-svg" role="grid" aria-label={`Days with movement in ${year}`}>
        {monthLabels.map(m => <text key={m.label} x={m.x} y={11} className="ch-axis">{m.label}</text>)}
        {['Mon', '', 'Wed', '', 'Fri', '', ''].map((l, i) => l && <text key={l} x={0} y={top + i * (cell + gap) + cell - 2} className="ch-axis">{l}</text>)}
        {all.map((d, i) => {
          const col = Math.floor((i + firstDow) / 7), row = (i + firstDow) % 7
          const x = left + col * (cell + gap), y = top + row * (cell + gap)
          const day = days.get(d)
          const future = d > today
          const lvl = day ? levels(day.value) : -1
          const title = new Date(d + 'T12:00:00Z').toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short', timeZone: 'UTC' })
          return (
            <rect key={d} x={x} y={y} width={cell} height={cell} rx={3}
              className={`cal-cell ${future ? 'future' : ''} lvl-${lvl}`}
              {...markProps(tip, { title, value: day ? day.display : future ? 'still to come' : 'no data', note: day?.note }, day && onPick ? () => onPick(d) : undefined)} />
          )
        })}
      </svg>
      {tip.node}
    </div>
  )
}

/** The legend under the calendar: less → more. */
export function HeatLegend({ labels }: { labels: [string, string] }) {
  return (
    <div className="cal-legend" aria-hidden="true">
      <span className="muted small">{labels[0]}</span>
      {[-1, 0, 1, 2, 3, 4].map(l => <span key={l} className={`cal-swatch lvl-${l}`} />)}
      <span className="muted small">{labels[1]}</span>
    </div>
  )
}
