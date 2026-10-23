// Insights: the year (or any range) in numbers and charts, a year in review,
// the calendar of tracked days, and the countries and cities you have been to.
import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import L from 'leaflet'
import 'leaflet/dist/leaflet.css'
import '../styles/insights.css'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { currentUnit, flag, fmtDist, fmtDur, fmtHours, fmtLongDate, fmtShortDate, isoDateOf, modeOf, monthName, placeEmoji, todayIn } from '../lib/geo'
import type { CityRow, CountryRow, DaySummary, Insights as InsightsT } from '../lib/types'
import { Card, Empty, Loading, Tabs, copyText, errMsg, useToast } from '../components/ui'
import { Icon } from '../components/icons'
import { Columns, HBars, HeatLegend, StackBar, YearCalendar, compact, type CalDay } from '../components/charts'

type TabId = 'overview' | 'year' | 'calendar' | 'countries'
const TABS: { id: TabId; label: string; icon: string }[] = [
  { id: 'overview', label: 'Overview', icon: 'chart' },
  { id: 'year', label: 'Year in review', icon: 'star' },
  { id: 'calendar', label: 'Calendar', icon: 'calendar' },
  { id: 'countries', label: 'Countries', icon: 'globe' },
]

// Categorical slots 1–4 of the validated reference palette (light / dark),
// in fixed order; "no data" is neutral and hatched, not a series hue.
const SPLIT = [
  { key: 'home', label: 'Home', light: '#2a78d6', dark: '#3987e5' },
  { key: 'work', label: 'Work', light: '#eb6834', dark: '#d95926' },
  { key: 'elsewhere', label: 'Elsewhere', light: '#1baf7a', dark: '#199e70' },
  { key: 'moving', label: 'On the move', light: '#eda100', dark: '#c98500' },
] as const

const EARTH_M = 40_075_000
const MOON_M = 384_400_000

/** Axis ticks in whole km (or miles). */
const tickDist = (m: number) => compact(currentUnit() === 'mi' ? m / 1609.344 : m / 1000)

function lastDayOf(month: string): string {
  const [y, m] = month.split('-').map(Number)
  return new Date(Date.UTC(y, m, 0)).toISOString().slice(0, 10)
}

export default function Insights() {
  const { tab: raw } = useParams()
  const tab: TabId = TABS.some(t => t.id === raw) ? (raw as TabId) : 'overview'
  const navigate = useNavigate()
  const [sp, setSp] = useSearchParams()
  const { boot } = useApp()
  const thisYear = Number(todayIn().slice(0, 4))
  const firstYear = Math.min(thisYear, Number((boot?.first_day || todayIn()).slice(0, 4)))
  const year = Math.max(firstYear, Math.min(thisYear, Number(sp.get('year')) || thisYear))
  const from = sp.get('from') || '', to = sp.get('to') || ''
  const custom = !!from && tab !== 'calendar' && tab !== 'countries'
  const setYear = (y: number) => { const n = new URLSearchParams(sp); n.set('year', String(y)); n.delete('from'); n.delete('to'); setSp(n, { replace: true }) }
  const go = (t: TabId) => navigate(`/insights${t === 'overview' ? '' : '/' + t}${sp.toString() ? '?' + sp.toString() : ''}`, { replace: true })

  return (
    <div className="ins-page">
      <div className="ins-head">
        <div className="ins-tabs"><Tabs tabs={TABS} value={tab} onChange={go} /></div>
        {tab !== 'countries' && (custom ? (
          <div className="ins-range">
            <span className="chip">{fmtShortDate(from, true)} – {fmtShortDate(to || todayIn(), true)}</span>
            <button type="button" className="btn ghost sm" onClick={() => setYear(year)}>Back to {year}</button>
          </div>
        ) : (
          <div className="ins-year" role="group" aria-label="Year">
            <button type="button" className="btn ghost icon-only sm" disabled={year <= firstYear} onClick={() => setYear(year - 1)} aria-label="Previous year"><Icon name="chevron-left" size={16} /></button>
            <span className="ins-year-label tnum">{year}</span>
            <button type="button" className="btn ghost icon-only sm" disabled={year >= thisYear} onClick={() => setYear(year + 1)} aria-label="Next year"><Icon name="chevron-right" size={16} /></button>
          </div>
        ))}
      </div>
      {tab === 'overview' && <WithInsights year={year} from={custom ? from : ''} to={custom ? to : ''}>{d => <Overview d={d} />}</WithInsights>}
      {tab === 'year' && <WithInsights year={year} from={custom ? from : ''} to={custom ? to : ''}>{d => <YearInReview d={d} label={custom ? `${fmtShortDate(from, true)} – ${fmtShortDate(to || todayIn(), true)}` : String(year)} />}</WithInsights>}
      {tab === 'calendar' && <CalendarTab year={year} />}
      {tab === 'countries' && <CountriesTab />}
    </div>
  )
}

