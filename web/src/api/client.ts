export interface ArtifactSummary {
  id: string;
  name: string;
  owner_id?: string;
  status: 'pending' | 'building' | 'deploying' | 'running' | 'failed' | 'deleted';
  target: 'kubernetes' | 'sandbox' | 'runner';
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

// ApiError carries the numeric HTTP status so callers can branch on it reliably.
// Response.statusText is empty over HTTP/2 (the protocol dropped the reason
// phrase), so error handling must never depend on it — e.g. the dashboard's
// login gate keys off a 401 (issue #41).
export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

// httpError builds an ApiError from a failed response, tagging the message with
// the numeric status (not statusText, which is blank over HTTP/2).
function httpError(res: Response, context: string): ApiError {
  return new ApiError(res.status, `${context} (HTTP ${res.status})`);
}

export interface ArtifactListResult {
  artifacts: ArtifactSummary[];
  total: number;
}

// --- v0.3.1 /v1/apps (VibedApp) shape + mapping -----------------------------
// The UI's data model predates VibedApp; rather than rewrite every component we
// map the new App shape into the existing Artifact types here, so the rest of
// the frontend is unchanged.

interface ApiApp {
  app_id: string;
  name?: string;
  owner?: string;
  phase: 'Pending' | 'Claiming' | 'Starting' | 'Ready' | 'Suspended' | 'Failed';
  url?: string;
  runtime?: { lane?: 'fast' | 'general'; template?: string };
  last_deployed_at?: string;
}

// Exported so SSE consumers (App.tsx) can map the raw phase carried on
// enriched bridge events without a per-event refetch. Accepts any string:
// unknown phases read as pending, same as the default arm always did.
export function phaseToStatus(phase: string): ArtifactSummary['status'] {
  switch (phase) {
    case 'Ready':     return 'running';
    case 'Starting':  return 'deploying';
    case 'Claiming':  return 'building';
    case 'Failed':    return 'failed';
    case 'Suspended': // idle-suspended apps read as pending until next deploy
    case 'Pending':
    default:          return 'pending';
  }
}

function mapApp(a: ApiApp): Artifact {
  const ts = a.last_deployed_at ?? '';
  return {
    id: a.app_id,
    name: a.name ?? a.app_id,
    owner_id: a.owner,
    status: phaseToStatus(a.phase),
    // VibedApps run on microVM sandboxes (general) or workerd runners (fast).
    target: a.runtime?.lane === 'fast' ? 'runner' : 'sandbox',
    url: a.url,
    created_at: ts,
    updated_at: ts,
    version: 1, // VibedApp has no version history yet
    language: a.runtime?.template,
  };
}

// _status is accepted for signature compatibility with the legacy orchestrator
// client but ignored: /v1/apps has no status filter.
export async function fetchArtifacts(_status?: string, offset = 0, limit = 50): Promise<ArtifactListResult> {
  const params = new URLSearchParams();
  if (limit > 0) params.set('limit', String(limit));
  if (offset > 0) params.set('offset', String(offset));
  const qs = params.toString();
  const res = await fetchWithTimeout(`${BASE}/v1/apps${qs ? `?${qs}` : ''}`);
  if (!res.ok) throw httpError(res, "Failed to fetch apps");
  const data = await res.json();
  const items: ApiApp[] = data?.items ?? [];
  const artifacts = items.map(mapApp);
  // Prefer the server's post-filter total; fall back to a length-based
  // estimate against servers that predate the total field.
  const total = typeof data?.total === 'number' ? data.total : offset + artifacts.length;
  return { artifacts, total };
}

export async function fetchArtifact(id: string): Promise<Artifact> {
  const res = await fetchWithTimeout(`${BASE}/v1/apps/${id}`);
  if (!res.ok) throw httpError(res, "Failed to fetch app");
  return mapApp(await res.json());
}

export async function fetchLogs(id: string): Promise<LogsResponse> {
  // GET /v1/apps/{id}/logs streams the recent tail as Server-Sent Events
  // (`data: <line>` per event) and, without `?follow=true`, terminates once the
  // snapshot is sent — so a plain fetch resolves. The viewer polls this.
  const res = await fetchWithTimeout(`${BASE}/v1/apps/${id}/logs`);
  if (res.status === 404) return { artifact_id: id, logs: [] };
  if (!res.ok) throw httpError(res, "Failed to fetch logs");
  const logs = (await res.text())
    .split('\n')
    .filter((l) => l.startsWith('data: '))
    .map((l) => l.slice('data: '.length));
  return { artifact_id: id, logs };
}

/**
 * Follow an app's logs live via GET /v1/apps/{id}/logs?follow=true.
 *
 * The endpoint is SSE-shaped (`data: <line>` frames) but requires the same
 * Bearer token as every other /v1 call, and EventSource cannot set an
 * Authorization header (the /api/events dashboard stream gets away with a
 * bare EventSource only because no browser-held token is involved there).
 * So this streams over fetch() and parses SSE frames off the response's
 * ReadableStream. A plain fetchWithTimeout can't be used: its 30s abort
 * would kill a healthy long-lived tail.
 *
 * Resolves when the server ends the stream; rejects on HTTP errors, abort,
 * or an `event: error` frame from the server.
 */
export async function followLogs(
  id: string,
  onLine: (line: string) => void,
  opts?: { signal?: AbortSignal; onOpen?: () => void },
): Promise<void> {
  const token = getAuthToken();
  const headers = new Headers({ Accept: 'text/event-stream' });
  if (token) headers.set('Authorization', `Bearer ${token}`);
  const res = await fetch(`${BASE}/v1/apps/${id}/logs?follow=true`, {
    headers,
    signal: opts?.signal,
  });
  if (!res.ok) throw httpError(res, 'Failed to follow logs');
  if (!res.body) throw new Error('Log streaming unsupported by this browser');
  opts?.onOpen?.();

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  let eventName = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    const lines = buf.split('\n');
    buf = lines.pop() ?? ''; // keep the trailing partial line for the next chunk
    for (const line of lines) {
      if (line === '') {
        eventName = ''; // blank line = frame boundary, reset the event name
        continue;
      }
      if (line.startsWith('event: ')) {
        eventName = line.slice('event: '.length);
        continue;
      }
      if (!line.startsWith('data: ')) continue; // ids / heartbeat comments
      const data = line.slice('data: '.length);
      if (eventName === 'error') throw new Error(data);
      onLine(data);
    }
  }
}

