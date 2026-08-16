import AdminPanel from '../components/AdminPanel'

interface Props {
  currentUser: string
  isAdmin: boolean
  onBack: () => void
}

/**
 * SettingsPage hosts admin surfaces lifted off the main dashboard: when the
 * caller is an admin, user + department management. Non-admins see only a
 * note that management is admin-only.
 */
export default function SettingsPage({ currentUser, isAdmin, onBack }: Props) {
  return (
    <main className="main">
      <div className="page-header">
        <button className="back-btn" onClick={onBack} aria-label="Back to dashboard">← Back</button>
        <h1 className="page-title">Settings</h1>
      </div>

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
