import { useState, useEffect, useCallback } from 'react'
import {
  User, Department,
  fetchUsers, createUser, updateUser,
  fetchDepartments, createDepartment, deleteDepartment,
} from '../api/client'
import './AdminPanel.css'

interface Props {
  currentUser: string
}

function timeAgo(dateStr: string): string {
  const seconds = Math.floor((Date.now() - new Date(dateStr).getTime()) / 1000)
  if (seconds < 60) return 'just now'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

export default function AdminPanel({ currentUser }: Props) {
  const [users, setUsers] = useState<User[]>([])
  const [departments, setDepartments] = useState<Department[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [collapsed, setCollapsed] = useState(false)
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

  const loadData = useCallback(async () => {
    try {
      setLoading(true)
      setError(null)
      const [usersData, deptsData] = await Promise.all([fetchUsers(), fetchDepartments()])
      setUsers(usersData ?? [])
      setDepartments(deptsData ?? [])
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load data')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { loadData() }, [loadData])

  const handleCreateUser = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!createName.trim()) return
    try {
      setCreating(true)
      setError(null)
      const user = await createUser(createName.trim(), createEmail.trim(), createRole)
      if (createDeptId) {
        const updated = await updateUser(user.id, { department_id: createDeptId })
        setUsers((prev) => [...prev, updated])
      } else {
        setUsers((prev) => [...prev, user])
      }
      setCreateName('')
      setCreateEmail('')
      setCreateRole('user')
      setCreateDeptId('')
      setShowCreateUser(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create user')
    } finally {
      setCreating(false)
    }
  }

  const handleCreateDept = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!deptName.trim()) return
    try {
      setCreatingDept(true)
      setError(null)
      const dept = await createDepartment(deptName.trim())
      setDepartments((prev) => [...prev, dept])
      setDeptName('')
      setShowCreateDept(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create department')
    } finally {
      setCreatingDept(false)
    }
  }

  const handleDeleteDept = async (id: string) => {
    try {
      setActionId(id)
      await deleteDepartment(id)
      setDepartments((prev) => prev.filter((d) => d.id !== id))
      // Clear department from users that had this department
      setUsers((prev) => prev.map((u) => u.department_id === id ? { ...u, department_id: '' } : u))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete department')
    } finally {
      setActionId(null)
    }
  }

  const handleToggleRole = async (user: User) => {
    const newRole = user.role === 'admin' ? 'user' : 'admin'
    try {
      setActionId(user.id)
      const updated = await updateUser(user.id, { role: newRole })
      setUsers((prev) => prev.map((u) => (u.id === user.id ? updated : u)))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update role')
    } finally {
      setActionId(null)
    }
  }

  const handleToggleStatus = async (user: User) => {
    const newStatus = user.status === 'active' ? 'suspended' : 'active'
    try {
      setActionId(user.id)
      const updated = await updateUser(user.id, { status: newStatus })
      setUsers((prev) => prev.map((u) => (u.id === user.id ? updated : u)))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update status')
    } finally {
      setActionId(null)
    }
  }

  const handleChangeDept = async (user: User, newDeptId: string) => {
    try {
      setActionId(user.id)
      const updated = await updateUser(user.id, { department_id: newDeptId })
      setUsers((prev) => prev.map((u) => (u.id === user.id ? updated : u)))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update department')
    } finally {
      setActionId(null)
    }
  }

  const isSelf = (id: string) => id === currentUser

  const deptUserCount = (deptId: string) => users.filter((u) => u.department_id === deptId).length

  return (
    <div className="ap-section">
      <h2 className="section-title" onClick={() => setCollapsed(!collapsed)} style={{ cursor: 'pointer' }}>
        User Management
        <span className="count">{users.length}</span>
        <span className="ap-collapse-icon">{collapsed ? '+' : '-'}</span>
      </h2>

      {!collapsed && (
        <>
          {error && (
            <div className="ap-error">
              {error}
              <button onClick={() => setError(null)}>Dismiss</button>
            </div>
          )}

          <div className="ap-tabs">
            <button
              className={`ap-tab ${activeTab === 'users' ? 'ap-tab-active' : ''}`}
              onClick={() => setActiveTab('users')}
            >
              Users ({users.length})
            </button>
            <button
              className={`ap-tab ${activeTab === 'departments' ? 'ap-tab-active' : ''}`}
              onClick={() => setActiveTab('departments')}
            >
              Departments ({departments.length})
            </button>
            <button className="ap-refresh-btn" onClick={loadData} disabled={loading}>
              {loading ? 'Loading...' : 'Refresh'}
            </button>
          </div>

          {activeTab === 'users' && (
            <>
              <div className="ap-toolbar">
                <button className="ap-create-btn" onClick={() => setShowCreateUser(!showCreateUser)}>
                  {showCreateUser ? 'Cancel' : '+ New User'}
                </button>
              </div>

              {showCreateUser && (
                <form className="ap-create-form" onSubmit={handleCreateUser}>
                  <input className="ap-input" placeholder="Name" value={createName}
                    onChange={(e) => setCreateName(e.target.value)} required />
                  <input className="ap-input" placeholder="Email (optional)" value={createEmail}
                    onChange={(e) => setCreateEmail(e.target.value)} />
                  <select className="ap-select" value={createRole}
                    onChange={(e) => setCreateRole(e.target.value)}>
                    <option value="user">User</option>
                    <option value="admin">Admin</option>
                  </select>
                  <select className="ap-select" value={createDeptId}
                    onChange={(e) => setCreateDeptId(e.target.value)}>
                    <option value="">No department</option>
                    {departments.map((d) => (
                      <option key={d.id} value={d.id}>{d.name}</option>
                    ))}
                  </select>
                  <button className="ap-submit-btn" type="submit" disabled={creating || !createName.trim()}>
                    {creating ? 'Creating...' : 'Create'}
                  </button>
                </form>
              )}

              {loading && users.length === 0 ? (
                <div className="ap-empty">Loading users...</div>
              ) : users.length === 0 ? (
                <div className="ap-empty">No users found. Auth may be disabled.</div>
              ) : (
                <div className="ap-table-wrap">
                  <table className="ap-table">
                    <thead>
                      <tr>
                        <th>Name</th>
                        <th>Email</th>
                        <th>Department</th>
                        <th>Role</th>
                        <th>Status</th>
                        <th>Provider</th>
                        <th>Created</th>
                        <th>Actions</th>
                      </tr>
                    </thead>
                    <tbody>
                      {users.map((user) => (
                        <tr key={user.id}>
                          <td className="ap-name">
                            {user.name}
                            {isSelf(user.id) && <span className="ap-you-badge">you</span>}
                          </td>
                          <td className="ap-email">{user.email || '--'}</td>
                          <td>
                            <select
                              className="ap-dept-select"
                              value={user.department_id || ''}
                              onChange={(e) => handleChangeDept(user, e.target.value)}
                              disabled={actionId === user.id}
                            >
                              <option value="">--</option>
                              {departments.map((d) => (
                                <option key={d.id} value={d.id}>{d.name}</option>
                              ))}
                            </select>
                          </td>
                          <td>
                            <span className={`ap-role-badge ${user.role === 'admin' ? 'ap-role-admin' : ''}`}>
                              {user.role}
                            </span>
                          </td>
                          <td>
                            <span className={`ap-status ${user.status === 'active' ? 'ap-status-active' : 'ap-status-suspended'}`}>
                              <span className="ap-status-dot" />
                              {user.status}
                            </span>
                          </td>
                          <td className="ap-provider">{user.provider}</td>
                          <td className="ap-created">{timeAgo(user.created_at)}</td>
                          <td className="ap-actions">
                            <button className="ap-action-btn"
                              onClick={() => handleToggleRole(user)}
                              disabled={isSelf(user.id) || actionId === user.id}
                              title={isSelf(user.id) ? 'Cannot change own role' : ''}>
                              {user.role === 'admin' ? 'Make User' : 'Make Admin'}
                            </button>
                            <button className={`ap-action-btn ${user.status === 'active' ? 'ap-action-danger' : ''}`}
                              onClick={() => handleToggleStatus(user)}
                              disabled={isSelf(user.id) || actionId === user.id}
                              title={isSelf(user.id) ? 'Cannot change own status' : ''}>
                              {user.status === 'active' ? 'Suspend' : 'Activate'}
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}

          {activeTab === 'departments' && (
            <>
              <div className="ap-toolbar">
                <button className="ap-create-btn" onClick={() => setShowCreateDept(!showCreateDept)}>
                  {showCreateDept ? 'Cancel' : '+ New Department'}
                </button>
              </div>

              {showCreateDept && (
                <form className="ap-create-form" onSubmit={handleCreateDept}>
                  <input className="ap-input" placeholder="Department name" value={deptName}
                    onChange={(e) => setDeptName(e.target.value)} required style={{ flex: 2 }} />
                  <button className="ap-submit-btn" type="submit" disabled={creatingDept || !deptName.trim()}>
                    {creatingDept ? 'Creating...' : 'Create'}
                  </button>
                </form>
              )}

              {departments.length === 0 ? (
                <div className="ap-empty">No departments yet. Create one to organize users.</div>
              ) : (
                <div className="ap-table-wrap">
                  <table className="ap-table">
                    <thead>
                      <tr>
                        <th>Name</th>
                        <th>Members</th>
                        <th>Created</th>
                        <th>Actions</th>
                      </tr>
                    </thead>
                    <tbody>
                      {departments.map((dept) => (
                        <tr key={dept.id}>
                          <td className="ap-name">{dept.name}</td>
                          <td>
                            <span className="ap-dept-count">{deptUserCount(dept.id)}</span>
                          </td>
                          <td className="ap-created">{timeAgo(dept.created_at)}</td>
                          <td className="ap-actions">
                            <button
                              className="ap-action-btn ap-action-danger"
                              onClick={() => handleDeleteDept(dept.id)}
                              disabled={actionId === dept.id}
                            >
                              Delete
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}
        </>
      )}
    </div>
  )
}