/** Loads /insights for the range and hands it to the tab, with loading, error and empty states. */
function WithInsights({ year, from, to, children }: { year: number; from: string; to: string; children: (d: InsightsT) => React.ReactNode }) {
  const [d, setD] = useState<InsightsT | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    let live = true
    setD(null); setErr('')
    const q = from ? `from=${from}&to=${to || todayIn()}` : `year=${year}`
    api.get<InsightsT>(`${V1}/insights?${q}`).then(r => { if (live) setD(r) }).catch(e => { if (live) setErr(errMsg(e)) })
    return () => { live = false }
  }, [year, from, to])
  if (err) return <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>
  if (!d) return <Loading />
  if (!d.totals.days) return <NoData />
  return <>{children(d)}</>
}

function NoData() {
  return (
    <Empty title="Nothing recorded in this period" icon="chart">
      Connect your phone under <Link to="/devices">Devices</Link>, or bring your history in from Google Takeout, GPX or Dawarich under <Link to="/data">Import &amp; export</Link>.
    </Empty>
  )
}

function useDark() {
  const { theme } = useApp()
  return theme === 'dark'
}

// ---------- overview ----------

function Stat({ label, value, sub, to }: { label: string; value: React.ReactNode; sub?: React.ReactNode; to?: string }) {
  const body = <><div className="stat-label">{label}</div><div className="stat-value tnum">{value}</div>{sub && <div className="stat-sub">{sub}</div>}</>
  return to ? <Link to={to} className="stat link">{body}</Link> : <div className="stat">{body}</div>
}

