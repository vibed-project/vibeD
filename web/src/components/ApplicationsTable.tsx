import { useState } from 'react'
import type { ArtifactSummary } from '../api/client'
import { DataTable, Column } from '../ui/DataTable'
import { Badge, Button } from '../ui/primitives'
import { statusMeta, targetLabels, timeAgo } from '../lib/format'

interface Props {
  artifacts: ArtifactSummary[]
  currentUser: string
  isAdmin: boolean
  statusFilter: string
  onStatusFilter: (v: string) => void
  onViewLogs: (id: string) => void
  onViewVersions: (id: string) => void
  onShare: (id: string) => void
  onDelete: (id: string) => Promise<void>
}

// Applications rendered as a data table: searchable, sortable,
// paginated, with a status facet — replacing the card-only list for dense views.
export default function ApplicationsTable(props: Props) {
  const { artifacts, currentUser, isAdmin, statusFilter, onStatusFilter, onViewLogs, onViewVersions, onShare, onDelete } = props
  const [confirmId, setConfirmId] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)

  const canWrite = (a: ArtifactSummary) => !currentUser || a.owner_id === currentUser || isAdmin

  const doDelete = async (id: string) => {
    setBusyId(id)
    try {
      await onDelete(id)
    } finally {
      setBusyId(null)
      setConfirmId(null)
    }
  }

  const columns: Column<ArtifactSummary>[] = [
    {
      key: 'name',
      header: 'Name',
      sortable: true,
      render: (a) => (
        <span style={{ fontWeight: 'var(--weight-medium)' }}>
          {a.url ? <a href={a.url} target="_blank" rel="noreferrer">{a.name}</a> : a.name}
        </span>
      ),
    },
    {
      key: 'status',
      header: 'Status',
      sortable: true,
      render: (a) => {
        const m = statusMeta[a.status] ?? statusMeta.pending
        return <Badge tone={m.tone} dot>{m.label}</Badge>
      },
    },
    { key: 'target', header: 'Runtime', sortable: true, render: (a) => targetLabels[a.target] ?? a.target },
    {
      key: 'owner_id',
      header: 'Owner',
      sortable: true,
      render: (a) => (a.owner_id === currentUser ? <Badge tone="accent">you</Badge> : (a.owner_id || '—')),
    },
    {
      key: 'updated_at',
      header: 'Updated',
      sortable: true,
      sortValue: (a) => a.updated_at,
      render: (a) => timeAgo(a.updated_at),
    },
    {
      key: 'actions',
      header: '',
      align: 'right',
      render: (a) => (
        <div style={{ display: 'inline-flex', gap: 'var(--space-1)', justifyContent: 'flex-end' }}>
          <Button size="sm" variant="ghost" onClick={() => onViewLogs(a.id)}>Logs</Button>
          <Button size="sm" variant="ghost" onClick={() => onViewVersions(a.id)}>Versions</Button>
          {canWrite(a) && <Button size="sm" variant="ghost" onClick={() => onShare(a.id)}>Share</Button>}
          {canWrite(a) && (
            confirmId === a.id ? (
              <>
                <Button size="sm" variant="danger" onClick={() => doDelete(a.id)} disabled={busyId === a.id}>
                  {busyId === a.id ? 'Deleting…' : 'Confirm'}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setConfirmId(null)}>Cancel</Button>
              </>
            ) : (
              <Button size="sm" variant="ghost" onClick={() => setConfirmId(a.id)} aria-label={`Delete ${a.name}`}>Delete</Button>
            )
          )}
        </div>
      ),
    },
  ]

  const statuses = ['all', 'running', 'building', 'deploying', 'pending', 'failed']

  return (
    <DataTable
      columns={columns}
      rows={artifacts}
      rowKey={(a) => a.id}
      searchText={(a) => `${a.name} ${a.owner_id ?? ''} ${a.target} ${a.status}`}
      searchPlaceholder="Search applications…"
      emptyTitle="No matching applications"
      toolbar={
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--space-2)', fontSize: 'var(--text-sm)', color: 'var(--text-muted)' }}>
          Status
          <select className="ui-input" value={statusFilter} onChange={(e) => onStatusFilter(e.target.value)} style={{ height: 32 }}>
            {statuses.map((s) => (
              <option key={s} value={s}>{s === 'all' ? 'All' : statusMeta[s as ArtifactSummary['status']]?.label ?? s}</option>
            ))}
          </select>
        </label>
      }
    />
  )
}
