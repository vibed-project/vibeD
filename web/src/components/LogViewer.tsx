import { useState, useEffect, useRef } from 'react'
import { fetchLogs, followLogs } from '../api/client'
import './LogViewer.css'

interface Props {
  artifactId: string
  onClose: () => void
}

export default function LogViewer({ artifactId, onClose }: Props) {
  const [logs, setLogs] = useState<string[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const logsEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let mounted = true
    let pollInterval: ReturnType<typeof setInterval> | null = null
    const controller = new AbortController()

    // Snapshot-polling path, kept as the fallback for when the live stream
    // can't be established or drops (older server, proxy buffering, pod gone).
    async function loadLogs() {
      try {
        setLoading(true)
        setError(null)
        const data = await fetchLogs(artifactId)
        if (mounted) {
          setLogs(data.logs ?? [])
        }
      } catch (err) {
        if (mounted) {
          setError(err instanceof Error ? err.message : 'Failed to load logs')
        }
      } finally {
        if (mounted) setLoading(false)
      }
    }

    function startPolling() {
      if (!mounted || pollInterval) return
      loadLogs()
      pollInterval = setInterval(loadLogs, 3000)
    }

    // Live tail: follow=true seeds the recent tail then streams new lines,
    // so no interval is needed while the stream is healthy.
    setLogs([])
    followLogs(
      artifactId,
      (line) => {
        if (mounted) setLogs((prev) => [...prev, line])
      },
      {
        signal: controller.signal,
        onOpen: () => {
          if (mounted) {
            setLoading(false)
            setError(null)
          }
        },
      },
    )
      // Stream over (server closed it) or failed (HTTP error / abort):
      // fall back to the 3s polling path. startPolling no-ops after unmount.
      .then(() => startPolling())
      .catch(() => startPolling())

    return () => {
      mounted = false
      controller.abort()
      if (pollInterval) clearInterval(pollInterval)
    }
  }, [artifactId])

  useEffect(() => {
    logsEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [logs])

  return (
    <div className="log-overlay" onClick={onClose}>
      <div className="log-panel" onClick={(e) => e.stopPropagation()}>
        <div className="log-header">
          <h3>Logs: {artifactId.substring(0, 12)}</h3>
          <button className="log-close" onClick={onClose}>&times;</button>
        </div>
        <div className="log-content">
          {loading && logs.length === 0 && <div className="log-loading">Loading logs...</div>}
          {error && <div className="log-error">{error}</div>}
          {logs.length === 0 && !loading && !error && (
            <div className="log-empty">No logs available (service may be scaled to zero)</div>
          )}
          {logs.map((line, i) => (
            <div key={i} className="log-line">
              <span className="log-num">{i + 1}</span>
              <span className="log-text">{line}</span>
            </div>
          ))}
          <div ref={logsEndRef} />
        </div>
      </div>
    </div>
  )
}
