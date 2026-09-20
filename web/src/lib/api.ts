// Types are hand-written and mirrored by a golden-file test in Go, so drift
// fails in CI rather than turning up as an undefined in a browser.

export interface ClusterInfo {
  name: string;
  profile: string;
  profileName?: string;
  namespace?: string;
  chartRepo?: string;
  catalog: string;
  catalogRef?: string;
  version: string;
  peers?: Peer[];
  warnings?: string[];
}

export interface Peer {
  name: string;
  url: string;
}

export interface Deployment {
  release: string;
  namespace: string;
  chart?: string;
  status?: string;
  revision: number;
  updated?: string;
  managed: boolean;
  model?: string;
  variant?: string;
  catalogRef?: string;
  drift?: string;
}

export interface DeploymentsResponse {
  cluster: string;
  catalogRef: string;
  deployments: Deployment[];
  summary: { total: number; untracked: number; catalogBehind: number };
}

export interface Chart {
  name: string;
  version: string;
}

export interface Requires {
  gpus: number;
  nodes?: number;
  topology?: string;
  gpuProduct?: string[];
  rdma?: boolean;
}

export interface IndexVariant {
  id: string;
  engine: string;
  default?: boolean;
  description?: string;
  chart: Chart;
  requires: Requires;
}

export interface IndexModel {
  name: string;
  displayName?: string;
  description?: string;
  family?: string;
  tags?: string[];
  source: { hf: string; sizeGiB?: number };
  variants: IndexVariant[];
  path: string;
}

export interface CatalogResponse {
  ref: string;
  source: string;
  index: { apiVersion: string; count: number; models: IndexModel[] };
}

export interface Variant extends IndexVariant {
  image?: { repository: string; tag: string };
  values?: Record<string, unknown>;
}

export interface Entry {
  apiVersion: string;
  name: string;
  servedName?: string;
  displayName?: string;
  description?: string;
  family?: string;
  license?: string;
  tags?: string[];
  source: { hf: string; sizeGiB?: number; requiredGlobs?: string[] };
  variants: Variant[];
}

export interface Node {
  Name: string;
  GPUProduct: string;
  GPUs: number;
  Schedulable: boolean;
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = (await res.json()).error ?? msg;
    } catch {
      // Body was not the JSON error envelope; the status line is all we have.
    }
    throw new ApiError(msg, res.status);
  }
  return res.json() as Promise<T>;
}

export const api = {
  cluster: () => get<ClusterInfo>("/api/cluster"),
  deployments: () => get<DeploymentsResponse>("/api/deployments"),
  catalog: () => get<CatalogResponse>("/api/catalog"),
  model: (name: string) =>
    get<{ ref: string; entry: Entry }>(`/api/catalog/${encodeURIComponent(name)}`),
  nodes: () => get<{ cluster: string; nodes: Node[] }>("/api/nodes"),
};
