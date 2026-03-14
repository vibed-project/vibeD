export interface ArtifactSummary {
  id: string;
  name: string;
  owner_id?: string;
  status: 'pending' | 'building' | 'deploying' | 'running' | 'failed' | 'deleted';
  target: 'knative' | 'kubernetes' | 'wasmcloud';
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
  user_id: string;
  role: string;
}

export interface OrganizationInfo {
  name: string;
}

const BASE = '';
const DEFAULT_TIMEOUT_MS = 30_000;

async function fetchWithTimeout(url: string, opts?: RequestInit): Promise<Response> {
  const controller = new AbortController();
  const id = setTimeout(() => controller.abort(), DEFAULT_TIMEOUT_MS);
  try {
    return await fetch(url, { ...opts, signal: controller.signal });
  } finally {
    clearTimeout(id);
  }
}

export async function fetchArtifacts(status?: string): Promise<ArtifactSummary[]> {
  const params = status ? `?status=${status}` : '';
  const res = await fetchWithTimeout(`${BASE}/api/artifacts${params}`);
  if (!res.ok) throw new Error(`Failed to fetch artifacts: ${res.statusText}`);
  const data = await res.json();
  return data ?? [];
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
