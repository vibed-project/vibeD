import { useState, useEffect, useCallback } from 'react'
import {
  ArtifactSummary,
  TargetInfo,
  WhoAmI,
  fetchArtifacts,
  fetchArtifact,
  fetchTargets,
  deleteArtifact,
  fetchWhoami,
  fetchOrganization,
  subscribeToEvents,
  getAuthToken,
  setAuthToken,
  clearAuthToken,
} from './api/client'
import ArtifactList from './components/ArtifactList'
import DeploymentTargets from './components/DeploymentTargets'
import LogViewer from './components/LogViewer'
import VersionHistory from './components/VersionHistory'
import ShareDialog from './components/ShareDialog'
import AdminPanel from './components/AdminPanel'
import SetupGuide from './components/SetupGuide'
import ShareLinkPage from './components/ShareLinkPage'
import './App.css'

// Detect public share link route: /share/<token> or /api/share/<token>
function getShareToken(): string | null {
  const m = window.location.pathname.match(/^\/(?:api\/)?share\/([a-f0-9]{64})$/)
  return m ? m[1] : null
}

function App() {
  // Public share link route — render standalone page, no auth/nav needed
  const shareToken = getShareToken()
  if (shareToken) {
    return <ShareLinkPage token={shareToken} />
  }

  const [artifacts, setArtifacts] = useState<ArtifactSummary[]>([])
  const [targets, setTargets] = useState<TargetInfo[]>([])
  const [selectedArtifactId, setSelectedArtifactId] = useState<string | null>(null)
  const [versionArtifactId, setVersionArtifactId] = useState<string | null>(null)
  const [shareArtifactId, setShareArtifactId] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [currentUser, setCurrentUser] = useState<string>('')
  const [isAdmin, setIsAdmin] = useState(false)
  const [profile, setProfile] = useState<WhoAmI | null>(null)
  const [showProfile, setShowProfile] = useState(false)
  const [orgName, setOrgName] = useState<string>('')
  const [totalArtifacts, setTotalArtifacts] = useState(0)
  const [needsAuth, setNeedsAuth] = useState(false)
  const [authInput, setAuthInput] = useState('')
  const [authError, setAuthError] = useState('')

  const initIdentity = useCallback(() => {
    fetchWhoami()
      .then((info) => {
        setCurrentUser(info.id || info.user_id)
        setIsAdmin(info.role === 'admin')
        setProfile(info)
        setNeedsAuth(false)
      })
      .catch((err) => {
        if (err?.message?.includes('401') || err?.message?.includes('Unauthorized')) {
          setNeedsAuth(true)
        }
        // Auth may be disabled — that's fine
      })
  }, [])

  const handleLogin = (e: React.FormEvent) => {
    e.preventDefault()
    if (!authInput.trim()) return
    setAuthToken(authInput.trim())
    setAuthError('')
    fetchWhoami()
      .then((info) => {
        setCurrentUser(info.id || info.user_id)
        setIsAdmin(info.role === 'admin')
        setProfile(info)
        setNeedsAuth(false)
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
  }

  // Fetch user identity and org info on mount
  useEffect(() => {
    initIdentity()

    fetchOrganization()
      .then((org) => setOrgName(org.name))
      .catch(() => {
        // Organization may not be configured
      })
  }, [])

  const loadData = useCallback(async () => {
    try {
      setLoading(true)
      setError(null)
      const [result, tgts] = await Promise.all([fetchArtifacts(), fetchTargets()])
      setArtifacts(result.artifacts)
      setTotalArtifacts(result.total)
      setTargets(tgts)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load data')
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
      setError(err instanceof Error ? err.message : 'Failed to load more')
    }
  }, [artifacts.length])

  const handleDelete = useCallback(async (id: string) => {
    await deleteArtifact(id)
    setArtifacts((prev) => prev.filter((a) => a.id !== id))
  }, [])

  useEffect(() => {
    loadData()

    // Subscribe to real-time SSE events; fall back to polling on failure
    let fallbackInterval: ReturnType<typeof setInterval> | null = null

    const es = subscribeToEvents(
      (event) => {
        if (event.type === 'artifact.deleted') {
          setArtifacts((prev) => prev.filter((a) => a.id !== event.artifact_id))
        } else {
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
        }
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
    <div className="app">
      <header className="header">
        <div className="header-left">
          <h1 className="logo">
            <img src="/logo.png" alt="vibeD" className="logo-img" />
            vibeD
          </h1>
          <span className="subtitle">Workload Orchestrator</span>
          {orgName && <span className="org-badge">{orgName}</span>}
        </div>
        <div className="header-right">
          {currentUser && (
            <div className="profile-wrapper">
              <button className="profile-trigger" onClick={() => setShowProfile(!showProfile)}>
                <span className="profile-avatar">
                  {(profile?.name || currentUser).charAt(0).toUpperCase()}
                </span>
                <span className="profile-name">{profile?.name || currentUser}</span>
                {isAdmin && <span className="admin-badge">admin</span>}
              </button>
              {showProfile && (
                <div className="profile-card">
                  <div className="profile-card-header">
                    <span className="profile-card-avatar">
                      {(profile?.name || currentUser).charAt(0).toUpperCase()}
                    </span>
                    <div className="profile-card-identity">
                      <span className="profile-card-name">{profile?.name || currentUser}</span>
                      {profile?.email && <span className="profile-card-email">{profile.email}</span>}
                    </div>
                  </div>
                  <div className="profile-card-details">
                    <div className="profile-card-row">
                      <span className="profile-card-label">Role</span>
                      <span className={`profile-card-role ${isAdmin ? 'profile-card-role-admin' : ''}`}>
                        {profile?.role || 'user'}
                      </span>
                    </div>
                    <div className="profile-card-row">
                      <span className="profile-card-label">Status</span>
                      <span className="profile-card-status">{profile?.status || 'active'}</span>
                    </div>
                    {profile?.provider && (
                      <div className="profile-card-row">
                        <span className="profile-card-label">Provider</span>
                        <span className="profile-card-value">{profile.provider}</span>
                      </div>
                    )}
                    <div className="profile-card-row">
                      <span className="profile-card-label">ID</span>
                      <span className="profile-card-value profile-card-id">{profile?.id || profile?.user_id}</span>
                    </div>
                  </div>
                  {getAuthToken() && (
                    <div className="profile-card-footer">
                      <button className="profile-logout-btn" onClick={handleLogout}>Sign out</button>
                    </div>
                  )}
                </div>
              )}
            </div>
          )}
          {needsAuth && !currentUser && (
            <form className="auth-inline" onSubmit={handleLogin}>
              <input
                className="auth-input"
                type="password"
                placeholder="API Key"
                value={authInput}
                onChange={(e) => setAuthInput(e.target.value)}
              />
              <button className="auth-submit" type="submit">Sign in</button>
              {authError && <span className="auth-error">{authError}</span>}
            </form>
          )}
          <button className="refresh-btn" onClick={loadData} disabled={loading}>
            {loading ? 'Loading...' : 'Refresh'}
          </button>
        </div>
      </header>

      {error && (
        <div className="error-banner">
          {error}
          <button onClick={() => setError(null)}>Dismiss</button>
        </div>
      )}

      <main className="main">
        <section className="section">
          <SetupGuide />
        </section>

        <section className="section">
          <DeploymentTargets targets={targets} />
        </section>

        {isAdmin && (
          <section className="section">
            <AdminPanel currentUser={currentUser} />
          </section>
        )}

        <section className="section">
          <h2 className="section-title">
            Deployed Artifacts
            <span className="count">{artifacts.length}</span>
          </h2>
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
            <button className="load-more-btn" onClick={loadMore}>
              Load more ({artifacts.length} of {totalArtifacts})
            </button>
          )}
        </section>
      </main>

      {selectedArtifactId && (
        <LogViewer
          artifactId={selectedArtifactId}
          onClose={() => setSelectedArtifactId(null)}
        />
      )}

      {versionArtifactId && (
        <VersionHistory
          artifactId={versionArtifactId}
          onClose={() => setVersionArtifactId(null)}
          onRollbackComplete={loadData}
        />
      )}

      {shareArtifactId && (
        <ShareDialog
          artifactId={shareArtifactId}
          onClose={() => setShareArtifactId(null)}
          onShareComplete={loadData}
        />
      )}
    </div>
  )
}

export default App
