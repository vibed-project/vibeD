import { useState, useEffect, useCallback, useRef } from 'react'
import {
  ArtifactSummary,
  WhoAmI,
  fetchArtifacts,
  fetchArtifact,
  deleteArtifact,
  fetchWhoami,
  fetchOrganization,
  subscribeToEvents,
  phaseToStatus,
  setAuthToken,
  clearAuthToken,
} from './api/client'
import ArtifactList from './components/ArtifactList'
import ApplicationsTable from './components/ApplicationsTable'
import LogViewer from './components/LogViewer'
import VersionHistory from './components/VersionHistory'
import ShareDialog from './components/ShareDialog'
import ShareLinkPage from './components/ShareLinkPage'
import SettingsPage from './pages/SettingsPage'
import ConnectPage from './pages/ConnectPage'
import AppShell, { NavItem } from './shell/AppShell'
import { Button, Spinner, EmptyState, ErrorState } from './ui/primitives'
import { useToast } from './ui/toast'
import './App.css'

// Detect public share link route: /share/<token> or /api/share/<token>
function getShareToken(): string | null {
  const m = window.location.pathname.match(/^\/(?:api\/)?share\/([a-f0-9]{64})$/)
  return m ? m[1] : null
}

// Top-level dashboard routes. The share-link path above takes precedence
// because it renders a standalone, auth-free page. Everything else maps
// pathname → route name; unknown paths fall through to the dashboard so
// stale bookmarks don't 404.
type Route = 'dashboard' | 'settings' | 'connect'
function routeFromPath(path: string): Route {
  if (path === '/settings' || path.startsWith('/settings/')) return 'settings'
  if (path === '/connect' || path.startsWith('/connect/')) return 'connect'
  return 'dashboard'
}
const ROUTE_PATHS: Record<Route, string> = {
  dashboard: '/',
  settings: '/settings',
  connect: '/connect',
}
const ROUTE_TITLES: Record<Route, string> = {
  dashboard: 'Applications',
  settings: 'Administration',
  connect: 'Connect',
}
const NAV: NavItem[] = [
  { id: 'dashboard', label: 'Applications', icon: '▦' },
  { id: 'connect', label: 'Connect', icon: '⚡' },
  { id: 'settings', label: 'Administration', icon: '⚙', requiresAdmin: true },
]

