import type { ArtifactSummary } from '../api/client'

// Shared display helpers so the Applications card and table (and any future
// surface) render status/target/time identically.

export function timeAgo(dateStr: string): string {
  if (!dateStr) return '—'
  const seconds = Math.floor((Date.now() - new Date(dateStr).getTime()) / 1000)
  if (Number.isNaN(seconds)) return '—'
  if (seconds < 60) return `${Math.max(0, seconds)}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

type BadgeTone = 'neutral' | 'green' | 'yellow' | 'red' | 'blue' | 'accent'

export const statusMeta: Record<ArtifactSummary['status'], { tone: BadgeTone; label: string }> = {
  running: { tone: 'green', label: 'Running' },
  building: { tone: 'yellow', label: 'Building' },
  deploying: { tone: 'blue', label: 'Deploying' },
  pending: { tone: 'neutral', label: 'Pending' },
  failed: { tone: 'red', label: 'Failed' },
  deleted: { tone: 'neutral', label: 'Deleted' },
}

export const targetLabels: Record<string, string> = {
  kubernetes: 'Kubernetes',
  sandbox: 'Sandbox',
  runner: 'Fast lane',
}
