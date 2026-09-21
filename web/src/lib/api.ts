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
  allowDeploy: boolean;
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
  version?: string;
  phase?: string;
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

export interface IndexVersion {
  version: string;
  path: string;
  digest: string;
  variants: IndexVariant[];
}

export interface IndexModel {
  name: string;
  displayName?: string;
  description?: string;
  family?: string;
  tags?: string[];
  source: { hf: string; sizeGiB?: number };
  latest: string;
  versions: IndexVersion[];
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
  version: string;
  digest?: string;
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
  model: (name: string, version?: string) =>
    get<{ ref: string; entry: Entry }>(
      `/api/catalog/${encodeURIComponent(name)}` + (version ? `?version=${encodeURIComponent(version)}` : ""),
    ),
  nodes: () => get<{ cluster: string; nodes: Node[] }>("/api/nodes"),
  status: (ns: string, release: string) =>
    get<ReleaseStatus>(
      `/api/releases/${encodeURIComponent(ns)}/${encodeURIComponent(release)}/status`,
    ),
  releasePlan: (namespace: string, release: string) =>
    get<Plan>(
      `/api/releases/${encodeURIComponent(namespace)}/${encodeURIComponent(release)}/plan`,
    ),
};

export interface Plan {
  apiVersion: string;
  release: { name: string; namespace: string };
  source: {
    catalog?: string;
    ref?: string;
    model: string;
    version?: string;
    digest?: string;
    variant: string;
  };
  chart: { name: string; version: string; repo?: string; path?: string };
  engine: string;
  profile: string;
  values: Record<string, unknown>;
  provenance?: Record<string, string>;
  helmfile?: string;
  edits?: Record<string, unknown>;
  hash: string;
}

export interface DiffResult {
  planHash: string;
  changed: boolean;
  output: string;
  revision: number;
  exists: boolean;
}

export interface ApplyResult {
  planHash: string;
  release: string;
  revision: number;
  output: string;
  status: string;
  statusError?: string;
}

export interface PlanRequest {
  model?: string;
  fromRelease?: string;
  version?: string;
  variant?: string;
  release?: string;
  namespace?: string;
  serviceId?: string;
  localPath?: string;
  overrides?: Record<string, unknown>;
  overridesYAML?: string;
  editsYAML?: string;
}

export interface Pod {
  name: string;
  phase: string;
  ready: boolean;
  restarts: number;
  node?: string;
  message?: string;
  ageSeconds: number;
}

export interface ReleaseStatus {
  release: string;
  namespace: string;
  exists: boolean;
  revision: number;
  helmStatus?: string;
  pods: Pod[];
  ready: number;
  total: number;
  route?: string;
  warning?: string;
}

export interface ProbeResult {
  url: string;
  ok: boolean;
  status?: number;
  latencyMs: number;
  models?: string[];
  error?: string;
  body?: string;
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = (await res.json()).error ?? msg;
    } catch {
      // Not the JSON error envelope.
    }
    throw new ApiError(msg, res.status);
  }
  return res.json() as Promise<T>;
}

export const deployApi = {
  plan: (req: PlanRequest) => post<Plan>("/api/plans", req),
  diff: (req: PlanRequest | { planHash: string }) => post<DiffResult>("/api/diff", req),
  apply: (planHash: string, expectRevision: number) =>
    post<ApplyResult>("/api/apply", { planHash, expectRevision }),
  install: (planHash: string) => post<ApplyResult>("/api/install", { planHash }),
  probe: (ns: string, release: string) =>
    post<ProbeResult>(
      `/api/releases/${encodeURIComponent(ns)}/${encodeURIComponent(release)}/probe`,
      {},
    ),
};
