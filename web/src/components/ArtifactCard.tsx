import { useState } from 'react'
import { ArtifactSummary } from '../api/client'
import './ArtifactCard.css'

interface Props {
  artifact: ArtifactSummary
  onViewLogs: () => void
  onDelete: () => Promise<void>
}

const statusConfig: Record<string, { color: string; label: string }> = {
  running: { color: 'var(--green)', label: 'Running' },
  building: { color: 'var(--yellow)', label: 'Building' },
  deploying: { color: 'var(--blue)', label: 'Deploying' },
  pending: { color: 'var(--text-muted)', label: 'Pending' },
  failed: { color: 'var(--red)', label: 'Failed' },
  deleted: { color: 'var(--text-muted)', label: 'Deleted' },
}

const targetLabels: Record<string, string> = {
  knative: 'Knative',
  kubernetes: 'Kubernetes',
  wasmcloud: 'wasmCloud',
}

function timeAgo(dateStr: string): string {
  const seconds = Math.floor((Date.now() - new Date(dateStr).getTime()) / 1000)
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

export default function ArtifactCard({ artifact, onViewLogs, onDelete }: Props) {
  const status = statusConfig[artifact.status] ?? statusConfig.pending
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const handleDelete = async () => {
    setDeleting(true)
    try {
      await onDelete()
    } catch {
      setDeleting(false)
      setConfirmDelete(false)
    }
  }

  return (
    <div className="artifact-card">
      <div className="card-header">
        <div className="card-title-row">
          <h3 className="card-name">{artifact.name}</h3>
          <span className="status-badge" style={{ color: status.color, borderColor: status.color }}>
            <span className="status-dot" style={{ background: status.color }} />
            {status.label}
          </span>
        </div>
        <span className="card-target">{targetLabels[artifact.target] ?? artifact.target}</span>
      </div>

      <div className="card-body">
        {artifact.url ? (
          <a href={artifact.url} target="_blank" rel="noopener noreferrer" className="card-url">
            {artifact.url}
          </a>
        ) : (
          <span className="card-url-pending">
            {artifact.status === 'building' ? 'Building...' :
             artifact.status === 'deploying' ? 'Deploying...' :
             artifact.status === 'failed' ? 'Deployment failed' :
             'Waiting...'}
          </span>
        )}
      </div>

      <div className="card-footer">
        <span className="card-time">Updated {timeAgo(artifact.updated_at)}</span>
        <div className="card-actions">
          {confirmDelete ? (
            <div className="confirm-delete">
              <span className="confirm-text">Delete?</span>
              <button
                className="action-btn action-danger"
                onClick={handleDelete}
                disabled={deleting}
              >
                {deleting ? 'Deleting...' : 'Yes'}
              </button>
              <button
                className="action-btn"
                onClick={() => setConfirmDelete(false)}
                disabled={deleting}
              >
                No
              </button>
            </div>
          ) : (
            <>
              <button className="action-btn action-danger-outline" onClick={() => setConfirmDelete(true)}>
                Delete
              </button>
              <button className="action-btn" onClick={onViewLogs}>Logs</button>
              {artifact.url && (
                <a href={artifact.url} target="_blank" rel="noopener noreferrer" className="action-btn action-primary">
                  Open
                </a>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}
