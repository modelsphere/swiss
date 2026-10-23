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
| **plan editor** | the escape hatch, applied last — any key, exempt from ownership, labelled `edit` |


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
| `DELETE /api/releases/{ns}/{release}` | uninstall; a helm release stays listed as `uninstalling` until helm has removed it and its pods are gone |
| `POST /api/releases/{ns}/{release}/chat` | health check: one real inference request |
| `GET /api/runs`, `GET /api/runs/{id}` | the operation log; output is per row, not in the list |
| `GET /api/nodes` | the GPU inventory: type, allocatable, in use, and what holds it |
| `GET /api/profile` | the site profile as parsed, after defaults |


## Uninstalling

A helm release stays on `GET /api/deployments` with `phase: uninstalling` until helm has removed the release and its pods are gone. `DELETE` returns when helm does; it does not wait for those pods to finish terminating.

| | |
| --- | --- |
| marker | plan ConfigMap `status.yaml` `phase: uninstalling`, plus label `swiss.modelsphere.dev/phase=uninstalling` so the metadata-only list can see it |
| memory | `namespace/release` on the process; `inflight` while that helm call has not returned, so a list cannot delete the plan underneath it |
| rebuild | the background loop at startup, and the first deployments list, fill that set from ConfigMaps carrying the label |
| drop | helm no longer has the release, and no pod matches `app=<release>-<chart>` (when the plan names a chart) or `app.kubernetes.io/instance=<release>` |
| check | every 5s from `Server.Run`, and again when the deployments list or that release's status is read |

```
DELETE
  ├─ no plan ConfigMap ─► helm uninstall ─► delete the ConfigMap
  └─ plan ConfigMap
        write phase + label
        helm uninstall
          ├─ error ─► phase failed, clear the label, keep the ConfigMap
          └─ ok ─► delete archive Secrets
                helm release still recorded, or pods match either selector
                  ├─ yes ─► stay listed (phase uninstalling)
                  └─ no  ─► delete the ConfigMap and drop the row
```

| situation | result |
| --- | --- |
| helm uninstall fails | `phase: failed` and the error; the plan stays; the row does not stay `uninstalling` |
| DELETE again, helm release already gone | 200, `output` is `uninstall already in progress` |
| DELETE again, helm release still recorded | helm uninstall runs again |
| install, upgrade, or rollback of that name | 409 `release <ns>/<name> is uninstalling` |
| ConfigMap delete fails | the row stays; the next check retries |
| pod list fails, or reading the helm release fails | the row stays |
| helm release still recorded and no pods (restart mid-uninstall) | the row stays `uninstalling`; the next DELETE runs helm again |
| swissd restarts | the label is read back; the row stays until the helm release and its pods are both gone |

An LLMService with `metadata.deletionTimestamp` set reports `phase: uninstalling` on the deployments row and `llmservice.phase` on status, whatever `status.phase` says. Install, upgrade, and rollback of that name are 409. `DELETE` of an LLMService still waits until the object is gone.

`phase` gains the value `uninstalling` on a deployment row, on helm `planStatus.phase`, and on `llmservice.phase`. No other field on those objects changes.


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
