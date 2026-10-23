// The app shell: sidebar, top bar, search palette, shortcuts — the Pocket layout.
import { useEffect, useRef, useState } from 'react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { agoTs, fmtDist, todayIn } from '../lib/geo'
import { Icon } from './icons'
import { CommandPalette, type PaletteCommand } from './CommandPalette'
import { ShortcutsSheet } from './ShortcutsSheet'
import { Avatar, type MenuItem, MenuButton, modalOpen, useIsMobile, useMediaQuery } from './ui'
import { AskButton } from './AskButton'

const SIDEBAR_KEY = 'pm-sidebar'

export const NAV = [
  { to: '/', label: 'Map', icon: 'map', end: true, key: 'm' },
  { to: '/timeline', label: 'Timeline', icon: 'activity', key: 't' },
  { to: '/insights', label: 'Insights', icon: 'chart', key: 'i' },
  { to: '/places', label: 'Places', icon: 'pin', key: 'p' },
  { to: '/family', label: 'Family & sharing', icon: 'users', key: 'f' },
  { to: '/devices', label: 'Devices', icon: 'smartphone', key: 'd' },
  { to: '/data', label: 'Import & export', icon: 'database', key: 'e' },
]

export default function Layout() {
  const navigate = useNavigate()
  const loc = useLocation()
  const mobile = useIsMobile()
  const narrow = useMediaQuery('(max-width: 1099px)')
  const drawer = mobile || narrow
  const [sidebarOpen, setSidebarOpen] = useState(() => !drawer && localStorage.getItem(SIDEBAR_KEY) !== '0')
  useEffect(() => { setSidebarOpen(!drawer && localStorage.getItem(SIDEBAR_KEY) !== '0') }, [drawer])
  const drawerOpenedAt = useRef(0)
  const toggleSidebar = () => setSidebarOpen(o => { drawerOpenedAt.current = performance.now(); const n = !o; if (!drawer) localStorage.setItem(SIDEBAR_KEY, n ? '1' : '0'); return n })
  const closeDrawer = () => { if (drawer) setSidebarOpen(false) }
  const section = loc.pathname.split('/')[1] || ''
  useEffect(() => { document.querySelector('.content')?.scrollTo({ top: 0 }) }, [section])

  const [palette, setPalette] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  useEffect(() => {
    const open = () => setShortcuts(true)
    window.addEventListener('pm:shortcuts', open)
    return () => window.removeEventListener('pm:shortcuts', open)
  }, [])
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette(p => !p); return }
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (t.closest?.('input, textarea, select, [contenteditable="true"]') || modalOpen() || document.querySelector('.menu, .popover, .sel-pop')) return
      const go = (to: string) => { e.preventDefault(); navigate(to) }
      const n = NAV.find(x => x.key === e.key)
      if (n) { go(n.to); return }
      switch (e.key) {
        case '/': e.preventDefault(); setPalette(true); break
        case '?': e.preventDefault(); setShortcuts(true); break
        case ',': go('/settings'); break
        case '[': e.preventDefault(); toggleSidebar(); break
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }) // eslint-disable-line react-hooks/exhaustive-deps

  const commands: PaletteCommand[] = [
    ...NAV.map(n => ({ id: n.to, label: n.label, icon: n.icon, hint: n.key.toUpperCase(), run: () => navigate(n.to) })),
    { id: 'today', label: "Today's timeline", icon: 'today', keywords: 'day now', run: () => navigate(`/timeline/${todayIn()}`) },
    { id: 'year', label: 'Year in review', icon: 'star', keywords: 'report insights annual', run: () => navigate('/insights') },
    { id: 'countries', label: 'Countries you have been to', icon: 'globe', keywords: 'scratch map world', run: () => navigate('/insights/countries') },
    { id: 'device', label: 'Connect a phone or app', icon: 'smartphone', keywords: 'owntracks overland dawarich gpslogger traccar setup key', run: () => navigate('/devices?add=1') },
    { id: 'import', label: 'Import Google Takeout, GPX or Dawarich', icon: 'upload', keywords: 'records json timeline gpx kml geojson', run: () => navigate('/data') },
    { id: 'export', label: 'Export your history', icon: 'download', keywords: 'gpx geojson csv backup', run: () => navigate('/data#export') },
    { id: 'share', label: 'Share your location', icon: 'share', keywords: 'family link live', run: () => navigate('/family') },
    { id: 'settings', label: 'Settings', icon: 'settings', hint: ',', run: () => navigate('/settings') },
    { id: 'shortcuts', label: 'Keyboard shortcuts', icon: 'keyboard', hint: '?', run: () => setShortcuts(true) },
  ]

  return (
    <div className={`app ${sidebarOpen ? 'with-sidebar' : 'no-sidebar'} ${mobile ? 'is-mobile' : ''} ${drawer ? 'drawer-mode' : ''} sec-${section || 'map'}`}>
      {drawer && sidebarOpen && <div className="drawer-backdrop" onClick={e => { if (e.timeStamp - drawerOpenedAt.current > 400) setSidebarOpen(false) }} />}
      <div className={`sidebar-wrap ${sidebarOpen ? 'open' : ''}`}>
        <Sidebar onNavigate={closeDrawer} />
      </div>
      <div className="main">
        <TopBar onMenu={toggleSidebar} onPalette={() => setPalette(true)} mobile={mobile} compact={narrow} sidebarOpen={sidebarOpen} />
        <main className="content"><div className="content-inner"><Outlet /></div></main>
      </div>
      {palette && <CommandPalette onClose={() => setPalette(false)} commands={commands} />}
      {shortcuts && <ShortcutsSheet onClose={() => setShortcuts(false)} />}
    </div>
  )
}

