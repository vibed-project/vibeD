import { useState, useEffect, useCallback } from 'react'
import {
  User, Department,
  fetchUsers, createUser, updateUser,
  fetchDepartments, createDepartment, deleteDepartment,
} from '../api/client'
import { DataTable, Column } from '../ui/DataTable'
import { Badge, Button, Spinner } from '../ui/primitives'
import { useToast } from '../ui/toast'
import { timeAgo } from '../lib/format'
import './AdminPanel.css'

interface Props {
  currentUser: string
}

export default function AdminPanel({ currentUser }: Props) {
  const toast = useToast()
  const [users, setUsers] = useState<User[]>([])
  const [departments, setDepartments] = useState<Department[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreateUser, setShowCreateUser] = useState(false)
  const [showCreateDept, setShowCreateDept] = useState(false)
  const [createName, setCreateName] = useState('')
  const [createEmail, setCreateEmail] = useState('')
  const [createRole, setCreateRole] = useState('user')
  const [createDeptId, setCreateDeptId] = useState('')
  const [creating, setCreating] = useState(false)
  const [deptName, setDeptName] = useState('')
  const [creatingDept, setCreatingDept] = useState(false)
  const [actionId, setActionId] = useState<string | null>(null)
  const [activeTab, setActiveTab] = useState<'users' | 'departments'>('users')
  // The plaintext API key for a just-created user, shown once so the admin can
  // hand it over — the user signs in with it (Authorization: Bearer <key>).
  const [newKey, setNewKey] = useState<{ name: string; key: string } | null>(null)

  const loadData = useCallback(async () => {
    try {
      setLoading(true)
      const [usersData, deptsData] = await Promise.all([fetchUsers(), fetchDepartments()])
      setUsers(usersData ?? [])
      setDepartments(deptsData ?? [])
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to load data')
    } finally {
      setLoading(false)
    }
  }, [toast])

  useEffect(() => { loadData() }, [loadData])

  const handleCreateUser = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!createName.trim()) return
    try {
      setCreating(true)
      const user = await createUser(createName.trim(), createEmail.trim(), createRole)
      const finalUser = createDeptId ? await updateUser(user.id, { department_id: createDeptId }) : user
      setUsers((prev) => [...prev, finalUser])
      if (user.api_key) setNewKey({ name: user.name, key: user.api_key })
      setCreateName(''); setCreateEmail(''); setCreateRole('user'); setCreateDeptId('')
      setShowCreateUser(false)
      toast.success('User created')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to create user')
    } finally {
      setCreating(false)
    }
  }

  const handleCreateDept = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!deptName.trim()) return
    try {
      setCreatingDept(true)
      const dept = await createDepartment(deptName.trim())
      setDepartments((prev) => [...prev, dept])
      setDeptName('')
      setShowCreateDept(false)
      toast.success('Department created')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to create department')
    } finally {
      setCreatingDept(false)
    }
  }

  const withAction = async (id: string, fn: () => Promise<void>, failMsg: string) => {
    try {
      setActionId(id)
      await fn()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : failMsg)
    } finally {
      setActionId(null)
    }
  }

  const handleDeleteDept = (id: string) => withAction(id, async () => {
    await deleteDepartment(id)
    setDepartments((prev) => prev.filter((d) => d.id !== id))
    setUsers((prev) => prev.map((u) => u.department_id === id ? { ...u, department_id: '' } : u))
  }, 'Failed to delete department')

  const handleToggleRole = (user: User) => withAction(user.id, async () => {
    const updated = await updateUser(user.id, { role: user.role === 'admin' ? 'user' : 'admin' })
    setUsers((prev) => prev.map((u) => (u.id === user.id ? updated : u)))
  }, 'Failed to update role')

  const handleToggleStatus = (user: User) => withAction(user.id, async () => {
    const updated = await updateUser(user.id, { status: user.status === 'active' ? 'suspended' : 'active' })
    setUsers((prev) => prev.map((u) => (u.id === user.id ? updated : u)))
  }, 'Failed to update status')

  const handleChangeDept = (user: User, newDeptId: string) => withAction(user.id, async () => {
    const updated = await updateUser(user.id, { department_id: newDeptId })
    setUsers((prev) => prev.map((u) => (u.id === user.id ? updated : u)))
  }, 'Failed to update department')

  const isSelf = (id: string) => id === currentUser
  const deptUserCount = (deptId: string) => users.filter((u) => u.department_id === deptId).length
  const deptName_ = (id?: string) => departments.find((d) => d.id === id)?.name ?? ''

  const userColumns: Column<User>[] = [
    {
      key: 'name', header: 'Name', sortable: true,
      render: (u) => <span>{u.name}{isSelf(u.id) && <> <Badge tone="accent">you</Badge></>}</span>,
    },
    { key: 'email', header: 'Email', sortable: true, render: (u) => u.email || '—' },
    {
      key: 'department_id', header: 'Department',
      render: (u) => (
        <select className="ui-input" style={{ height: 30 }} value={u.department_id || ''}
          onChange={(e) => handleChangeDept(u, e.target.value)} disabled={actionId === u.id} aria-label={`Department for ${u.name}`}>
          <option value="">—</option>
          {departments.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
        </select>
      ),
    },
    {
      key: 'role', header: 'Role', sortable: true,
      render: (u) => <Badge tone={u.role === 'admin' ? 'accent' : 'neutral'}>{u.role}</Badge>,
    },
    {
      key: 'status', header: 'Status', sortable: true,
      render: (u) => <Badge tone={u.status === 'active' ? 'green' : 'red'} dot>{u.status}</Badge>,
    },
    { key: 'provider', header: 'Provider', sortable: true },
    { key: 'created_at', header: 'Created', sortable: true, sortValue: (u) => u.created_at, render: (u) => timeAgo(u.created_at) },
    {
      key: 'actions', header: '', align: 'right',
      render: (u) => (
        <div style={{ display: 'inline-flex', gap: 'var(--space-1)', justifyContent: 'flex-end' }}>
          <Button size="sm" variant="ghost" onClick={() => handleToggleRole(u)} disabled={isSelf(u.id) || actionId === u.id}
            title={isSelf(u.id) ? 'Cannot change own role' : ''}>
            {u.role === 'admin' ? 'Make user' : 'Make admin'}
          </Button>
          <Button size="sm" variant={u.status === 'active' ? 'danger' : 'default'} onClick={() => handleToggleStatus(u)}
            disabled={isSelf(u.id) || actionId === u.id} title={isSelf(u.id) ? 'Cannot change own status' : ''}>
            {u.status === 'active' ? 'Suspend' : 'Activate'}
          </Button>
        </div>
      ),
    },
  ]

  const deptColumns: Column<Department>[] = [
    { key: 'name', header: 'Name', sortable: true },
    { key: 'members', header: 'Members', sortable: true, sortValue: (d) => deptUserCount(d.id), render: (d) => <Badge>{deptUserCount(d.id)}</Badge> },
    { key: 'created_at', header: 'Created', sortable: true, sortValue: (d) => d.created_at, render: (d) => timeAgo(d.created_at) },
    {
      key: 'actions', header: '', align: 'right',
      render: (d) => <Button size="sm" variant="danger" onClick={() => handleDeleteDept(d.id)} disabled={actionId === d.id}>Delete</Button>,
    },
  ]

  return (
    <div className="ap-section">
      <div className="ap-tabbar">
        <div className="ap-tabs">
          <button className={`ap-tab ${activeTab === 'users' ? 'ap-tab-active' : ''}`} onClick={() => setActiveTab('users')}>
            Users <span className="ap-tab-count">{users.length}</span>
          </button>
          <button className={`ap-tab ${activeTab === 'departments' ? 'ap-tab-active' : ''}`} onClick={() => setActiveTab('departments')}>
            Departments <span className="ap-tab-count">{departments.length}</span>
          </button>
        </div>
        <Button size="sm" variant="ghost" onClick={loadData} disabled={loading}>{loading ? <Spinner label="Refreshing" /> : 'Refresh'}</Button>
      </div>

      {newKey && (
        <div className="ap-newkey">
          <div className="ap-newkey-head">
            <strong>API key for {newKey.name}</strong>
            <span className="ap-newkey-note">Shown once — copy it now. {newKey.name} signs in with this key.</span>
          </div>
          <div className="ap-newkey-row">
            <code className="ap-newkey-code">{newKey.key}</code>
            <Button size="sm" onClick={() => navigator.clipboard?.writeText(newKey.key)}>Copy</Button>
            <Button size="sm" variant="ghost" onClick={() => setNewKey(null)}>Dismiss</Button>
          </div>
        </div>
      )}

      {activeTab === 'users' && (
        <>
          {showCreateUser && (
            <form className="ap-create-form" onSubmit={handleCreateUser}>
              <input className="ui-input" placeholder="Name" value={createName} onChange={(e) => setCreateName(e.target.value)} required />
              <input className="ui-input" placeholder="Email (optional)" value={createEmail} onChange={(e) => setCreateEmail(e.target.value)} />
              <select className="ui-input" value={createRole} onChange={(e) => setCreateRole(e.target.value)}>
                <option value="user">User</option>
                <option value="admin">Admin</option>
              </select>
              <select className="ui-input" value={createDeptId} onChange={(e) => setCreateDeptId(e.target.value)}>
                <option value="">No department</option>
                {departments.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
              </select>
              <Button variant="primary" type="submit" disabled={creating || !createName.trim()}>{creating ? 'Creating…' : 'Create'}</Button>
            </form>
          )}
          {loading && users.length === 0 ? (
            <div className="ui-state"><Spinner label="Loading users" /></div>
          ) : (
            <DataTable
              columns={userColumns}
              rows={users}
              rowKey={(u) => u.id}
              searchText={(u) => `${u.name} ${u.email ?? ''} ${u.role} ${u.status} ${u.provider} ${deptName_(u.department_id)}`}
              searchPlaceholder="Search users…"
              emptyTitle="No users found"
              emptyDescription="Auth may be disabled, or no users have been created yet."
              toolbar={<Button size="sm" variant="primary" onClick={() => setShowCreateUser((v) => !v)}>{showCreateUser ? 'Cancel' : '+ New user'}</Button>}
            />
          )}
        </>
      )}

      {activeTab === 'departments' && (
        <>
          {showCreateDept && (
            <form className="ap-create-form" onSubmit={handleCreateDept}>
              <input className="ui-input" placeholder="Department name" value={deptName} onChange={(e) => setDeptName(e.target.value)} required style={{ flex: 2 }} />
              <Button variant="primary" type="submit" disabled={creatingDept || !deptName.trim()}>{creatingDept ? 'Creating…' : 'Create'}</Button>
            </form>
          )}
          <DataTable
            columns={deptColumns}
            rows={departments}
            rowKey={(d) => d.id}
            searchText={(d) => d.name}
            searchPlaceholder="Search departments…"
            emptyTitle="No departments yet"
            emptyDescription="Create one to organize users into departments."
            toolbar={<Button size="sm" variant="primary" onClick={() => setShowCreateDept((v) => !v)}>{showCreateDept ? 'Cancel' : '+ New department'}</Button>}
          />
        </>
      )}
    </div>
  )
}
