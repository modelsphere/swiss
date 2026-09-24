// Types are hand-written and mirrored by a golden-file test in Go, so drift
// fails in CI rather than turning up as an undefined in a browser.

export interface ClusterInfo {
  name: string;
  profile: string;
  profileName?: string;
  // Scheduling defaults from the site profile, shown as form placeholders.
  priorityClassName?: string;
  schedulerName?: string;
  namespace?: string;
  chartRepo?: string;
  // The site's own answer, which a deploy can add to but not take away.
  createNamespace?: boolean;
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
  model?: string;
  variant?: string;
  catalogRef?: string;
  version?: string;
  phase?: string;
  drift?: string;
  // The path the entrypoint publishes this release on. Absent when the plan
  // names no route.
  route?: string;
}

export interface DeploymentsResponse {
  cluster: string;
  catalogRef: string;
  deployments: Deployment[];
  page: number;
  perPage: number;
  // total is every release swiss deployed, counted from a metadata-only list.
  // catalogBehind is only the rows on this page -- knowing it for the rest means
  // reading their plans, which is the cost paging exists to avoid.
  summary: { total: number; catalogBehind: number };
}

export interface Chart {
  name: string;
  version: string;
}

export interface Requires {
  gpus: number;
  // Accelerator brand. Absent means nvidia; it decides the extended resource
  // the pod requests, not just which card matches.
  vendor?: "nvidia" | "ascend" | "cambricon" | "hygon" | "amd";
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
  source: { hf: string; revision?: string; sizeGiB?: number };
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
  source: { hf: string; revision?: string; sizeGiB?: number };
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
  // Why it was done, typed by whoever did it. Nothing derives it -- a diff says
  // what changed and only a person can say why.
  note?: string;
  // The revision this left the release at. Absent for an operation that
  // produced none: a failed apply, an uninstall, or a row written before
  // swissd recorded it. A row that has one is a rollback target.
  revision?: number;
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
    // The public base URL openresty is served on; a model's URL is
    // <gateway>/<route>. Display only -- no check calls it.
    gateway?: string;
    nginxConfigMap?: string;
    nginxService?: string;
    nginxSelector?: string;
    monitorConfigMap?: string;
    nginxPort?: number;
    auth?: RouteAuth;
  };
  schedule?: { priorityClassName?: string; schedulerName?: string };
  nodes?: { gpusPerNode?: number };
  createNamespace?: boolean;
  extra?: Record<string, unknown>;
}

export interface ProfileResponse {
  source: string;
  cluster: string;
  profile: SiteProfile;
  // The stored text, which is what the editor edits. The parsed view above is
  // what swissd composes against; editing that would drop every comment.
  yaml?: string;
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

// onUnauthorized is registered by the session gate. A 401 from any call means
// the login ended while the page was open -- the token expired, or the password
// was changed -- and the whole app should go back to the login rather than each
// page rendering its own "not logged in" error.
let unauthorized: (() => void) | null = null;
export function onUnauthorized(f: (() => void) | null) {
  unauthorized = f;
}

async function fail(res: Response): Promise<never> {
  let msg = res.statusText;
  try {
    msg = (await res.json()).error ?? msg;
  } catch {
    // Body was not the JSON error envelope; the status line is all we have.
  }
  if (res.status === 401) unauthorized?.();
  throw new ApiError(msg, res.status);
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) return fail(res);
  return res.json() as Promise<T>;
}

export interface Session {
  authenticated: boolean;
  user?: string;
  expiresAt?: string;
  // Absent until authenticated: whether the site profile has been written yet.
  // A fresh install has none, and the first login lands on the setup page.
  initialized?: boolean;
  // This swissd runs with no login at all.
  authDisabled?: boolean;
}