function Sidebar({ onNavigate }: { onNavigate: () => void }) {
  const { status, settings, boot } = useApp()
  const title = settings?.title || status?.title || 'Pocket Movement'
  const devices = (boot?.devices || []).filter(d => !d.revoked && d.scope !== 'read')
  const stale = devices.filter(d => d.last_point_at && Date.now() / 1000 - d.last_point_at > 12 * 3600).length
  const [today, setToday] = useState<{ distance: number } | null>(null)
  useEffect(() => { api.get<{ today?: { distance: number } }>(`${V1}/now`).then(n => setToday(n.today || null)).catch(() => {}) }, [])
  return (
    <aside className="sidebar" aria-label="Movement">
      <div className="sb-brand">
        <img src="/favicon.svg" alt="" className="sb-logo" />
        <span className="sb-title">{title !== 'Pocket Movement' ? title : <>Pocket <b>Movement</b></>}</span>
        {status?.version && <span className="sb-version" title={`Version ${status.version}`}>{/^\d/.test(status.version) ? 'v' + status.version : status.version}</span>}
      </div>
      <NavLink to={`/timeline/${todayIn()}`} className="btn primary create-btn" onClick={onNavigate}>
        <Icon name="today" size={17} strokeWidth={2} />Today{today ? <span className="create-sub">{fmtDist(today.distance)}</span> : null}
      </NavLink>
      <nav className="sb-nav" aria-label="Sections">
        {NAV.map(n => (
          <NavLink key={n.to} to={n.to} end={n.end} onClick={onNavigate} className={({ isActive }) => `sb-nav-item ${isActive ? 'active' : ''}`}>
            <Icon name={n.icon} size={16} />{n.label}
            {n.to === '/devices' && stale > 0 && <span className="badge warn" title="Some phones have not reported for a while">{stale} quiet</span>}
            {n.to === '/family' && (boot?.shared_with_me.length || 0) > 0 && <span className="sb-count">{boot!.shared_with_me.length}</span>}
          </NavLink>
        ))}
      </nav>
      {devices.length > 0 && (
        <div className="sb-group">
          <div className="sb-group-h">Phones</div>
          {devices.slice(0, 4).map(d => {
            const age = d.last_point_at ? Date.now() / 1000 - d.last_point_at : Infinity
            return (
              <NavLink key={d.id} to={`/devices#d${d.id}`} className="sb-device" onClick={onNavigate} title={d.last_point_at ? `Last position ${agoTs(d.last_point_at)}` : 'No position yet'}>
                <span className={`dot ${age < 3600 ? 'ok' : age < 12 * 3600 ? 'mid' : 'off'}`} />
                <span className="grow ellipsis">{d.name}</span>
                <span className="muted small">{d.last_point_at ? agoTs(d.last_point_at) : '—'}</span>
              </NavLink>
            )
          })}
        </div>
      )}
      <div className="sb-foot">
        <NavLink to="/settings" className="sb-foot-link" onClick={onNavigate}><Icon name="settings" size={15} />Settings</NavLink>
        <button type="button" className="sb-foot-link" onClick={() => { onNavigate(); window.dispatchEvent(new CustomEvent('pm:shortcuts')) }}><Icon name="keyboard" size={15} />Shortcuts</button>
      </div>
    </aside>
  )
}

const SECTION: Record<string, string> = { '': 'Map', timeline: 'Timeline', insights: 'Insights', places: 'Places', family: 'Family & sharing', devices: 'Devices', data: 'Import & export' }

function TopBar({ onMenu, onPalette, mobile, compact, sidebarOpen }: { onMenu: () => void; onPalette: () => void; mobile: boolean; compact: boolean; sidebarOpen: boolean }) {
  const { user, themeMode, setThemeMode, status, setUser } = useApp()
  const navigate = useNavigate()
  const loc = useLocation()
  const section = SECTION[loc.pathname.split('/')[1] || ''] || ''
  const signOut = async () => { await api.post(`${V1}/auth/logout`).catch(() => {}); setUser(null); navigate('/login') }
  const avatarMenu = (): MenuItem[] => [
    { heading: user?.name || user?.email || '' },
    { label: 'Profile', icon: 'user', onSelect: () => navigate('/settings/profile') },
    { label: 'Settings', icon: 'settings', onSelect: () => navigate('/settings'), hint: ',' },
    { label: 'Keyboard shortcuts', icon: 'keyboard', onSelect: () => window.dispatchEvent(new CustomEvent('pm:shortcuts')), hint: '?' },
    'sep',
    { heading: 'Appearance' },
    { label: 'System', checked: themeMode === 'system', onSelect: () => setThemeMode('system') },
    { label: 'Light', checked: themeMode === 'light', onSelect: () => setThemeMode('light') },
    { label: 'Dark', checked: themeMode === 'dark', onSelect: () => setThemeMode('dark') },
    ...(status?.auth_mode === 'local' ? ['sep' as const, { label: 'Sign out', icon: 'log-out', onSelect: signOut }] : []),
  ]
  return (
    <header className="topbar">
      <button type="button" className="btn ghost icon-only" onClick={onMenu} aria-label={sidebarOpen ? 'Hide the sidebar' : 'Show the sidebar'} title="Menu ([)"><Icon name={mobile || compact ? 'menu' : 'sidebar'} size={18} /></button>
      <h1 className="range-title">{section}</h1>
      <span className="spacer" id="topbar-slot" />
      <button type="button" className="btn ghost icon-only" onClick={onPalette} aria-label="Search" title="Search (⌘K or /)"><Icon name="search" size={18} /></button>
      <AskButton />
      <MenuButton label="Account" className="avatar-btn" align="right" width={220} items={avatarMenu}>
        <Avatar name={user?.name} email={user?.email} size={30} />
      </MenuButton>
    </header>
  )
}