export async function deleteArtifact(id: string): Promise<void> {
  const res = await fetchWithTimeout(`${BASE}/v1/apps/${id}`, { method: 'DELETE' });
  if (!res.ok && res.status !== 404) throw httpError(res, "Failed to delete app");
}

export async function fetchWhoami(): Promise<WhoAmI> {
  const res = await fetchWithTimeout(`${BASE}/api/whoami`);
  if (!res.ok) throw httpError(res, "Failed to fetch user info");
  return res.json();
}

export async function fetchOrganization(): Promise<OrganizationInfo> {
  const res = await fetchWithTimeout(`${BASE}/api/organization`);
  if (!res.ok) throw httpError(res, "Failed to fetch organization");
  return res.json();
}

// Deploy-history entry from GET /v1/apps/{id}/versions.
interface ApiVersion {
  version: number;
  timestamp: string;
  template?: string;
  lane?: string;
  source_hash?: string;
  rolled_back_from?: number;
}

export async function fetchVersions(id: string): Promise<ArtifactVersion[]> {
  const res = await fetchWithTimeout(`${BASE}/v1/apps/${id}/versions`);
  if (res.status === 404) return [];
  if (!res.ok) throw httpError(res, "Failed to fetch versions");
  const data = await res.json();
  const items: ApiVersion[] = data?.items ?? [];
  return items.map((v) => ({
    version_id: `v${v.version}`,
    artifact_id: id,
    version: v.version,
    image_ref: v.template ?? '',
    status: v.rolled_back_from ? `rolled back from v${v.rolled_back_from}` : '',
    created_at: v.timestamp,
    created_by: '',
  }));
}

