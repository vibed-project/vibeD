export interface ArtifactSummary {
  id: string;
  name: string;
  owner_id?: string;
  status: 'pending' | 'building' | 'deploying' | 'running' | 'failed' | 'deleted';
  target: 'knative' | 'kubernetes';
  url?: string;
  created_at: string;
  updated_at: string;
  version: number;
  shared_with?: string[];
}

export interface Artifact extends ArtifactSummary {
  image_ref?: string;
  port?: number;
  env_vars?: Record<string, string>;
  language?: string;
  error?: string;
  storage_ref?: string;
  version_id?: string;
}

export interface ArtifactVersion {
  version_id: string;
  artifact_id: string;
  version: number;
  image_ref: string;
  storage_ref?: string;
  env_vars?: Record<string, string>;
  status: string;
  url?: string;
  created_at: string;
  created_by: string;
}

export interface TargetInfo {
  name: string;
  available: boolean;
  preferred: boolean;
  description: string;
}

export interface LogsResponse {
  artifact_id: string;
  logs: string[];
}

export interface WhoAmI {
  // Basic fields (always present)
  user_id: string;
  role: string;
  // Extended fields (present when user store has the record)
  id?: string;
  name?: string;
  email?: string;
  status?: string;
  provider?: string;
}

export interface OrganizationInfo {
  name: string;
}

export interface User {
  id: string;
  name: string;
  email?: string;
  role: string;
  status: string;
  provider: string;
  department_id?: string;
  created_at: string;
  updated_at: string;
}

export interface Department {
  id: string;
  name: string;
  created_at: string;
  updated_at: string;
}

const BASE = '';
const DEFAULT_TIMEOUT_MS = 30_000;
const AUTH_TOKEN_KEY = 'vibed_auth_token';

export function getAuthToken(): string | null {
  return localStorage.getItem(AUTH_TOKEN_KEY);
}

export function setAuthToken(token: string): void {
  localStorage.setItem(AUTH_TOKEN_KEY, token);
}

export function clearAuthToken(): void {
  localStorage.removeItem(AUTH_TOKEN_KEY);
}

async function fetchWithTimeout(url: string, opts?: RequestInit): Promise<Response> {
  const controller = new AbortController();
  const token = getAuthToken();
  const headers = new Headers(opts?.headers);
  if (token) {
    headers.set('Authorization', `Bearer ${token}`);
  }
  const id = setTimeout(() => controller.abort(), DEFAULT_TIMEOUT_MS);
  try {
    return await fetch(url, { ...opts, headers, signal: controller.signal });
  } finally {
    clearTimeout(id);
  }
}

export interface ArtifactListResult {
  artifacts: ArtifactSummary[];
  total: number;
}

export async function fetchArtifacts(status?: string, offset = 0, limit = 50): Promise<ArtifactListResult> {
  const params = new URLSearchParams();
  if (status) params.set('status', status);
  if (offset > 0) params.set('offset', String(offset));
  if (limit !== 50) params.set('limit', String(limit));
  const qs = params.toString() ? `?${params.toString()}` : '';
  const res = await fetchWithTimeout(`${BASE}/api/artifacts${qs}`);
  if (!res.ok) throw new Error(`Failed to fetch artifacts: ${res.statusText}`);
  const data = await res.json();
  return { artifacts: data?.artifacts ?? [], total: data?.total ?? 0 };
}

export async function fetchArtifact(id: string): Promise<Artifact> {
  const res = await fetchWithTimeout(`${BASE}/api/artifacts/${id}`);
  if (!res.ok) throw new Error(`Failed to fetch artifact: ${res.statusText}`);
  return res.json();
}

export async function fetchLogs(id: string): Promise<LogsResponse> {
  const res = await fetchWithTimeout(`${BASE}/api/artifacts/${id}/logs`);
  if (!res.ok) throw new Error(`Failed to fetch logs: ${res.statusText}`);
  return res.json();
}

export async function deleteArtifact(id: string): Promise<void> {
  const res = await fetchWithTimeout(`${BASE}/api/artifacts/${id}`, { method: 'DELETE' });
  if (!res.ok) throw new Error(`Failed to delete artifact: ${res.statusText}`);
}

export async function fetchTargets(): Promise<TargetInfo[]> {
  const res = await fetchWithTimeout(`${BASE}/api/targets`);
  if (!res.ok) throw new Error(`Failed to fetch targets: ${res.statusText}`);
  return res.json();
}

export async function fetchWhoami(): Promise<WhoAmI> {
  const res = await fetchWithTimeout(`${BASE}/api/whoami`);
  if (!res.ok) throw new Error(`Failed to fetch user info: ${res.statusText}`);
  return res.json();
}

export async function fetchOrganization(): Promise<OrganizationInfo> {
  const res = await fetchWithTimeout(`${BASE}/api/organization`);
  if (!res.ok) throw new Error(`Failed to fetch organization: ${res.statusText}`);
  return res.json();
}

export async function fetchVersions(id: string): Promise<ArtifactVersion[]> {
  const res = await fetchWithTimeout(`${BASE}/api/artifacts/${id}/versions`);
  if (!res.ok) throw new Error(`Failed to fetch versions: ${res.statusText}`);
  const data = await res.json();
  return data?.versions ?? [];
}

export async function rollbackArtifact(id: string, version: number): Promise<void> {
  const res = await fetchWithTimeout(`${BASE}/api/artifacts/${id}/rollback`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ version }),
  });
  if (!res.ok) throw new Error(`Failed to rollback artifact: ${res.statusText}`);
}