export const api = {
  // Asked before anything renders: whether this browser is logged in, and
  // whether the site has been set up. 200 either way -- "not logged in" is the
  // expected first answer, and an error status for the normal case makes every
  // other 401 harder to read.
  session: () => get<Session>("/api/session"),
  login: (username: string, password: string) =>
    post<Session & { token: string }>("/api/login", { username, password }),
  logout: () => post<{ ok: boolean }>("/api/logout", {}),
  cluster: () => get<ClusterInfo>("/api/cluster"),
  deployments: (page = 1, perPage = 25) =>
    get<DeploymentsResponse>(`/api/deployments?page=${page}&perPage=${perPage}`),
  catalog: () => get<CatalogResponse>("/api/catalog"),
  model: (name: string, version?: string) =>
    // localPath is what the site's template resolves to for this model, so a
    // form can show the real default rather than describe the template.
    // imageRepository is each variant's image after the site's mirror rewrite,
    // by variant id, so the form can offer that rewrite as a choice.
    get<{
      ref: string;
      entry: Entry;
      localPath?: string;
      pathTemplate?: string;
      imageRepository?: Record<string, string>;
    }>(
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
  revisions: (ns: string, release: string) =>
    get<RevisionsResponse>(
      `/api/releases/${encodeURIComponent(ns)}/${encodeURIComponent(release)}/revisions`,
    ),
  // What helm holds for a revision, chart defaults included. The plan beside
  // the release says what swiss composed; this says what helm was given.
  revisionValues: (ns: string, release: string, revision: number) =>
    get<RevisionValues>(
      `/api/releases/${encodeURIComponent(ns)}/${encodeURIComponent(release)}/revisions/${revision}/values`,
    ),
  // The plan a revision was applied from, read from its archive. What the
  // rollback page shows: the settings that would come back, which are not what
  // recomposing that version against today's catalog would produce.
  revisionPlan: (ns: string, release: string, revision: number) =>
    get<Plan>(
      `/api/releases/${encodeURIComponent(ns)}/${encodeURIComponent(release)}/revisions/${revision}/plan`,
    ),
  releasePlan: (namespace: string, release: string) =>
    get<Plan>(
      `/api/releases/${encodeURIComponent(namespace)}/${encodeURIComponent(release)}/plan`,
    ),
  // The profile a site that has none starts from. Served rather than kept in
  // the app: the default and the thing that validates it are one definition.
  profileTemplate: () => get<ProfileResponse>("/api/profile/template"),
  // One of the two shapes, never both: the form sends the parsed profile and
  // swissd renders the document; the text editor sends it as typed, comments
  // and all.
  saveProfile: (body: { profile: SiteProfile } | { yaml: string }) =>
    put<ProfileResponse>("/api/profile", body),
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
  // The override documents a release is applied through, one per layer, each
  // exactly as that layer wrote it. Merged in order, last writer wins. There is
  // no separate copy of the form's or the editor's input -- those are two of
  // these documents, and reading them from anywhere else is how an upgrade
  // silently seeds an empty form.
  layers: Record<string, Record<string, unknown>>;
  helmfile?: string;
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
  gpuProducts?: string[];
  overrides?: Record<string, unknown>;
  overridesYAML?: string;
  editsYAML?: string;
  // Composed into the plan as helmfile's createNamespace, on top of the site
  // profile's own setting. helm creates the namespace; this is the plan saying
  // it may.
  createNamespace?: boolean;
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
  // Kept beside the release as well as in the audit log, so the reason for the
  // last change outlives the database it was also written to.
  note?: string;
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
  // That route on the site's gateway, when the profile names one.
  url?: string;
  // What the engine advertises — servedName, not the catalog id.
  model?: string;
  // How the entrypoint expects a key. Never the key itself. authPrefix is "" on
  // a custom header that carries the key bare, so it is always present.
  authHeader?: string;
  authPrefix?: string;
  warning?: string;
  // Written beside the release on every apply. The only thing that can say an
  // apply was started and never finished -- helm reports the last one that did.
  planStatus?: PlanStatus;
}

// The wire format a check speaks. The values are the server's; the labels the
// page shows them under name the protocol, not the verb.
export type InferenceApi = "chat" | "completions" | "messages";

// The entrypoint may need a credential. The site profile can name one (a Secret
// it points at), and these override it per call — which is what lets an operator
// test a key before it is written into the cluster. Never stored by the browser.
export interface EntrypointAuth {
  apiKey?: string;
  headers?: Record<string, string>;
}

export interface ChatRequest extends EntrypointAuth {
  api?: InferenceApi;
  prompt?: string;
  model?: string;
  maxTokens?: number;
}

export interface ChatResult {
  url: string;
  api: InferenceApi;
  model?: string;
  ok: boolean;
  status?: number;
  latencyMs: number;
  reply?: string;
  // Where a reasoning model puts its output, and all there is of it when the
  // token budget ran out before the answer.
  reasoning?: string;
  finishReason?: string;
  error?: string;
  body?: string;
  // Header names only, never values: enough to tell "no key was sent" from
  // "the key was wrong".
  sentHeaders?: string[];
  // The request the server made, as a runnable line. The key is a shell
  // variable in it, never the value.
  curl?: string;
}

export interface Revision {
  revision: number;
  model?: string;
  version?: string;
  variant?: string;
  // "name-version", the way helm names a chart. Two revisions of one model
  // version can differ only by this.
  chart?: string;
  planHash?: string;
  // The live one, which is the workspace rather than an archive.
  current?: boolean;
}

// `helm get values --all` for one revision, as the yaml helm prints.
export interface RevisionValues {
  namespace: string;
  release: string;
  revision: number;
  values: string;
}

export interface RevisionsResponse {
  release: string;
  namespace: string;
  revisions: Revision[];
}

export interface RevisionDiff extends DiffResult {
  toRevision: number;
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
  curl?: string;
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) return fail(res);
  return res.json() as Promise<T>;
}