function App() {
  const toast = useToast()

  // Public share link route — render standalone page, no auth/nav needed
  const shareToken = getShareToken()
  if (shareToken) {
    return <ShareLinkPage token={shareToken} />
  }

  const [route, setRoute] = useState<Route>(() => routeFromPath(window.location.pathname))
  const navigate = useCallback((next: Route) => {
    if (next === route) return
    window.history.pushState({}, '', ROUTE_PATHS[next])
    setRoute(next)
  }, [route])
  useEffect(() => {
    const onPop = () => setRoute(routeFromPath(window.location.pathname))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const [artifacts, setArtifacts] = useState<ArtifactSummary[]>([])
  const [selectedArtifactId, setSelectedArtifactId] = useState<string | null>(null)
  const [versionArtifactId, setVersionArtifactId] = useState<string | null>(null)
  const [shareArtifactId, setShareArtifactId] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [currentUser, setCurrentUser] = useState<string>('')
  const [isAdmin, setIsAdmin] = useState(false)
  const [profile, setProfile] = useState<WhoAmI | null>(null)
  const [orgName, setOrgName] = useState<string>('')
  const [totalArtifacts, setTotalArtifacts] = useState(0)
  const [needsAuth, setNeedsAuth] = useState(false)
  const [authInput, setAuthInput] = useState('')
  const [authError, setAuthError] = useState('')
  const [viewMode, setViewMode] = useState<'table' | 'cards'>(() =>
    (localStorage.getItem('vibed_apps_view') as 'table' | 'cards') || 'table',
  )
  const [statusFilter, setStatusFilter] = useState('all')
  const setView = useCallback((v: 'table' | 'cards') => {
    setViewMode(v)
    localStorage.setItem('vibed_apps_view', v)
  }, [])

  const applyIdentity = useCallback((info: WhoAmI) => {
    setCurrentUser(info.id || info.user_id)
    setIsAdmin(info.role === 'admin')
    setProfile(info)
    setNeedsAuth(false)
  }, [])

  const initIdentity = useCallback(() => {
    fetchWhoami()
      .then(applyIdentity)
      .catch((err) => {
        // Key off the numeric status, not the message: Response.statusText is
        // empty over HTTP/2 so the old string match never fired behind an HTTP/2
        // gateway, leaving the login form hidden (issue #41). The string checks
        // stay as a fallback for any error that doesn't carry a status.
        if (err?.status === 401 || err?.message?.includes('401') || err?.message?.includes('Unauthorized')) {
          setNeedsAuth(true)
        }
        // Auth may be disabled — that's fine
      })
  }, [applyIdentity])

  const handleLogin = (e: React.FormEvent) => {
    e.preventDefault()
    if (!authInput.trim()) return
    setAuthToken(authInput.trim())
    setAuthError('')
    fetchWhoami()
      .then((info) => {
        applyIdentity(info)
        toast.success('Signed in')
      })
      .catch(() => {
        clearAuthToken()
        setAuthError('Invalid API key')
      })
  }

  const handleLogout = () => {
    clearAuthToken()
    setCurrentUser('')
    setIsAdmin(false)
    setProfile(null)
    setNeedsAuth(true)
    if (route !== 'dashboard') navigate('dashboard')
  }

  // Fetch user identity and org info on mount
  useEffect(() => {
    initIdentity()
    fetchOrganization()
      .then((org) => setOrgName(org.name))
      .catch(() => {
        // Organization may not be configured
      })
  }, [initIdentity])

  const loadData = useCallback(async () => {
    try {
      setLoading(true)
      setLoadError(null)
      const result = await fetchArtifacts()
      setArtifacts(result.artifacts)
      setTotalArtifacts(result.total)
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : 'Failed to load applications')
    } finally {
      setLoading(false)
    }
  }, [])

  const loadMore = useCallback(async () => {
    try {
      const result = await fetchArtifacts(undefined, artifacts.length)
      setArtifacts((prev) => [...prev, ...result.artifacts])
      setTotalArtifacts(result.total)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to load more')
    }
  }, [artifacts.length, toast])

  const handleDelete = useCallback(async (id: string) => {
    try {
      await deleteArtifact(id)
      setArtifacts((prev) => prev.filter((a) => a.id !== id))
      toast.success('Application deleted')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delete failed')
    }
  }, [toast])

  // Mirror of artifacts for the SSE handler below: the subscription effect
  // runs once, so its closure would otherwise see a stale list when deciding
  // whether an event's app is already known.
  const artifactsRef = useRef<ArtifactSummary[]>([])
  useEffect(() => {
    artifactsRef.current = artifacts
  }, [artifacts])

  useEffect(() => {
    loadData()

    // Subscribe to real-time SSE events; fall back to polling on failure
    let fallbackInterval: ReturnType<typeof setInterval> | null = null

    const es = subscribeToEvents(
      (event) => {
        if (event.type === 'artifact.deleted') {
          setArtifacts((prev) => prev.filter((a) => a.id !== event.artifact_id))
          return
        }
        // Enriched bridge events (name + raw VibedApp phase) carry enough to
        // update a known artifact in place — no per-event refetch. Legacy
        // orchestrator events lack these fields and keep the refetch path;
        // unknown apps also refetch once, since the event has no lane/created
        // info to build a faithful list entry from.
        const known = artifactsRef.current.some((a) => a.id === event.artifact_id)
        if (event.name && event.phase && known) {
          const status = phaseToStatus(event.phase)
          setArtifacts((prev) =>
            prev.map((a) =>
              a.id === event.artifact_id
                ? {
                    ...a,
                    name: event.name ?? a.name,
                    status,
                    url: event.url ?? a.url,
                    updated_at: event.timestamp,
                  }
                : a,
            ),
          )
          return
        }
        // Refetch the single changed artifact for full data
        fetchArtifact(event.artifact_id)
          .then((updated) => {
            setArtifacts((prev) => {
              const idx = prev.findIndex((a) => a.id === event.artifact_id)
              if (idx >= 0) {
                const next = [...prev]
                next[idx] = updated
                return next
              }
              // New artifact — add to list
              return [...prev, updated]
            })
          })
          .catch(() => loadData()) // Full reload on fetch failure
      },
      () => {
        // SSE connection error — EventSource auto-reconnects, but
        // start polling as a fallback in case reconnect fails
        if (!fallbackInterval) {
          fallbackInterval = setInterval(loadData, 5000)
        }
      },
    )

    // If SSE reconnects successfully, stop the polling fallback
    es.onopen = () => {
      if (fallbackInterval) {
        clearInterval(fallbackInterval)
        fallbackInterval = null
      }
    }

    return () => {
      es.close()
      if (fallbackInterval) clearInterval(fallbackInterval)
    }
  }, [loadData])

  return (
    <AppShell
      nav={NAV}
      activeId={route}
      onNavigate={(id) => navigate(id as Route)}
      title={ROUTE_TITLES[route]}
      orgName={orgName}
      currentUser={currentUser}
      isAdmin={isAdmin}
      profile={profile}
      needsAuth={needsAuth}
      authInput={authInput}
      authError={authError}
      onAuthInput={setAuthInput}
      onLogin={handleLogin}
      onLogout={handleLogout}
      onBrandClick={() => navigate('dashboard')}
    >
      {route === 'settings' && (
        <SettingsPage currentUser={currentUser} isAdmin={isAdmin} onBack={() => navigate('dashboard')} />
      )}

      {route === 'connect' && <ConnectPage onBack={() => navigate('dashboard')} />}

      {route === 'dashboard' && (
        <section className="section">
          <div className="section-header">
            <h2 className="section-title">
              Applications
              <span className="count">{artifacts.length}</span>
            </h2>
            <div style={{ display: 'inline-flex', gap: 'var(--space-2)' }}>
              <div className="view-toggle" role="group" aria-label="View mode">
                <Button size="sm" variant={viewMode === 'table' ? 'primary' : 'ghost'} onClick={() => setView('table')} aria-pressed={viewMode === 'table'}>Table</Button>
                <Button size="sm" variant={viewMode === 'cards' ? 'primary' : 'ghost'} onClick={() => setView('cards')} aria-pressed={viewMode === 'cards'}>Cards</Button>
              </div>
              <Button size="sm" onClick={loadData} disabled={loading}>
                {loading ? <Spinner label="Refreshing" /> : 'Refresh'}
              </Button>
            </div>
          </div>

          {loading && artifacts.length === 0 ? (
            <div className="ui-state"><Spinner label="Loading applications" /></div>
          ) : loadError ? (
            <ErrorState title="Couldn't load applications" description={loadError} onRetry={loadData} />
          ) : artifacts.length === 0 ? (
            <EmptyState
              icon="▦"
              title="No applications yet"
              description="Deploy an app through the vibeD MCP server and it will appear here in real time."
              action={<Button size="sm" onClick={() => navigate('connect')}>How to connect</Button>}
            />
          ) : viewMode === 'table' ? (
            <ApplicationsTable
              artifacts={statusFilter === 'all' ? artifacts : artifacts.filter((a) => a.status === statusFilter)}
              currentUser={currentUser}
              isAdmin={isAdmin}
              statusFilter={statusFilter}
              onStatusFilter={setStatusFilter}
              onViewLogs={(id) => setSelectedArtifactId(id)}
              onViewVersions={(id) => setVersionArtifactId(id)}
              onShare={(id) => setShareArtifactId(id)}
              onDelete={handleDelete}
            />
          ) : (
            <>
              <ArtifactList
                artifacts={artifacts}
                currentUser={currentUser}
                isAdmin={isAdmin}
                onViewLogs={(id) => setSelectedArtifactId(id)}
                onViewVersions={(id) => setVersionArtifactId(id)}
                onShare={(id) => setShareArtifactId(id)}
                onDelete={handleDelete}
              />
              {artifacts.length < totalArtifacts && (
                <div className="load-more-wrap">
                  <Button onClick={loadMore}>
                    Load more ({artifacts.length} of {totalArtifacts})
                  </Button>
                </div>
              )}
            </>
          )}
        </section>
      )}

      {selectedArtifactId && (
        <LogViewer artifactId={selectedArtifactId} onClose={() => setSelectedArtifactId(null)} />
      )}

      {versionArtifactId && (
        <VersionHistory
          artifactId={versionArtifactId}
          onClose={() => setVersionArtifactId(null)}
          onRollbackComplete={loadData}
        />
      )}

      {shareArtifactId && (
        <ShareDialog artifactId={shareArtifactId} onClose={() => setShareArtifactId(null)} />
      )}
    </AppShell>
  )
}

export default App
