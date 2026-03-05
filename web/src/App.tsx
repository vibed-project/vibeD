import { useState, useEffect, useCallback } from 'react'
import { ArtifactSummary, TargetInfo, fetchArtifacts, fetchTargets, deleteArtifact } from './api/client'
import ArtifactList from './components/ArtifactList'
import DeploymentTargets from './components/DeploymentTargets'
import LogViewer from './components/LogViewer'
import './App.css'

function App() {
  const [artifacts, setArtifacts] = useState<ArtifactSummary[]>([])
  const [targets, setTargets] = useState<TargetInfo[]>([])
  const [selectedArtifactId, setSelectedArtifactId] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const loadData = useCallback(async () => {
    try {
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
    const interval = setInterval(loadData, 5000) // Poll every 5s
    return () => clearInterval(interval)
  }, [loadData])

  return (
    <div className="app">
      <header className="header">
        <div className="header-left">
          <h1 className="logo">
            <span className="logo-icon">&#9889;</span> vibeD
          </h1>
          <span className="subtitle">Workload Orchestrator</span>
        </div>
        <button className="refresh-btn" onClick={loadData} disabled={loading}>
          {loading ? 'Loading...' : 'Refresh'}
        </button>
      </header>

      {error && (
        <div className="error-banner">
          {error}
          <button onClick={() => setError(null)}>Dismiss</button>
        </div>
      )}

      <main className="main">
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
            onViewLogs={(id) => setSelectedArtifactId(id)}
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
    </div>
  )
}

export default App
