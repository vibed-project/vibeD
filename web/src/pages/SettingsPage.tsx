import { useEffect, useState } from 'react'
import { TargetInfo, fetchTargets } from '../api/client'
import DeploymentTargets from '../components/DeploymentTargets'
import AdminPanel from '../components/AdminPanel'

interface Props {
  currentUser: string
  isAdmin: boolean
  onBack: () => void
}

/**
 * SettingsPage hosts operator/admin surfaces lifted off the main dashboard:
 * deployment-target inventory and (when the caller is an admin) user +
 * department management. Non-admins see only the target list. Loads its own
 * target data so the dashboard's polling/SSE loop doesn't need to know
 * about this page.
 */
export default function SettingsPage({ currentUser, isAdmin, onBack }: Props) {
  const [targets, setTargets] = useState<TargetInfo[]>([])
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    fetchTargets()
      .then(setTargets)
      .catch((err) => setError(err instanceof Error ? err.message : 'Failed to load targets'))
  }, [])

  return (
    <main className="main">
      <div className="page-header">
        <button className="back-btn" onClick={onBack} aria-label="Back to dashboard">← Back</button>
        <h1 className="page-title">Settings</h1>
      </div>

      {error && (
        <div className="error-banner">
          {error}
          <button onClick={() => setError(null)}>Dismiss</button>
        </div>
      )}

      <section className="section">
        <DeploymentTargets targets={targets} />
      </section>

      {isAdmin && (
        <section className="section">
          <AdminPanel currentUser={currentUser} />
        </section>
      )}

      {!isAdmin && (
        <section className="section">
          <p className="page-note">
            User management is available to administrators only.
          </p>
        </section>
      )}
    </main>
  )
}
