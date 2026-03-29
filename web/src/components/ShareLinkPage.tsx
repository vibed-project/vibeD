import { useState, useEffect } from 'react'
import { resolveShareLink } from '../api/client'
import './ShareLinkPage.css'

interface ArtifactView {
  name: string
  status: string
  url: string
  target: string
}

interface Props {
  token: string
}

export default function ShareLinkPage({ token }: Props) {
  const [state, setState] = useState<'loading' | 'needs_password' | 'ready' | 'error'>('loading')
  const [artifact, setArtifact] = useState<ArtifactView | null>(null)
  const [password, setPassword] = useState('')
  const [passwordError, setPasswordError] = useState('')
  const [checking, setChecking] = useState(false)
  const [errorMsg, setErrorMsg] = useState('')

  useEffect(() => {
    resolveShareLink(token)
      .then((data) => {
        setArtifact(data)
        setState('ready')
      })
      .catch((err: Error) => {
        if (err.message === 'password_required') {
          setState('needs_password')
        } else {
          setState('error')
          setErrorMsg('This link is no longer valid or has expired.')
        }
      })
  }, [token])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!password.trim()) return
    setChecking(true)
    setPasswordError('')
    try {
      const data = await resolveShareLink(token, password)
      setArtifact(data)
      setState('ready')
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : ''
      if (msg === 'invalid_password') {
        setPasswordError('Incorrect password. Please try again.')
      } else {
        setState('error')
        setErrorMsg('This link is no longer valid or has expired.')
      }
    } finally {
      setChecking(false)
    }
  }

  return (
    <div className="slp-root">
      <div className="slp-card">
        <div className="slp-logo">vibeD</div>

        {state === 'loading' && (
          <div className="slp-body">
            <div className="slp-spinner" />
            <p className="slp-sub">Checking link…</p>
          </div>
        )}

        {state === 'error' && (
          <div className="slp-body">
            <div className="slp-icon slp-icon-error">✕</div>
            <h2 className="slp-title">Link unavailable</h2>
            <p className="slp-sub">{errorMsg}</p>
          </div>
        )}

        {state === 'needs_password' && (
          <div className="slp-body">
            <div className="slp-icon slp-icon-lock">🔒</div>
            <h2 className="slp-title">Password required</h2>
            <p className="slp-sub">This link is password-protected. Enter the password to continue.</p>
            <form className="slp-form" onSubmit={handleSubmit}>
              <input
                className={`slp-input ${passwordError ? 'slp-input-error' : ''}`}
                type="password"
                placeholder="Enter password"
                value={password}
                onChange={(e) => { setPassword(e.target.value); setPasswordError('') }}
                autoFocus
                disabled={checking}
              />
              {passwordError && <p className="slp-error">{passwordError}</p>}
              <button className="slp-btn" type="submit" disabled={checking || !password.trim()}>
                {checking ? 'Checking…' : 'Unlock'}
              </button>
            </form>
          </div>
        )}

        {state === 'ready' && artifact && (
          <div className="slp-body">
            <div className="slp-icon slp-icon-ok">✓</div>
            <h2 className="slp-title">{artifact.name}</h2>
            <div className="slp-meta">
              <span className={`slp-status slp-status-${artifact.status}`}>{artifact.status}</span>
              <span className="slp-target">{artifact.target}</span>
            </div>
            {artifact.url ? (
              <a className="slp-url-btn" href={artifact.url} target="_blank" rel="noopener noreferrer">
                Open artifact ↗
              </a>
            ) : (
              <p className="slp-sub">No public URL available.</p>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
