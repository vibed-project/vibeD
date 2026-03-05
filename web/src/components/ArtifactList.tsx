import { ArtifactSummary } from '../api/client'
import ArtifactCard from './ArtifactCard'
import './ArtifactList.css'

interface Props {
  artifacts: ArtifactSummary[]
  onViewLogs: (id: string) => void
  onDelete: (id: string) => Promise<void>
}

export default function ArtifactList({ artifacts, onViewLogs, onDelete }: Props) {
  if (artifacts.length === 0) {
    return (
      <div className="empty-state">
        <div className="empty-icon">&#128230;</div>
        <h3>No artifacts deployed</h3>
        <p>Deploy your first artifact using an MCP-compatible AI coding tool.</p>
        <code className="empty-hint">
          Use the <strong>deploy_artifact</strong> MCP tool to get started
        </code>
      </div>
    )
  }

  return (
    <div className="artifact-list">
      {artifacts.map((artifact) => (
        <ArtifactCard
          key={artifact.id}
          artifact={artifact}
          onViewLogs={() => onViewLogs(artifact.id)}
          onDelete={() => onDelete(artifact.id)}
        />
      ))}
    </div>
  )
}
