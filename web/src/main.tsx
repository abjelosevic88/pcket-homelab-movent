import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import './styles/base.css'
import './styles/app.css'
import './styles/settings.css'
import './styles/movement.css'
import { AppProvider, useApp } from './lib/app-context'
import { Loading, ToastProvider } from './components/ui'
import Layout from './components/Layout'
import { ErrorBoundary } from './components/ErrorBoundary'
import Login from './pages/Login'
import MapPage from './pages/MapPage'
import Timeline from './pages/Timeline'
import Insights from './pages/Insights'
import Places from './pages/Places'
import PlaceDetail from './pages/PlaceDetail'
import Family from './pages/Family'
import Devices from './pages/Devices'
import Data from './pages/Data'
import Settings from './pages/Settings'
import SharedView from './pages/SharedView'

function Gate({ children }: { children: React.ReactNode }) {
  const { status, user, boot } = useApp()
  const loc = useLocation()
  if (!status) return <div className="boot"><img src="/favicon.svg" alt="" /><Loading /></div>
  if (status.needs_setup) return <Navigate to="/setup" replace />
  if (!user) return <Navigate to="/login" replace state={{ from: loc.pathname + loc.search }} />
  if (!boot) return <div className="boot"><img src="/favicon.svg" alt="" /><Loading /></div>
  return <>{children}</>
}

function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login mode="login" />} />
      <Route path="/setup" element={<Login mode="setup" />} />
      <Route path="/s/:token" element={<ErrorBoundary where="shared"><SharedView /></ErrorBoundary>} />
      <Route path="/settings/:tab?" element={<Gate><ErrorBoundary where="settings"><Settings /></ErrorBoundary></Gate>} />
      <Route element={<Gate><ErrorBoundary where="movement"><Layout /></ErrorBoundary></Gate>}>
        <Route path="/" element={<MapPage />} />
        <Route path="/timeline" element={<Timeline />} />
        <Route path="/timeline/:date" element={<Timeline />} />
        <Route path="/insights" element={<Insights />} />
        <Route path="/insights/:tab" element={<Insights />} />
        <Route path="/places" element={<Places />} />
        <Route path="/places/:id" element={<PlaceDetail />} />
        <Route path="/family" element={<Family />} />
        <Route path="/devices" element={<Devices />} />
        <Route path="/data" element={<Data />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  )
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <ErrorBoundary where="app"><ToastProvider><AppProvider><App /></AppProvider></ToastProvider></ErrorBoundary>
    </BrowserRouter>
  </React.StrictMode>,
)
