import { ButtonHTMLAttributes, ReactNode } from 'react'
import './primitives.css'

// Token-driven UI primitives shared across the app. Plain React + CSS classes so
// there is one Button/Badge/Card/state implementation instead of per-component
// re-styling.

type ButtonVariant = 'default' | 'primary' | 'ghost' | 'danger'

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  size?: 'sm' | 'md'
  icon?: boolean
}

export function Button({ variant = 'default', size = 'md', icon = false, className = '', ...rest }: ButtonProps) {
  const cls = [
    'ui-btn',
    variant !== 'default' && `ui-btn-${variant}`,
    size === 'sm' && 'ui-btn-sm',
    icon && 'ui-btn-icon',
    className,
  ]
    .filter(Boolean)
    .join(' ')
  return <button className={cls} {...rest} />
}

type BadgeTone = 'neutral' | 'green' | 'yellow' | 'red' | 'blue' | 'accent'

export function Badge({ tone = 'neutral', dot = false, children }: { tone?: BadgeTone; dot?: boolean; children: ReactNode }) {
  const cls = ['ui-badge', tone !== 'neutral' && `ui-badge-${tone}`, dot && 'ui-badge-dot'].filter(Boolean).join(' ')
  return <span className={cls}>{children}</span>
}

export function Spinner({ label = 'Loading' }: { label?: string }) {
  return <span className="ui-spinner" role="status" aria-label={label} />
}

export function EmptyState({ icon, title, description, action }: { icon?: ReactNode; title: string; description?: string; action?: ReactNode }) {
  return (
    <div className="ui-state">
      {icon && <div className="ui-state-icon" aria-hidden="true">{icon}</div>}
      <div className="ui-state-title">{title}</div>
      {description && <div className="ui-state-desc">{description}</div>}
      {action}
    </div>
  )
}

export function ErrorState({ title, description, onRetry }: { title: string; description?: string; onRetry?: () => void }) {
  return (
    <div className="ui-state" role="alert">
      <div className="ui-state-icon" aria-hidden="true">⚠️</div>
      <div className="ui-state-title">{title}</div>
      {description && <div className="ui-state-desc">{description}</div>}
      {onRetry && <Button size="sm" onClick={onRetry}>Try again</Button>}
    </div>
  )
}