function Overview({ d }: { d: InsightsT }) {
  const navigate = useNavigate()
  const dark = useDark()
  const t = d.totals
  const months = d.months.map(m => ({
    key: m.month, label: monthName(m.month).slice(0, 3), value: m.distance, display: fmtDist(m.distance),
    note: `${m.days} days tracked${m.countries > 1 ? ` · ${m.countries} countries` : ''}`,
    onClick: () => navigate(`/?from=${m.month}-01&to=${lastDayOf(m.month)}`),
  }))
  const modes = Object.entries(d.modes).filter(([, v]) => v > 0).sort((a, b) => b[1] - a[1]).map(([m, v]) => ({
    key: m, name: modeOf(m).label, label: <><span className="mode-emoji" aria-hidden="true">{modeOf(m).emoji}</span>{modeOf(m).label}</>,
    value: v, display: fmtDist(v), color: modeOf(m).color, note: `${d.journeys_by_mode[m] || 0} journeys`,
  }))
  // Monday first.
  const week = [1, 2, 3, 4, 5, 6, 0].map(i => d.weekdays[i]).filter(Boolean).map(w => ({
    key: w.weekday, label: w.weekday.slice(0, 3), value: w.avg, display: fmtDist(w.avg), note: `average over ${w.days} days`,
  }))
  const split = [
    ...SPLIT.map(s => ({ key: s.key, label: s.label, value: d.time_split[s.key], display: fmtHours(d.time_split[s.key]), color: dark ? s.dark : s.light })),
    { key: 'unknown', label: 'No data', value: d.time_split.unknown, display: fmtHours(d.time_split.unknown), color: 'var(--surface-3)', pattern: true },
  ]
  const r = d.records
  return (
    <div className="ins-grid">
      <div className="stats span-all">
        <Stat label="Distance" value={fmtDist(t.distance)} sub={`${(t.distance / EARTH_M).toFixed(2)}× around the Earth`} />
        <Stat label="Days tracked" value={t.days.toLocaleString()} sub={`${compact(t.points)} points`} to="/insights/calendar" />
        <Stat label="On the move" value={fmtHours(t.moving_s)} sub={`${t.journeys.toLocaleString()} journeys`} />
        <Stat label="Visits" value={t.visits.toLocaleString()} sub={`${t.places} places · ${t.new_places} new`} to="/places" />
        <Stat label="Countries" value={t.countries} sub={d.countries.slice(0, 6).map(c => flag(c.code)).join(' ')} to="/insights/countries" />
        <Stat label="Cities" value={t.cities} sub={d.cities.slice(0, 2).map(c => c.city).join(', ')} to="/insights/countries" />
      </div>

      <Card title="Distance by month" className="span-2">
        <Columns data={months} unitTick={tickDist} />
        <p className="muted small">Tap a month to see it on the map.</p>
      </Card>

      <Card title="How you moved" sub="Distance by transport mode">
        {modes.length ? <HBars data={modes} /> : <p className="muted">No journeys yet.</p>}
      </Card>

      <Card title="Where the time went" className="span-2">
        <StackBar segments={split} />
      </Card>

      <Card title="Average day of the week" sub="Distance per tracked day">
        <Columns data={week} height={150} unitTick={tickDist} />
      </Card>

      <Card title="Top places" sub="By time spent" flush>
        {d.top_places.length === 0 ? <p className="muted pad">No visits yet.</p> : (
          <ol className="rank">
            {d.top_places.slice(0, 8).map((p, i) => (
              <li key={p.place.id}>
                <Link to={`/places/${p.place.id}`} className="rank-row">
                  <span className="rank-n tnum">{i + 1}</span>
                  <span className="rank-emoji" aria-hidden="true">{placeEmoji(p.place.category)}</span>
                  <span className="grow ellipsis"><b>{p.place.name || 'Unnamed place'}</b><span className="muted small"> {p.place.city}</span></span>
                  <span className="tnum small">{fmtHours(p.total_s)}</span>
                  <span className="muted small tnum rank-visits">{p.visits}×</span>
                </Link>
              </li>
            ))}
          </ol>
        )}
      </Card>

      <Card title="Records" className="span-2">
        <div className="records">
          {r.longest_day && <Record icon="route" label="Longest day" value={fmtDist(r.longest_day.distance)} sub={fmtLongDate(r.longest_day.date)} to={`/timeline/${r.longest_day.date}`} />}
          {r.furthest_from_home && <Record icon="compass" label="Furthest from home" value={fmtDist(r.furthest_from_home.distance)} sub={fmtLongDate(r.furthest_from_home.date)} to={`/timeline/${r.furthest_from_home.date}`} />}
          {r.longest_journey && <Record icon={modeOf(r.longest_journey.mode).icon} label="Longest journey" value={fmtDist(r.longest_journey.distance)} sub={`${modeOf(r.longest_journey.mode).label} · ${fmtDur(r.longest_journey.end_ts - r.longest_journey.start_ts)} · ${fmtShortDate(isoDateOf(r.longest_journey.start_ts), true)}`} to={`/timeline/${isoDateOf(r.longest_journey.start_ts)}`} />}
          {r.fastest_journey && <Record icon="activity" label="Top speed" value={`${Math.round(r.fastest_journey.max_speed)} km/h`} sub={`${modeOf(r.fastest_journey.mode).label} · ${fmtShortDate(isoDateOf(r.fastest_journey.start_ts), true)}`} to={`/timeline/${isoDateOf(r.fastest_journey.start_ts)}`} />}
          {r.most_countries && <Record icon="globe" label="Most countries in a day" value={r.most_countries.countries.map(flag).join(' ')} sub={fmtLongDate(r.most_countries.date)} to={`/timeline/${r.most_countries.date}`} />}
          {r.longest_streak && <Record icon="fire" label="Longest streak" value={`${r.longest_streak.days} days`} sub={`ended ${fmtShortDate(r.longest_streak.ended, true)}`} />}
        </div>
      </Card>

      {d.countries.length > 0 && (
        <Card title="Countries" sub={`${d.countries.length} in this period`} actions={<Link to="/insights/countries" className="btn ghost sm">Scratch map</Link>}>
          <ul className="flags">
            {d.countries.map(c => <li key={c.code} title={`${c.name}: ${c.days} days`}><span className="flag-emoji" aria-hidden="true">{flag(c.code)}</span><span className="ellipsis">{c.name}</span><span className="muted small tnum">{c.days} d</span>{c.new && <span className="badge accent">new</span>}</li>)}
          </ul>
        </Card>
      )}
    </div>
  )
}

