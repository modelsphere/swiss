# Swiss: a deploy control plane for the sglang and vllm charts

Swiss composes a model release from a public catalog, a site profile and
deploy-time settings, then hands it to helm. It feeds the existing charts,
`helmfile.yaml` and the `Makefile` targets rather than replacing them.

- **Built:** the catalog, `swiss` (CLI), `swissd` (HTTP server with an embedded
  web UI, write path behind `server.allowDeploy`).
- **Not built:** preflight, `emit`, auth.

## Components

| piece | artifact | runtime |
| --- | --- | --- |
| **swiss-catalog** | published static files, data only | none |
| **core** | Go module, `internal/` | library |
| **`swiss`** | binary | CLI, stateless |
| **`swissd`** | binary | HTTP server |
| **web UI** | React + TypeScript | embedded into `swissd` |

Three things are deployed: the catalog, `swissd`, and the engine charts in the
helm repo (`https://modelsphere.github.io/helm-charts` by default).

## Layers

Composition is a few inputs with key sets and strict precedence. Later layer overrides.


| layer | owns |
| --- | --- |
| helm chart defaults | things defaults in sglang/vllm chart values.yaml |
| **site defaults** (`defaults`) | the site's preferences, config it in `defaults.features` in the profile, the `cart`, `serviceMonitor`, `sloRequirement` and `scaler` `enabled` flags, unset on |
| **catalog variant** | model deployment recipes define in [model-catalog](https://github.com/modelsphere/model-catalog) and how it parallelizes: `model.*`, `extraArgs`, `lws.*`, `env`, `image.repository` |
| **site profile** | what makes it work here: the registry mirror, cache paths, route ConfigMaps, namespace, the `model.localPath` template, the default scheduler and priority class |
| **derived** | swiss's rules, above the variant: a section the form filled in is switched on, `cache.maxSlotsPerNode` |
| **user form** | how much, where, how routed: `serviceId`, `replicaCount`, `scaler.*`, `modelRoute.*`, scheduling |
| **plan editor** | the escape hatch — any key, exempt from ownership, labelled `edit` |
| **autoscale** | `replicaCount` from PD autoscaling, applied last, so it wins over the form and the editor |


## Pipeline

```
catalog entry ─┐
site profile  ─┼─► compose ─► Plan ─► diff ─► apply | install ─► helm
form / flags  ─┘             (hash)     ↓
                                   revision lock
```

| endpoint | |
| --- | --- |
| `POST /api/plans` | compose; returns the plan and stores it by hash |
| `POST /api/diff` | diff a stored plan against the live release |
| `POST /api/apply` | upgrade, asserting the diffed revision when one is supplied |
| `POST /api/install` | create |
| `DELETE /api/releases/{ns}/{release}` | uninstall |
| `POST /api/releases/{ns}/{release}/chat` | health check: one real inference request |
| `GET /api/runs`, `GET /api/runs/{id}` | the operation log; output is per row, not in the list |
| `GET /api/nodes` | the GPU inventory: type, allocatable, in use, and what holds it |
| `GET /api/profile` | the site profile as parsed, after defaults |
| `GET/PUT/DELETE /api/releases/{ns}/{release}/autoscaling` | PD slice bounds for one release |


## PD autoscaling

A prefill/decode chart fails to render when `pd.enabled` and `scaler.enabled` are both set, so LLMScaler cannot own this. One DisaggregatedSet replica is one slice, and the slice count is `replicaCount` in the chart values, so a scale is a helm upgrade (or an LLMService spec update) of that one key.

swissd is the single writer. The chart refuses more than one replica, there is no leader election and no lease, and the only lock is the in-process per-release mutex. decision-gen does not write the cluster. The API owns `config.yaml` in ConfigMap `swiss-autoscale-<release>`; the loop owns `status.yaml`.

```
every scaler.autoscalePeriod (default 30s)
  list swiss-autoscale-* (owner=swiss), skip disabled
  snapshot guards, then GET decision-gen /decisions?namespace=&serviceId=
  want = clamp(active, minSlices, maxSlices)
  want == current -> record status
  else replace only the autoscale layer and upgrade, expectRevision = this tick's revision
```

`PUT` body is `{enabled, minSlices, maxSlices}` with `1 <= min <= max`. The release must be one swiss manages, and the plan must say `pd.enabled: true` without `pd.colocate: true`. `PUT {enabled: false}` keeps the ConfigMap; that is not "enabled" for carry-forward. `GET` is allowed when deploys are off and when the release is not managed (`{found: false}` if no ConfigMap). `PUT` and `DELETE` are 403 on a read-only swissd.

| rule | what happens to `replicaCount` |
| --- | --- |
| user upgrade while enabled | the live `autoscale` layer is carried and still wins |
| user upgrade while disabled, or after DELETE | the layer is not carried; form and edit apply again |
| rollback while enabled | the current release's `autoscale` layer is overlaid on the archived plan and the hash is recomputed |
| rollback while not enabled | the archived plan is reapplied as stored |
| DELETE | removes only the ConfigMap; the live layer stays until the next user upgrade |
| `swiss plan` (CLI) | does not carry the layer; the next tick that wants a different count writes it back |

A user apply and the loop take the same per-release lock with `TryLock`. A busy lock is HTTP 409 `release <ns>/<name> has an apply in progress`; the request does not wait. Uninstall takes that lock the same way. The loop does not hold it across the decision-gen call. A conflict on `expectRevision`, or a busy lock between the snapshot and the apply, is recorded and not retried until the next tick.

The first matching guard wins. A decision error is a hold only when no guard matched. There is no cooldown, no step cap, no stuck timeout.

| holdReason | when |
| --- | --- |
| `apply in progress` | the per-release lock is busy |
| `helm pending` | helm status starts with `pending` |
| `plan applying` | the plan ConfigMap phase is `applying` |
| `llmservice applying` / `pending` / `observedGeneration behind` | LLMService only |
| `not disaggregated` | `pd.enabled` is not true, or `pd.colocate` is true, including a plan that stopped being PD after the config was saved |
| `disaggregatedset missing` / `observedGeneration behind` | the DisaggregatedSet |
| `role not ready` / `role not updated` | any `status.roleStatuses` entry |
| `router not ready` | any `{fullname}-router-s<i>` for `i < current` is missing or not ready |
| the decision error, or `no decision` | decision-gen failed; this is never read as 0 replicas |
| `autoscale config invalid` | `minSlices < 1` or `minSlices > maxSlices` |

decision-gen: `GET {scaler.serverAddress}/decisions?namespace=&serviceId=` (default `http://decision-gen.llm-scaler.svc:80`), `apiVersion` `llmscaling.inference.x-k8s.io/v1alpha1`, exactly one row for that namespace and serviceId, `replicas.active` present and in `[0, MaxInt32]`. Non-200 and transport errors are holds. `scaler.serverHeaders` are sent. Timeout 5s.

## Catalog

See [model-catalog](https://github.com/modelsphere/model-catalog).

**A writable catalog is edited through swissd.** A profile entry with
`writable: true` is a catalog-server (schema v2) that takes writes on its
`api/v2/`. swissd stands in for the catalog's address and passes that API
through as it is -- no endpoint of its own to keep in step with catalog-server's,
and it checks nothing itself: catalog-server holds the rules.

```
/api/catalogs/<name>/api/v2/<path>           e.g. POST init (setup), PUT catalog (settings),
                                             POST models, PUT models/<m>/metadata, PUT models/<m>/variants/<id>
  profile catalogs[<name>].writable          else 403
  Secret[catalog.<name>.token]               else 409
  ──▶ <url>api/v2/<path>, query, headers and body as sent, Authorization: Bearer <token>
  a write's 2xx ──▶ drop the cached index, so the next read shows the commit
```
