import { ReactNode, useEffect, useRef, useState } from 'react'
import type { WhoAmI } from '../api/client'
import { getAuthToken } from '../api/client'
import ThemeToggle from '../components/ThemeToggle'
import { Button } from '../ui/primitives'
import './AppShell.css'

// NavItem describes one sidebar destination. requiresAdmin hides it from
// non-admins, so the nav is permission-aware rather than showing links a user can't use.
export interface NavItem {
  id: string
  label: string
  icon: string
  requiresAdmin?: boolean
}

interface AppShellProps {
  nav: NavItem[]
  activeId: string
  onNavigate: (id: string) => void
  title: string
  orgName?: string
  currentUser: string
  isAdmin: boolean
  profile: WhoAmI | null
  needsAuth: boolean
  authInput: string
  authError: string
  onAuthInput: (v: string) => void
  onLogin: (e: React.FormEvent) => void
  onLogout: () => void
  onBrandClick: () => void
  children: ReactNode
}

export default function AppShell(props: AppShellProps) {
  const {
    nav, activeId, onNavigate, orgName, currentUser, isAdmin, profile,
    needsAuth, authInput, authError, onAuthInput, onLogin, onLogout, onBrandClick, children,
  } = props

  const [showProfile, setShowProfile] = useState(false)
  const profileRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!showProfile) return
    const onPointerDown = (e: MouseEvent) => {
      if (profileRef.current && !profileRef.current.contains(e.target as Node)) setShowProfile(false)
    }
    const onKeyDown = (e: KeyboardEvent) => { if (e.key === 'Escape') setShowProfile(false) }
    document.addEventListener('mousedown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [showProfile])

  const visibleNav = nav.filter((n) => !n.requiresAdmin || isAdmin)
  const display = profile?.name || currentUser
  const initial = (display || '?').charAt(0).toUpperCase()

  return (
    <div className="shell">
      <header className="shell-topbar">
        <button className="shell-brand" onClick={onBrandClick} aria-label="vibeD home">
          <img src="/logo.png" alt="" className="shell-brand-img" />
          <span>vibeD</span>
        </button>
        <nav className="shell-nav" aria-label="Primary">
          {visibleNav.map((item) => (
            <button
              key={item.id}
              className={`shell-nav-item${item.id === activeId ? ' shell-nav-item-active' : ''}`}
              onClick={() => onNavigate(item.id)}
              aria-current={item.id === activeId ? 'page' : undefined}
            >
              <span className="shell-nav-icon" aria-hidden="true">{item.icon}</span>
              <span className="shell-nav-label">{item.label}</span>
            </button>
          ))}
        </nav>
        <div className="shell-topbar-spacer" />
        {orgName && (
          <span className="shell-org" title="Organization">
            <span className="shell-org-dot" aria-hidden="true" />
            {orgName}
          </span>
        )}
        <ThemeToggle />
        {currentUser && (
          <div className="shell-profile" ref={profileRef}>
            <button className="shell-profile-trigger" onClick={() => setShowProfile((v) => !v)} aria-haspopup="menu" aria-expanded={showProfile}>
              <span className="shell-avatar" aria-hidden="true">{initial}</span>
              <span className="shell-profile-name">{display}</span>
            </button>
            {showProfile && (
              <div className="shell-profile-card" role="menu">
                <div className="shell-profile-card-header">
                  <span className="shell-avatar" aria-hidden="true">{initial}</span>
                  <div>
                    <span className="shell-profile-card-name">{display}</span>
                    {profile?.email && <span className="shell-profile-card-email">{profile.email}</span>}
                  </div>
                </div>
                <div className="shell-profile-rows">
                  <div className="shell-profile-row"><span className="shell-profile-label">Role</span><span>{profile?.role || (isAdmin ? 'admin' : 'user')}</span></div>
                  <div className="shell-profile-row"><span className="shell-profile-label">Status</span><span>{profile?.status || 'active'}</span></div>
                  {profile?.provider && (
                    <div className="shell-profile-row"><span className="shell-profile-label">Provider</span><span>{profile.provider}</span></div>
                  )}
                  <div className="shell-profile-row"><span className="shell-profile-label">ID</span><span className="shell-profile-id">{profile?.id || profile?.user_id}</span></div>
                </div>
                {getAuthToken() && (
                  <div className="shell-profile-footer">
                    <Button size="sm" variant="ghost" onClick={onLogout}>Sign out</Button>
                  </div>
                )}
              </div>
            )}
          </div>
        )}
        {needsAuth && !currentUser && (
          <form className="shell-auth" onSubmit={onLogin}>
            <input className="ui-input" type="password" placeholder="API key" value={authInput} onChange={(e) => onAuthInput(e.target.value)} aria-label="API key" />
            <Button variant="primary" size="sm" type="submit">Sign in</Button>
            {authError && <span className="shell-auth-error" role="alert">{authError}</span>}
          </form>
        )}
      </header>

      <main className="shell-content">{children}</main>
    </div>
  )
}