function Record({ icon, label, value, sub, to }: { icon: string; label: string; value: React.ReactNode; sub: string; to?: string }) {
  const body = <><span className="rec-ico"><Icon name={icon} size={18} /></span><span className="grow"><span className="rec-label">{label}</span><span className="rec-value">{value}</span><span className="rec-sub muted small">{sub}</span></span></>
  return to ? <Link to={to} className="rec link">{body}</Link> : <div className="rec">{body}</div>
}

// ---------- year in review ----------

function YearInReview({ d, label }: { d: InsightsT; label: string }) {
  const { toast } = useToast()
  const t = d.totals
  const laps = t.distance / EARTH_M
  const moon = (t.distance / MOON_M) * 100
  const fav = Object.entries(d.modes).filter(([m, v]) => m !== 'unknown' && v > 0).sort((a, b) => b[1] - a[1])[0]
  const busiest = [...d.months].sort((a, b) => b.distance - a.distance)[0]
  const newCountries = d.countries.filter(c => c.new)
  const top3 = d.top_places.slice(0, 3)
  const r = d.records
  const scale = laps >= 1 ? `${laps.toFixed(laps < 10 ? 1 : 0)}× around the Earth` : moon >= 1 ? `${moon.toFixed(moon < 10 ? 1 : 0)}% of the way to the Moon` : `${(laps * 100).toFixed(1)}% of the way around the Earth`
  const summary = [
    `My ${label} on the move`,
    `${fmtDist(t.distance)} — ${scale}`,
    `${t.days} days tracked, ${t.journeys} journeys, ${t.visits} visits to ${t.places} places (${t.new_places} new)`,
    d.countries.length ? `${d.countries.length} ${d.countries.length === 1 ? 'country' : 'countries'}: ${d.countries.map(c => `${flag(c.code)} ${c.name}`).join(', ')}` : '',
    fav ? `Mostly ${modeOf(fav[0]).label.toLowerCase()}: ${fmtDist(fav[1])}` : '',
    top3.length ? `Favourite places: ${top3.map(p => p.place.name).filter(Boolean).join(', ')}` : '',
  ].filter(Boolean).join('\n')

  return (
    <div className="review">
      <section className="review-hero">
        <div className="review-kicker">{label} on the move</div>
        <div className="review-big tnum">{fmtDist(t.distance)}</div>
        <div className="review-scale">{scale}</div>
        <div className="review-route" aria-hidden="true">
          <svg viewBox="0 0 300 40" preserveAspectRatio="none"><path d="M4 30 C40 6 70 36 110 18 S180 4 210 22 S270 34 296 10" /><circle cx="4" cy="30" r="4" /><circle cx="296" cy="10" r="5" /></svg>
        </div>
      </section>

      <div className="review-cards">
        <ReviewCard emoji="📅" big={t.days.toLocaleString()} label="days tracked" sub={r.longest_streak ? `longest streak ${r.longest_streak.days} days` : undefined} />
        <ReviewCard emoji="🧭" big={t.journeys.toLocaleString()} label="journeys" sub={`${fmtHours(t.moving_s)} on the move`} />
        <ReviewCard emoji="📍" big={t.places.toLocaleString()} label="places visited" sub={`${t.new_places} of them new`} />
        <ReviewCard emoji="🌍" big={String(t.countries)} label={t.countries === 1 ? 'country' : 'countries'} sub={`${t.cities} ${t.cities === 1 ? 'city' : 'cities'}`} />
        {fav && <ReviewCard emoji={modeOf(fav[0]).emoji} big={modeOf(fav[0]).label} label="favourite way to move" sub={fmtDist(fav[1])} />}
        {busiest && busiest.distance > 0 && <ReviewCard emoji="🔥" big={monthName(busiest.month)} label="busiest month" sub={fmtDist(busiest.distance)} />}
      </div>

      {top3.length > 0 && (
        <section className="review-section">
          <h3>Where you spent your time</h3>
          <ol className="podium">
            {top3.map((p, i) => (
              <li key={p.place.id} className={`podium-${i + 1}`}>
                <Link to={`/places/${p.place.id}`}>
                  <span className="podium-medal" aria-hidden="true">{['🥇', '🥈', '🥉'][i]}</span>
                  <span className="podium-name">{placeEmoji(p.place.category)} {p.place.name || 'Unnamed place'}</span>
                  <span className="muted small">{fmtHours(p.total_s)} · {p.visits} visits</span>
                </Link>
              </li>
            ))}
          </ol>
        </section>
      )}

      {d.countries.length > 0 && (
        <section className="review-section">
          <h3>{newCountries.length ? `${newCountries.length} new ${newCountries.length === 1 ? 'country' : 'countries'}` : 'Countries'}</h3>
          <div className="review-flags">
            {d.countries.map(c => <span key={c.code} className={`review-flag ${c.new ? 'new' : ''}`} title={`${c.name} · ${c.days} days${c.new ? ' · first time' : ''}`}><span aria-hidden="true">{flag(c.code)}</span>{c.name}</span>)}
          </div>
        </section>
      )}

      {(r.longest_day || r.furthest_from_home || r.longest_journey) && (
        <section className="review-section">
          <h3>Personal bests</h3>
          <div className="records">
            {r.longest_day && <Record icon="route" label="Biggest day" value={fmtDist(r.longest_day.distance)} sub={fmtLongDate(r.longest_day.date)} to={`/timeline/${r.longest_day.date}`} />}
            {r.furthest_from_home && <Record icon="compass" label="Furthest from home" value={fmtDist(r.furthest_from_home.distance)} sub={fmtLongDate(r.furthest_from_home.date)} to={`/timeline/${r.furthest_from_home.date}`} />}
            {r.longest_journey && <Record icon={modeOf(r.longest_journey.mode).icon} label="Longest journey" value={fmtDist(r.longest_journey.distance)} sub={`${modeOf(r.longest_journey.mode).label} · ${fmtShortDate(isoDateOf(r.longest_journey.start_ts), true)}`} to={`/timeline/${isoDateOf(r.longest_journey.start_ts)}`} />}
          </div>
        </section>
      )}

      <div className="review-actions">
        <button type="button" className="btn" onClick={async () => toast(await copyText(summary) ? 'Summary copied' : 'Could not copy', 'ok')}><Icon name="copy" size={15} />Copy as text</button>
        <Link to="/insights" className="btn ghost">All the numbers</Link>
      </div>
    </div>
  )
}

