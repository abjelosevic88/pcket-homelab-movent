import React, { createContext, useCallback, useContext, useEffect, useState } from 'react'
import { api, V1 } from './api'
import { setUnits } from './geo'
import type { Bootstrap, Settings, User } from './types'

export type ThemeMode = 'system' | 'light' | 'dark'

interface AuthStatus { needs_setup: boolean; auth_mode: string; authenticated: boolean; user: User | null; version: string; title: string; oidc?: boolean; oidc_name?: string }
interface AppState {
  status: AuthStatus | null
  user: User | null
  boot: Bootstrap | null
  settings: Settings | null
  users: User[]
  themeMode: ThemeMode
  theme: 'light' | 'dark'
  setThemeMode: (m: ThemeMode) => void
  refresh: () => Promise<void>
  reloadBoot: () => Promise<void>
  setUser: (u: User | null) => void
  setSettings: (s: Settings) => void
}
const Ctx = createContext<AppState>(null!)
export function useApp() { return useContext(Ctx) }

export function AppProvider({ children }: { children: React.ReactNode }) {
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [user, setUser] = useState<User | null>(null)
  const [boot, setBoot] = useState<Bootstrap | null>(null)
  const [settings, setSettingsRaw] = useState<Settings | null>(null)
  const [users, setUsers] = useState<User[]>([])
  // The theme follows the system unless set to light or dark.
  const [themeMode, setThemeModeRaw] = useState<ThemeMode>(() => (localStorage.getItem('pm-theme') as ThemeMode) || 'system')
  const [systemDark, setSystemDark] = useState(() => window.matchMedia('(prefers-color-scheme: dark)').matches)
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const h = () => setSystemDark(mq.matches)
    mq.addEventListener('change', h)
    return () => mq.removeEventListener('change', h)
  }, [])
  const theme: 'light' | 'dark' = themeMode === 'system' ? (systemDark ? 'dark' : 'light') : themeMode
  useEffect(() => {
    document.documentElement.dataset.theme = theme
    document.querySelectorAll('meta[name="theme-color"]').forEach(m => m.setAttribute('content', theme === 'dark' ? '#111113' : '#ffffff'))
  }, [theme])
  const setThemeMode = useCallback((m: ThemeMode) => { setThemeModeRaw(m); if (m === 'system') localStorage.removeItem('pm-theme'); else localStorage.setItem('pm-theme', m) }, [])
  useEffect(() => { if (settings?.title) document.title = settings.title }, [settings?.title])

  const reloadBoot = useCallback(async () => {
    const b = await api.get<Bootstrap>(`${V1}/bootstrap`)
    setUnits(b.settings.distance_unit, b.timezone)
    setBoot(b)
    setSettingsRaw(b.settings)
    api.get<User[]>(`${V1}/users`).then(setUsers).catch(() => {})
  }, [])
  const setSettings = useCallback((s: Settings) => {
    setSettingsRaw(s)
    setBoot(b => {
      if (b) setUnits(s.distance_unit, s.timezone || b.timezone)
      return b ? { ...b, settings: s } : b
    })
  }, [])
  const refresh = useCallback(async () => {
    const st = await api.get<AuthStatus>(`${V1}/auth/status`)
    setStatus(st)
    setUser(st.user)
    if (st.authenticated) await reloadBoot()
  }, [reloadBoot])
  useEffect(() => { refresh().catch(() => setStatus({ needs_setup: false, auth_mode: 'local', authenticated: false, user: null, version: '', title: 'Pocket Movement' })) }, [refresh])
  useEffect(() => {
    const h = () => { setUser(null); setStatus(s => s ? { ...s, authenticated: false, user: null } : s) }
    window.addEventListener('pm:unauthorized', h)
    return () => window.removeEventListener('pm:unauthorized', h)
  }, [])
  return <Ctx.Provider value={{ status, user, boot, settings, users, themeMode, theme, setThemeMode, refresh, reloadBoot, setUser, setSettings }}>{children}</Ctx.Provider>
}
