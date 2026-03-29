import { TargetInfo } from '../api/client'
import './DeploymentTargets.css'

interface Props {
  targets: TargetInfo[]
}

export default function DeploymentTargets({ targets }: Props) {
  if (targets.length === 0) return null

  return (
    <div className="targets">
      <h2 className="section-title">Deployment Targets</h2>
      <div className="targets-grid">
        {targets.map((target) => (
          <div
            key={target.name}
            className={`target-card ${target.available ? 'available' : 'unavailable'}`}
          >
            <div className="target-header">
              <span className={`target-indicator ${target.available ? 'on' : 'off'}`} />
              <span className="target-name">{formatTarget(target.name)}</span>
              {target.preferred && <span className="target-preferred">preferred</span>}
            </div>
            <p className="target-desc">{target.description}</p>
          </div>
        ))}
      </div>
    </div>
  )
}

function formatTarget(name: string): string {
  switch (name) {
    case 'knative': return 'Knative Serving'
    case 'kubernetes': return 'Kubernetes'
    default: return name
  }
}