export async function shareArtifact(id: string, userIds: string[]): Promise<void> {
  const res = await fetchWithTimeout(`${BASE}/api/artifacts/${id}/share`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ user_ids: userIds }),
  });
  if (!res.ok) throw new Error(`Failed to share artifact: ${res.statusText}`);
}

export async function unshareArtifact(id: string, userIds: string[]): Promise<void> {
  const res = await fetchWithTimeout(`${BASE}/api/artifacts/${id}/unshare`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ user_ids: userIds }),
  });
  if (!res.ok) throw new Error(`Failed to unshare artifact: ${res.statusText}`);
}

// User management (admin)

export async function fetchUsers(): Promise<User[]> {
  const res = await fetchWithTimeout(`${BASE}/api/users`);
  if (!res.ok) throw new Error(`Failed to fetch users: ${res.statusText}`);
  return res.json();
}

export async function createUser(name: string, email: string, role: string): Promise<User> {
  const res = await fetchWithTimeout(`${BASE}/api/users`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, email, role }),
  });
  if (!res.ok) throw new Error(`Failed to create user: ${res.statusText}`);
  return res.json();
}

export async function fetchUser(id: string): Promise<User> {
  const res = await fetchWithTimeout(`${BASE}/api/users/${id}`);
  if (!res.ok) throw new Error(`Failed to fetch user: ${res.statusText}`);
  return res.json();
}

export async function updateUser(id: string, updates: { role?: string; status?: string; department_id?: string }): Promise<User> {
  const res = await fetchWithTimeout(`${BASE}/api/users/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(updates),
  });
  if (!res.ok) throw new Error(`Failed to update user: ${res.statusText}`);
  return res.json();
}

export async function suspendUser(id: string): Promise<User> {
  const res = await fetchWithTimeout(`${BASE}/api/users/${id}`, { method: 'DELETE' });
  if (!res.ok) throw new Error(`Failed to suspend user: ${res.statusText}`);
  return res.json();
}

// Department management (admin)

export async function fetchDepartments(): Promise<Department[]> {
  const res = await fetchWithTimeout(`${BASE}/api/departments`);
  if (!res.ok) throw new Error(`Failed to fetch departments: ${res.statusText}`);
  return res.json();
}

export async function createDepartment(name: string): Promise<Department> {
  const res = await fetchWithTimeout(`${BASE}/api/departments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  });
  if (!res.ok) throw new Error(`Failed to create department: ${res.statusText}`);
  return res.json();
}

export async function updateDepartment(id: string, name: string): Promise<Department> {
  const res = await fetchWithTimeout(`${BASE}/api/departments/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  });
  if (!res.ok) throw new Error(`Failed to update department: ${res.statusText}`);
  return res.json();
}

export async function deleteDepartment(id: string): Promise<void> {
  const res = await fetchWithTimeout(`${BASE}/api/departments/${id}`, { method: 'DELETE' });
  if (!res.ok) throw new Error(`Failed to delete department: ${res.statusText}`);
}

// Share links

export interface ShareLink {
  token: string;
  artifact_id: string;
  created_by: string;
  has_password: boolean;
  expires_at?: string;
  created_at: string;
  revoked: boolean;
  url?: string;
}

export async function createShareLink(
  artifactId: string,
  password?: string,
  expiresIn?: string,
): Promise<ShareLink> {
  const res = await fetchWithTimeout(`${BASE}/api/artifacts/${artifactId}/share-link`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: password || '', expires_in: expiresIn || '' }),
  });
  if (!res.ok) throw new Error(`Failed to create share link: ${res.statusText}`);
  return res.json();
}

export async function listShareLinks(artifactId: string): Promise<ShareLink[]> {
  const res = await fetchWithTimeout(`${BASE}/api/artifacts/${artifactId}/share-links`);
  if (!res.ok) throw new Error(`Failed to list share links: ${res.statusText}`);
  return res.json();
}

export async function revokeShareLink(token: string): Promise<void> {
  const res = await fetchWithTimeout(`${BASE}/api/share-links/${token}`, { method: 'DELETE' });
  if (!res.ok) throw new Error(`Failed to revoke share link: ${res.statusText}`);
}

export async function resolveShareLink(
  token: string,
  password?: string,
): Promise<{ name: string; status: string; url: string; target: string }> {
  if (password) {
    const res = await fetch(`${BASE}/api/share/${token}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    });
    if (res.status === 401) throw new Error('invalid_password');
    if (!res.ok) throw new Error('not_found');
    return res.json();
  }
  const res = await fetch(`${BASE}/api/share/${token}`);
  if (res.status === 401) throw new Error('password_required');
  if (!res.ok) throw new Error('not_found');
  return res.json();
}

// SSE event types

export interface ArtifactEvent {
  id: string;
  type: 'artifact.status_changed' | 'artifact.deleted';
  artifact_id: string;
  status?: string;
  error?: string;
  timestamp: string;
}

/**
 * Subscribe to real-time artifact lifecycle events via SSE.
 * Returns the EventSource instance for cleanup (call .close()).
 */
export function subscribeToEvents(
  onEvent: (event: ArtifactEvent) => void,
  onError?: (err: Event) => void,
): EventSource {
  const es = new EventSource(`${BASE}/api/events`);

  es.addEventListener('artifact.status_changed', (e) => {
    onEvent(JSON.parse((e as MessageEvent).data));
  });

  es.addEventListener('artifact.deleted', (e) => {
    onEvent(JSON.parse((e as MessageEvent).data));
  });

  if (onError) {
    es.onerror = onError;
  }

  return es;
}
