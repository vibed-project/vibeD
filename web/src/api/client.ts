export interface ArtifactSummary {
  id: string;
  name: string;
  status: 'pending' | 'building' | 'deploying' | 'running' | 'failed' | 'deleted';
  target: 'knative' | 'kubernetes' | 'wasmcloud';
  url?: string;
  created_at: string;
  updated_at: string;
}

export interface Artifact extends ArtifactSummary {
  image_ref?: string;
  port?: number;
  env_vars?: Record<string, string>;
  language?: string;
  error?: string;
  storage_ref?: string;
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

const BASE = '';

export async function fetchArtifacts(status?: string): Promise<ArtifactSummary[]> {
  const params = status ? `?status=${status}` : '';
  const res = await fetch(`${BASE}/api/artifacts${params}`);
  if (!res.ok) throw new Error(`Failed to fetch artifacts: ${res.statusText}`);
  const data = await res.json();
  return data ?? [];
}

export async function fetchArtifact(id: string): Promise<Artifact> {
  const res = await fetch(`${BASE}/api/artifacts/${id}`);
  if (!res.ok) throw new Error(`Failed to fetch artifact: ${res.statusText}`);
  return res.json();
}

export async function fetchLogs(id: string): Promise<LogsResponse> {
  const res = await fetch(`${BASE}/api/artifacts/${id}/logs`);
  if (!res.ok) throw new Error(`Failed to fetch logs: ${res.statusText}`);
  return res.json();
}

export async function deleteArtifact(id: string): Promise<void> {
  const res = await fetch(`${BASE}/api/artifacts/${id}`, { method: 'DELETE' });
  if (!res.ok) throw new Error(`Failed to delete artifact: ${res.statusText}`);
}

export async function fetchTargets(): Promise<TargetInfo[]> {
  const res = await fetch(`${BASE}/api/targets`);
  if (!res.ok) throw new Error(`Failed to fetch targets: ${res.statusText}`);
  return res.json();
}