function ReviewCard({ emoji, big, label, sub }: { emoji: string; big: string; label: string; sub?: string }) {
  return <div className="review-card"><span className="review-emoji" aria-hidden="true">{emoji}</span><span className="review-card-big">{big}</span><span className="review-card-label">{label}</span>{sub && <span className="muted small">{sub}</span>}</div>
}

// ---------- calendar ----------

function CalendarTab({ year }: { year: number }) {
  const navigate = useNavigate()
  const [days, setDays] = useState<DaySummary[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    let live = true
    setDays(null); setErr('')
    api.get<DaySummary[]>(`${V1}/calendar?year=${year}`).then(r => { if (live) setDays(r) }).catch(e => { if (live) setErr(errMsg(e)) })
    return () => { live = false }
  }, [year])
  // Four steps by quartile of the days that moved, so one long trip does not flatten the rest.
  const { map, levels } = useMemo(() => {
    const map = new Map<string, CalDay>()
    const moved = (days || []).map(d => d.distance).filter(v => v > 0).sort((a, b) => a - b)
    const q = (p: number) => moved[Math.min(moved.length - 1, Math.floor(p * moved.length))] || 0
    const cuts = [q(0.25), q(0.5), q(0.75)]
    for (const d of days || []) {
      map.set(d.date, { date: d.date, value: d.distance, display: d.distance > 0 ? fmtDist(d.distance) : 'stayed put', note: [`${d.visits} visits`, d.countries.map(flag).join(' ')].filter(Boolean).join(' · ') })
    }
    const levels = (v: number) => v <= 0 ? 0 : v <= cuts[0] ? 1 : v <= cuts[1] ? 2 : v <= cuts[2] ? 3 : 4
    return { map, levels }
  }, [days])
  if (err) return <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>
  if (!days) return <Loading />
  if (!days.length) return <NoData />
  const months = Array.from({ length: 12 }, (_, i) => `${year}-${String(i + 1).padStart(2, '0')}`).map(m => {
    const list = days.filter(d => d.date.startsWith(m))
    const countries = [...new Set(list.flatMap(d => d.countries))]
    return { m, days: list.length, distance: list.reduce((s, d) => s + d.distance, 0), visits: list.reduce((s, d) => s + d.visits, 0), countries }
  })
  const total = days.reduce((s, d) => s + d.distance, 0)
  return (
    <div className="ins-grid">
      <Card title={`${days.length} days tracked in ${year}`} sub={`${fmtDist(total)} in all · tap a day to open it`} className="span-all">
        <YearCalendar year={year} days={map} levels={levels} onPick={d => navigate(`/timeline/${d}`)} />
        <HeatLegend labels={['Less', 'More']} />
      </Card>
      <div className="months span-all">
        {months.map(x => (
          <Link key={x.m} to={x.days ? `/?from=${x.m}-01&to=${lastDayOf(x.m)}` : '#'} className={`month ${x.days ? '' : 'empty'}`} onClick={e => { if (!x.days) e.preventDefault() }}>
            <span className="month-name">{monthName(x.m)}</span>
            <span className="month-dist tnum">{x.days ? fmtDist(x.distance) : '—'}</span>
            <span className="muted small">{x.days ? `${x.days} days · ${x.visits} visits` : 'no data'}</span>
            {x.countries.length > 0 && <span className="month-flags">{x.countries.map(flag).join(' ')}</span>}
          </Link>
        ))}
      </div>
    </div>
  )
}