export async function rollbackArtifact(id: string, version: number): Promise<void> {
  const res = await fetchWithTimeout(`${BASE}/v1/apps/${id}/rollback`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ version }),
  });
  if (!res.ok) throw httpError(res, "Failed to roll back app");
}

// User management (admin)

export async function fetchUsers(): Promise<User[]> {
  const res = await fetchWithTimeout(`${BASE}/api/users`);
  if (!res.ok) throw httpError(res, "Failed to fetch users");
  return res.json();
}

export async function createUser(name: string, email: string, role: string): Promise<User> {
  const res = await fetchWithTimeout(`${BASE}/api/users`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, email, role }),
  });
  if (!res.ok) throw httpError(res, "Failed to create user");
  return res.json();
}

export async function fetchUser(id: string): Promise<User> {
  const res = await fetchWithTimeout(`${BASE}/api/users/${id}`);
  if (!res.ok) throw httpError(res, "Failed to fetch user");
  return res.json();
}

export async function updateUser(id: string, updates: { role?: string; status?: string; department_id?: string }): Promise<User> {
  const res = await fetchWithTimeout(`${BASE}/api/users/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(updates),
  });
  if (!res.ok) throw httpError(res, "Failed to update user");
  return res.json();
}

export async function suspendUser(id: string): Promise<User> {
  const res = await fetchWithTimeout(`${BASE}/api/users/${id}`, { method: 'DELETE' });
  if (!res.ok) throw httpError(res, "Failed to suspend user");
  return res.json();
}

// Department management (admin)

export async function fetchDepartments(): Promise<Department[]> {
  const res = await fetchWithTimeout(`${BASE}/api/departments`);
  if (!res.ok) throw httpError(res, "Failed to fetch departments");
  return res.json();
}

export async function createDepartment(name: string): Promise<Department> {
  const res = await fetchWithTimeout(`${BASE}/api/departments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  });
  if (!res.ok) throw httpError(res, "Failed to create department");
  return res.json();
}

export async function updateDepartment(id: string, name: string): Promise<Department> {
  const res = await fetchWithTimeout(`${BASE}/api/departments/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  });
  if (!res.ok) throw httpError(res, "Failed to update department");
  return res.json();
}

export async function deleteDepartment(id: string): Promise<void> {
  const res = await fetchWithTimeout(`${BASE}/api/departments/${id}`, { method: 'DELETE' });
  if (!res.ok) throw httpError(res, "Failed to delete department");
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
  const res = await fetchWithTimeout(`${BASE}/v1/apps/${artifactId}/share-links`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: password || '', expires_in: expiresIn || '' }),
  });
  if (!res.ok) throw httpError(res, "Failed to create share link");
  return res.json();
}

export async function listShareLinks(artifactId: string): Promise<ShareLink[]> {
  const res = await fetchWithTimeout(`${BASE}/v1/apps/${artifactId}/share-links`);
  if (!res.ok) throw httpError(res, "Failed to list share links");
  const data: { items?: ShareLink[]; total?: number } = await res.json();
  return data.items ?? [];
}

export async function revokeShareLink(token: string): Promise<void> {
  const res = await fetchWithTimeout(`${BASE}/api/share-links/${token}`, { method: 'DELETE' });
  if (!res.ok) throw httpError(res, "Failed to revoke share link");
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
  // Enriched fields carried by the VibedApp event bridge (absent on legacy
  // orchestrator events — consumers must fall back to refetching then).
  // phase is the RAW VibedApp phase string (Pending/Claiming/Starting/Ready/
  // Suspended/Failed); on bridge events status carries the same raw phase,
  // so map through phaseToStatus rather than trusting status.
  name?: string;
  owner_id?: string;
  phase?: string;
  url?: string;
  template?: string;
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
