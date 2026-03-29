import { useState, useEffect, useCallback } from 'react'
import {
  fetchArtifact,
  shareArtifact,
  unshareArtifact,
  createShareLink,
  listShareLinks,
  revokeShareLink,
  Artifact,
  ShareLink,
} from '../api/client'
import './ShareDialog.css'

interface Props {
  artifactId: string
  onClose: () => void
  onShareComplete: () => void
}

function generatePassword(length = 16): string {
  const chars = 'ABCDEFGHJKMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789!@#$%'
  const arr = new Uint8Array(length)
  crypto.getRandomValues(arr)
  return Array.from(arr).map((b) => chars[b % chars.length]).join('')
}

function CopyButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard.writeText(value).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }
  return (
    <button className="sd-copy-btn" onClick={copy} title="Copy">
      {copied ? '✓' : '⎘'}
    </button>
  )
}

export default function ShareDialog({ artifactId, onClose, onShareComplete }: Props) {
  const [artifact, setArtifact] = useState<Artifact | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // User-based sharing
  const [inputValue, setInputValue] = useState('')
  const [sharing, setSharing] = useState(false)
  const [removing, setRemoving] = useState<string | null>(null)

  // Share links
  const [links, setLinks] = useState<ShareLink[]>([])
  const [linksLoading, setLinksLoading] = useState(false)
  const [linkPassword, setLinkPassword] = useState('')
  const [linkExpiry, setLinkExpiry] = useState('')
  const [creating, setCreating] = useState(false)
  const [newLink, setNewLink] = useState<ShareLink | null>(null)
  const [revoking, setRevoking] = useState<string | null>(null)
  const [showPassword, setShowPassword] = useState(false)

  const loadLinks = useCallback(async () => {
    setLinksLoading(true)
    try {
      const data = await listShareLinks(artifactId)
      setLinks(data ?? [])
    } catch {
      // share links may not be available if store is not SQLite
    } finally {
      setLinksLoading(false)
    }
  }, [artifactId])

  useEffect(() => {
    let mounted = true
    async function load() {
      try {
        setLoading(true)
        setError(null)
        const [data] = await Promise.all([fetchArtifact(artifactId), loadLinks()])
        if (mounted) setArtifact(data)
      } catch (err) {
        if (mounted) setError(err instanceof Error ? err.message : 'Failed to load artifact')
      } finally {
        if (mounted) setLoading(false)
      }
    }
    load()
    return () => { mounted = false }
  }, [artifactId, loadLinks])

  const handleShare = async () => {
    const userIds = inputValue.split(',').map((s) => s.trim()).filter((s) => s.length > 0)
    if (userIds.length === 0) return
    setSharing(true)
    setError(null)
    try {
      await shareArtifact(artifactId, userIds)
      setInputValue('')
      const updated = await fetchArtifact(artifactId)
      setArtifact(updated)
      onShareComplete()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to share')
    } finally {
      setSharing(false)
    }
  }

  const handleRemove = async (userId: string) => {
    setRemoving(userId)
    setError(null)
    try {
      await unshareArtifact(artifactId, [userId])
      const updated = await fetchArtifact(artifactId)
      setArtifact(updated)
      onShareComplete()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to remove')
    } finally {
      setRemoving(null)
    }
  }

  const handleCreateLink = async () => {
    setCreating(true)
    setError(null)
    setNewLink(null)
    try {
      const link = await createShareLink(artifactId, linkPassword || undefined, linkExpiry || undefined)
      setNewLink(link)
      setLinkPassword('')
      setLinkExpiry('')
      await loadLinks()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create link')
    } finally {
      setCreating(false)
    }
  }

  const handleRevoke = async (token: string) => {
    setRevoking(token)
    try {
      await revokeShareLink(token)
      await loadLinks()
      if (newLink?.token === token) setNewLink(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to revoke link')
    } finally {
      setRevoking(null)
    }
  }

  const activeLinks = links.filter((l) => !l.revoked)
  const sharedWith = artifact?.shared_with ?? []

  return (
    <div className="sd-overlay" onClick={onClose}>
      <div className="sd-panel" onClick={(e) => e.stopPropagation()}>
        <div className="sd-header">
          <h3>Share Artifact</h3>
          <button className="sd-close" onClick={onClose}>&times;</button>
        </div>

        <div className="sd-content">
          {loading && <div className="sd-loading">Loading...</div>}
          {error && <div className="sd-error">{error}</div>}

          {!loading && artifact && (
            <>
              <div className="sd-artifact-name">{artifact.name}</div>

              {/* ── Share Links ── */}
              <div className="sd-section">
                <div className="sd-section-title">Public share links</div>

                {/* New link creation form */}
                <div className="sd-link-form">
                  <div className="sd-link-row">
                    <div className="sd-password-wrap">
                      <input
                        className="sd-input"
                        type={showPassword ? 'text' : 'password'}
                        placeholder="Password (optional)"
                        value={linkPassword}
                        onChange={(e) => setLinkPassword(e.target.value)}
                        disabled={creating}
                      />
                      <button
                        className="sd-eye-btn"
                        type="button"
                        onClick={() => setShowPassword((v) => !v)}
                        title={showPassword ? 'Hide' : 'Show'}
                      >{showPassword ? '🙈' : '👁'}</button>
                    </div>
                    <button
                      className="sd-gen-btn"
                      type="button"
                      onClick={() => { setLinkPassword(generatePassword()); setShowPassword(true) }}
                      disabled={creating}
                      title="Generate random password"
                    >Generate</button>
                  </div>

                  <div className="sd-link-row">
                    <select
                      className="sd-input sd-select"
                      value={linkExpiry}
                      onChange={(e) => setLinkExpiry(e.target.value)}
                      disabled={creating}
                    >
                      <option value="">No expiry</option>
                      <option value="1h">1 hour</option>
                      <option value="24h">24 hours</option>
                      <option value="7d">7 days</option>
                      <option value="30d">30 days</option>
                    </select>
                    <button
                      className="sd-share-btn"
                      onClick={handleCreateLink}
                      disabled={creating}
                    >
                      {creating ? 'Creating…' : 'Create link'}
                    </button>
                  </div>
                </div>

                {/* Newly created link highlight */}
                {newLink && (
                  <div className="sd-new-link">
                    <div className="sd-new-link-label">
                      ✓ Link created{newLink.has_password ? ' · password-protected' : ''}
                    </div>
                    <div className="sd-url-row">
                      <span className="sd-url-text">{newLink.url || `…/api/share/${newLink.token}`}</span>
                      <CopyButton value={newLink.url || newLink.token} />
                    </div>
                    {newLink.has_password && linkPassword === '' && (
                      <p className="sd-hint">Share the password separately — it is not stored.</p>
                    )}
                  </div>
                )}

                {/* Existing active links */}
                {linksLoading ? (
                  <div className="sd-empty">Loading links…</div>
                ) : activeLinks.length === 0 && !newLink ? (
                  <div className="sd-empty">No active share links</div>
                ) : (
                  <div className="sd-user-list">
                    {activeLinks.map((link) => (
                      <div key={link.token} className="sd-user-row">
                        <div className="sd-link-info">
                          <div className="sd-url-row">
                            <span className="sd-url-text sd-url-mono">
                              {link.url ? link.url : `…${link.token.slice(-8)}`}
                            </span>
                            <CopyButton value={link.url || link.token} />
                          </div>
                          <div className="sd-link-meta">
                            {link.has_password && <span className="sd-badge sd-badge-lock">🔒 protected</span>}
                            {link.expires_at && (
                              <span className="sd-badge">expires {new Date(link.expires_at).toLocaleDateString()}</span>
                            )}
                          </div>
                        </div>
                        <button
                          className="sd-remove-btn"
                          onClick={() => handleRevoke(link.token)}
                          disabled={revoking === link.token}
                        >
                          {revoking === link.token ? '…' : 'Revoke'}
                        </button>
                      </div>
                    ))}
                  </div>
                )}
              </div>

              {/* ── User-based sharing ── */}
              <div className="sd-section">
                <div className="sd-section-title">Shared with users</div>
                {sharedWith.length === 0 ? (
                  <div className="sd-empty">Not shared with anyone</div>
                ) : (
                  <div className="sd-user-list">
                    {sharedWith.map((uid) => (
                      <div key={uid} className="sd-user-row">
                        <span className="sd-user-name">{uid}</span>
                        <span className="sd-user-perm">read-only</span>
                        <button
                          className="sd-remove-btn"
                          onClick={() => handleRemove(uid)}
                          disabled={removing === uid}
                        >
                          {removing === uid ? '…' : 'Remove'}
                        </button>
                      </div>
                    ))}
                  </div>
                )}
                <div className="sd-input-row" style={{ marginTop: '0.5rem' }}>
                  <input
                    className="sd-input"
                    type="text"
                    placeholder="User IDs (comma-separated)"
                    value={inputValue}
                    onChange={(e) => setInputValue(e.target.value)}
                    onKeyDown={(e) => { if (e.key === 'Enter' && !sharing) handleShare() }}
                    disabled={sharing}
                  />
                  <button
                    className="sd-share-btn"
                    onClick={handleShare}
                    disabled={sharing || inputValue.trim().length === 0}
                  >
                    {sharing ? 'Sharing…' : 'Share'}
                  </button>
                </div>
                <p className="sd-hint">Shared users get read-only access (view status, logs, URL).</p>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