// ---------- countries ----------

interface CountriesResp { countries: CountryRow[]; continents: number; world: number }

function CountriesTab() {
  const { boot } = useApp()
  const [data, setData] = useState<CountriesResp | null>(null)
  const [cities, setCities] = useState<CityRow[] | null>(null)
  const [err, setErr] = useState('')
  const [allCities, setAllCities] = useState(false)
  useEffect(() => {
    let live = true
    api.get<CountriesResp>(`${V1}/countries`).then(r => { if (live) setData(r) }).catch(e => { if (live) setErr(errMsg(e)) })
    const first = boot?.first_day || todayIn()
    api.get<InsightsT>(`${V1}/insights?from=${first}&to=${todayIn()}`).then(r => { if (live) setCities(r.cities) }).catch(() => { if (live) setCities([]) })
    return () => { live = false }
  }, [boot?.first_day])
  if (err) return <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>
  if (!data) return <Loading />
  if (!data.countries.length) return <NoData />
  const n = data.countries.length
  const byContinent = new Map<string, number>()
  for (const c of data.countries) byContinent.set(c.continent || 'Elsewhere', (byContinent.get(c.continent || 'Elsewhere') || 0) + 1)
  const cityList = (cities || []).slice().sort((a, b) => b.total_s - a.total_s)
  const home = boot?.home?.country || data.countries.slice().sort((a, b) => b.days - a.days)[0]?.code
  const abroad = data.countries.filter(c => c.code !== home).reduce((s, c) => s + c.days, 0)
  return (
    <div className="ins-grid">
      <div className="stats span-all">
        <Stat label="Countries" value={n} sub={`${((n / data.world) * 100).toFixed(1)}% of ${data.world}`} />
        <Stat label="Continents" value={data.continents} sub={[...byContinent.entries()].map(([k, v]) => `${k} ${v}`).join(' · ')} />
        <Stat label="Cities" value={cities ? cities.length : '…'} sub="where you stopped" />
        <Stat label="Days abroad" value={abroad} sub={home ? `days with a point outside ${flag(home)}` : undefined} />
      </div>
      <Card title="Scratch map" className="span-all" flush>
        <ScratchMap codes={data.countries.map(c => c.code)} />
      </Card>
      <Card title="Countries" sub="All time, most days first" flush>
        <ul className="country-list">
          {data.countries.slice().sort((a, b) => b.days - a.days).map(c => (
            <li key={c.code}>
              <span className="flag-emoji big" aria-hidden="true">{flag(c.code)}</span>
              <span className="grow">
                <b>{c.name}</b>
                <span className="muted small block">{c.first === c.last ? fmtShortDate(c.first, true) : `${fmtShortDate(c.first, true)} – ${fmtShortDate(c.last, true)}`}{c.years && c.years.length > 1 ? ` · ${c.years.join(', ')}` : ''}</span>
              </span>
              <span className="tnum">{c.days}<span className="muted small"> {c.days === 1 ? 'day' : 'days'}</span></span>
            </li>
          ))}
        </ul>
      </Card>
      <Card title="Cities" sub={cities ? `${cities.length} with a stop` : undefined} flush>
        {!cities ? <Loading /> : cities.length === 0 ? <p className="muted pad">Cities appear once places get their names from the geocoder.</p> : (
          <>
            <ul className="country-list">
              {(allCities ? cityList : cityList.slice(0, 20)).map(c => (
                <li key={c.country + c.city}>
                  <span className="flag-emoji" aria-hidden="true">{flag(c.country)}</span>
                  <span className="grow"><b>{c.city}</b><span className="muted small block">{c.visits} visits · last {fmtShortDate(isoDateOf(c.last), true)}</span></span>
                  <span className="tnum small">{fmtHours(c.total_s)}</span>
                </li>
              ))}
            </ul>
            {cityList.length > 20 && <button type="button" className="btn ghost sm block-btn" onClick={() => setAllCities(a => !a)}>{allCities ? 'Show fewer' : `Show all ${cityList.length}`}</button>}
          </>
        )}
      </Card>
    </div>
  )
}

