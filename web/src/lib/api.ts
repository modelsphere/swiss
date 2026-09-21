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

export interface Run {
  id: number;
  namespace: string;
  release: string;
  action: string;
  planHash: string;
  actor?: string;
  changed: boolean;
  error?: string;
  // Only on a single-run fetch: the list omits it, because helmfile output runs
  // to tens of kilobytes a row.
  output?: string;
  startedAt: string;
  endedAt: string;
}

export interface RunsResponse {
  runs: Run[];
  // False when this swissd has no database. The log is then empty rather than
  // broken -- losing the volume costs the history and nothing else.
  hasStore: boolean;
}

export interface RunFilter {
  namespace?: string;
  release?: string;
  action?: string;
  limit?: number;
}

// Field names are Go's here: cluster.Node predates the json tags the rest of
// the API carries, and renaming them is a server change, not a client one.
export interface RouteAuth {
  header?: string;
  prefix?: string;
  // Names, never the credential: the key lives in the Secret these point at.
  secretRef?: string;
  secretKey?: string;
  headers?: Record<string, string>;
}

export interface SiteProfile {
  name: string;
  namespace?: string;
  chartRepo?: string;
  chartPath?: string;
  registry?: { mirror?: string };
  model: { pathTemplate: string; overrides?: Record<string, string> };
  cache?: { enabled?: boolean; hostPath?: string };
  scaler?: { serverAddress?: string; serverHeaders?: Record<string, string> };
  route?: {
    nginxConfigMap?: string;
    nginxService?: string;
    nginxSelector?: string;
    monitorConfigMap?: string;
    nginxPort?: number;
    auth?: RouteAuth;
  };
  nodes?: { gpusPerNode?: number };
  createNamespace?: boolean;
  extra?: Record<string, unknown>;
}

export interface ProfileResponse {
  source: string;
  cluster: string;
  profile: SiteProfile;
}

export interface Node {
  Name: string;
  GPUProduct: string;
  // Allocatable, not capacity — what a fit check may actually use.
  GPUs: number;
  Schedulable: boolean;
  Ready: boolean;
  Kubelet?: string;
  Taints?: string[];
  // Absent when the cluster-wide pod list was refused: unknown, not zero.
  gpusUsed?: number;
  gpusFree?: number;
  gpuPods?: GPUPod[];
}

export interface GPUPod {
  namespace: string;
  name: string;
  gpus: number;
}

export interface NodesResponse {
  cluster: string;
  nodes: Node[];
  summary: { nodes: number; gpus: number; gpusUsed: number };
  // Set when GPU usage could not be computed — rbac.nodeGPUUsage is off, or
  // the cluster-wide pod list was refused.
  usageError?: string;
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
  nodes: () => get<NodesResponse>("/api/nodes"),
  profile: () => get<ProfileResponse>("/api/profile"),
  runs: (f: RunFilter = {}) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(f)) if (v) q.set(k, String(v));
    return get<RunsResponse>("/api/runs" + (q.size ? `?${q}` : ""));
  },
  run: (id: number) => get<Run>(`/api/runs/${id}`),
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
  // The form's layer as the user gave it, kept apart from values so an upgrade
  // can seed its form from what was actually set rather than from the merge.
  overrides?: Record<string, unknown>;
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

export interface PlanStatus {
  phase: string;
  action?: string;
  revision?: number;
  startedAt?: string;
  updatedAt?: string;
  error?: string;
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
  // Written beside the release on every apply. The only thing that can say an
  // apply was started and never finished -- helm reports the last one that did.
  planStatus?: PlanStatus;
}

export type ChatApi = "chat" | "completions" | "messages";

// The entrypoint may need a credential. The site profile can name one (a Secret
// it points at), and these override it per call — which is what lets an operator
// test a key before it is written into the cluster. Never stored by the browser.
export interface EntrypointAuth {
  apiKey?: string;
  headers?: Record<string, string>;
}

export interface ChatRequest extends EntrypointAuth {
  api?: ChatApi;
  prompt?: string;
  model?: string;
  maxTokens?: number;
}

export interface ChatResult {
  url: string;
  api: ChatApi;
  model?: string;
  ok: boolean;
  status?: number;
  latencyMs: number;
  reply?: string;
  error?: string;
  body?: string;
  // Header names only, never values: enough to tell "no key was sent" from
  // "the key was wrong".
  sentHeaders?: string[];
}

export interface UninstallResult {
  release: string;
  namespace: string;
  output: string;
  planError?: string;
}

export interface ProbeResult {
  url: string;
  ok: boolean;
  status?: number;
  latencyMs: number;
  models?: string[];
  error?: string;
  body?: string;
  sentHeaders?: string[];
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

async function del<T>(path: string): Promise<T> {
  const res = await fetch(path, { method: "DELETE", headers: { Accept: "application/json" } });
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
  // expectRevision is the optimistic lock a diff computed. It is left out when
  // no diff was run, and the server reads a missing revision as asserting
  // nothing rather than as revision zero.
  apply: (planHash: string, expectRevision?: number) =>
    post<ApplyResult>("/api/apply", { planHash, expectRevision }),
  install: (planHash: string) => post<ApplyResult>("/api/install", { planHash }),
  probe: (ns: string, release: string, auth: EntrypointAuth = {}) =>
    post<ProbeResult>(
      `/api/releases/${encodeURIComponent(ns)}/${encodeURIComponent(release)}/probe`,
      auth,
    ),
  // A real inference request. /v1/models proves the route resolves; this proves
  // the model can generate a token, which is not the same question.
  chat: (ns: string, release: string, req: ChatRequest = {}) =>
    post<ChatResult>(
      `/api/releases/${encodeURIComponent(ns)}/${encodeURIComponent(release)}/chat`,
      req,
    ),
  uninstall: (ns: string, release: string) =>
    del<UninstallResult>(`/api/releases/${encodeURIComponent(ns)}/${encodeURIComponent(release)}`),
};
