import { useState, useEffect, useCallback } from 'react'
import {
  ArtifactSummary,
  TargetInfo,
  fetchArtifacts,
  fetchArtifact,
  fetchTargets,
  deleteArtifact,
  fetchWhoami,
  fetchOrganization,
  subscribeToEvents,
} from './api/client'
import ArtifactList from './components/ArtifactList'
import DeploymentTargets from './components/DeploymentTargets'
import LogViewer from './components/LogViewer'
import VersionHistory from './components/VersionHistory'
import ShareDialog from './components/ShareDialog'
import SetupGuide from './components/SetupGuide'
import './App.css'

function App() {
  const [artifacts, setArtifacts] = useState<ArtifactSummary[]>([])
  const [targets, setTargets] = useState<TargetInfo[]>([])
  const [selectedArtifactId, setSelectedArtifactId] = useState<string | null>(null)
  const [versionArtifactId, setVersionArtifactId] = useState<string | null>(null)
  const [shareArtifactId, setShareArtifactId] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [currentUser, setCurrentUser] = useState<string>('')
  const [isAdmin, setIsAdmin] = useState(false)
  const [orgName, setOrgName] = useState<string>('')

  // Fetch user identity and org info on mount
  useEffect(() => {
    fetchWhoami()
      .then((info) => {
        setCurrentUser(info.user_id)
        setIsAdmin(info.role === 'admin')
      })
      .catch(() => {
        // Auth may be disabled — that's fine
      })

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
      const [arts, tgts] = await Promise.all([fetchArtifacts(), fetchTargets()])
      setArtifacts(arts)
      setTargets(tgts)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load data')
    } finally {
      setLoading(false)
    }
  }, [])

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
            <span className="user-info">
              {isAdmin && <span className="admin-badge">admin</span>}
              {currentUser}
            </span>
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