/** A world map with the countries you have been to filled in. */
function ScratchMap({ codes }: { codes: string[] }) {
  const ref = useRef<HTMLDivElement>(null)
  const map = useRef<L.Map | null>(null)
  const dark = useDark()
  const tiles = useRef<L.TileLayer | null>(null)
  useEffect(() => {
    if (!ref.current || map.current) return
    const m = L.map(ref.current, { worldCopyJump: true, zoomControl: true, attributionControl: true, minZoom: 1 }).setView([30, 10], 2)
    map.current = m
    const ro = new ResizeObserver(() => m.invalidateSize())
    ro.observe(ref.current)
    return () => { ro.disconnect(); m.remove(); map.current = null }
  }, [])
  useEffect(() => {
    const m = map.current
    if (!m) return
    tiles.current?.remove()
    // Keyless OSM tiles, washed out so the visited countries stand out.
    tiles.current = L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
      attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors', maxZoom: 8, className: dark ? 'scratch-tiles dark' : 'scratch-tiles',
    }).addTo(m)
  }, [dark])
  useEffect(() => {
    const m = map.current
    if (!m || !codes.length) return
    let live = true
    const group = L.featureGroup().addTo(m)
    const accent = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim() || '#4d7c0f'
    Promise.all(codes.map(c => api.get<{ code: string; name: string; rings: [number, number][][] }>(`${V1}/countries/${c}/shape`).catch(() => null))).then(shapes => {
      if (!live) return
      for (const s of shapes) {
        if (!s) continue
        L.polygon(s.rings, { color: accent, weight: 1.2, fillColor: accent, fillOpacity: dark ? 0.55 : 0.45 }).bindTooltip(`${flag(s.code)} ${s.name}`, { sticky: true }).addTo(group)
      }
      const b = group.getBounds()
      if (b.isValid()) m.fitBounds(b, { padding: [24, 24], maxZoom: 5 })
    })
    return () => { live = false; group.remove() }
  }, [codes.join(','), dark]) // eslint-disable-line react-hooks/exhaustive-deps
  return <div ref={ref} className="scratch-map" role="img" aria-label={`World map with ${codes.length} visited countries filled in`} />
}