async function put<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) return fail(res);
  return res.json() as Promise<T>;
}

async function del<T>(path: string): Promise<T> {
  const res = await fetch(path, { method: "DELETE", headers: { Accept: "application/json" } });
  if (!res.ok) return fail(res);
  return res.json() as Promise<T>;
}

export const deployApi = {
  plan: (req: PlanRequest) => post<Plan>("/api/plans", req),
  diff: (req: PlanRequest | { planHash: string }) => post<DiffResult>("/api/diff", req),
  // expectRevision is the optimistic lock a diff computed. It is left out when
  // no diff was run, and the server reads a missing revision as asserting
  // nothing rather than as revision zero.
  apply: (planHash: string, expectRevision?: number, note?: string) =>
    post<ApplyResult>("/api/apply", { planHash, expectRevision, note }),
  install: (planHash: string, note?: string) =>
    post<ApplyResult>("/api/install", { planHash, note }),
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
  // Optional, like every diff here. It carries back the live revision it saw,
  // which is what the rollback then asserts has not moved.
  diffRevision: (ns: string, release: string, toRevision: number) =>
    post<RevisionDiff>(
      `/api/releases/${encodeURIComponent(ns)}/${encodeURIComponent(release)}/revisions/${toRevision}/diff`,
      {},
    ),
  // expectRevision is what the page was showing. A rollback runs when something
  // is already wrong, which is when a second operator is most likely to be
  // acting on the same release, so it refuses rather than overwriting theirs.
  rollback: (
    ns: string,
    release: string,
    toRevision: number,
    expectRevision: number,
    note?: string,
  ) =>
    post<ApplyResult>(
      `/api/releases/${encodeURIComponent(ns)}/${encodeURIComponent(release)}/rollback`,
      { toRevision, expectRevision, note },
    ),
  uninstall: (ns: string, release: string) =>
    del<UninstallResult>(`/api/releases/${encodeURIComponent(ns)}/${encodeURIComponent(release)}`),
};
