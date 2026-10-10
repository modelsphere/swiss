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

- `swiss` and `swissd` are one Go module. All compose, validate, render and apply
  logic lives in `internal/`; both binaries are thin over it, so the server can
  never do something the CLI cannot.
- The SPA is embedded in the server binary — one artifact, no CORS, no second
  nginx.
- swissd's own chart is deliberately **not** in `helmfile.yaml`: that file is the
  model-release inventory, and swissd is not a model.

## Layers

Composition is four inputs with disjoint key sets and strict precedence. If two
layers could set one key, every question about this system becomes a question
about precedence.

| layer | owns |
| --- | --- |
| chart defaults | everything not claimed below |
| **catalog variant** | model identity and how it parallelizes: `model.*`, `extraArgs`, `lws.*`, `env`, `image` |
| **site profile** | what makes it work here: the registry mirror, cache paths, route ConfigMaps, namespace, the `model.localPath` template, the default scheduler and priority class |
| **user form** | how much, where, how routed: `serviceId`, `replicaCount`, `scaler.*`, `modelRoute.*`, scheduling |
| **plan editor** | the escape hatch, applied last — any key, exempt from ownership, labelled `edit` |

## The pipeline

```
catalog entry ─┐
site profile  ─┼─► compose ─► Plan ─► diff ─► apply | install ─► helm
form / flags  ─┘             (hash)     ↓
                                   revision lock
```

Both frontends produce and consume the same `Plan`. That, rather than shared code,
is what makes two of them worth more than one.

```
swiss plan    --model qwen3.6-35b-a3b --release glm-53 -o plan.json
swiss diff    --plan plan.json     # exit 2 when something would change
swiss apply   --plan plan.json     # release MUST exist
swiss install --plan plan.json     # release must NOT exist
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

**GPU products are the catalog's call, unless forced.** `gpuProducts` on
`POST /api/plans` becomes a required node affinity under the variant's vendor
label. Which products are accepted:

| variant `requires.gpuProduct` | `forceGpuProducts` | listed product | unlisted product |
| --- | --- | --- | --- |
| empty (any of its vendor's) | either | accepted | accepted |
| non-empty | `false` (default) | accepted | refused, 400 |
| non-empty | `true` | accepted | accepted if on the cluster under the variant's GPU resource, with a plan `warnings` entry; else refused, 400 (502 if nodes are unreadable) |

- Listed products are never checked against the cluster, forced or not: forcing only widens.
- A forced product keeps the variant as published -- GPU count, image, engine arguments. Nothing is retuned for the card.
- Another vendor's product cannot be forced: the resource, label and image are the variant's vendor's.
- `warnings` sits in the plan, beside the release, outside the hash.
- An upgrade re-checks only `gpuProducts` it is sent; affinity carried forward in the form layer is not re-checked.

**`apply` and `install` are separate verbs, deliberately.** Helm's `upgrade
--install` is forgiving, and that forgiveness is the hazard: a name or namespace
that does not match what is live installs a *second* release — two engines on one
set of GPUs. Creation is a word you have to type on purpose.

**Helm owns the cluster.** Three-way merge is not Swiss's to reimplement. Both
paths materialise a plan into a temp directory — `values.yaml` plus a one-release
`helmfile.yaml` — run helmfile against it, then discard it, so there is one
executor rather than a repo path and a server path that can disagree.

**The plan is written into the cluster before the apply**, as a
`swiss-plan-<release>` ConfigMap carrying the plan and a status key. That is the
invariant the whole design rests on:

> Every applied deploy must be reconstructible from the cluster alone.

So desired state is the cluster, and swissd's database is only an audit log of what
was attempted. Losing it costs the history and nothing else.

**Apply does not wait.** `wait: false`, `atomic: false`, `cleanupOnFail: false` —
on these workloads "failed" usually means "still loading", and auto-rolling-back a
40-minute weight load is the expensive wrong answer. Apply returns once the upgrade
is accepted, and watching is separate.

**Health is checked in three widening steps**, because each one passes on a model
the next still fails:

| check | proves |
| --- | --- |
| pods ready | the scheduler placed it and the probes pass |
| `GET /<route>/v1/models` through openresty | the route is published and the engine answers |
| `POST /<route>/v1/chat\|completions\|messages` | the model can actually generate a token |

The last is manual and costs a few tokens of GPU time. It is the only one that
catches half-loaded weights or a missing tensor-parallel peer, both of which still
return a model list.

**Uninstall names a release, not a plan.** It is the one verb that does not go
through a materialised workspace: removing a release needs no values, and
requiring a readable plan would leave the untracked row — the one most likely to
need cleaning up — impossible to remove. It runs `helm uninstall`, then deletes
the plan ConfigMap, then writes an audit row.

That order is the write-ahead in reverse. An apply records the plan *before*
touching the cluster so a failure cannot leave a live release with nothing beside
it; an uninstall removes the release *first* for the same reason. Dropping the
plan up front and then failing would turn a release swiss deployed into an
`untracked` row — the thing that is supposed to mean somebody installed by hand.

## The catalog

A published static site, fetched over HTTPS or read from a directory. There is no
git protocol and no cloning — it would buy nothing a static file server does not.

```
swiss-catalog/
  index.json                          generated, committed, the published surface
  schema/{metadata,version}.schema.json
  models/<name>/metadata.yaml          what the model is, shared by every version
  models/<name>/<name>-<version>.yaml  how it is served
  hack/{build-index,validate,serve}.sh
```

**`index.json` is the listing; an entry is fetched only when a model is opened.** A
listing must not cost one request per model, so the index carries summaries that
deliberately omit variant `values` — nothing can compose from a summary and render
a model with half its flags missing.

**Versions are published like packages and pinned by digest.** Each
`models/<name>/<name>-<version>.yaml` is immutable — a fix is a new version — and a deploy
pins one. The plan records the version *and* a sha256 of the entry file:

```yaml
source:
  model: qwen3.6-35b-a3b
  version: "1.0.0"
  digest: sha256:2ddfe906daeb...
  variant: sglang-tp2
```

`index.json` carries that digest per version and a fetch whose bytes do not match
is refused. The catalog reference itself is a digest of the index bytes, not a git
SHA: over plain HTTP no SHA is available, and hashing what was actually read lets
two consumers agree without coordinating.

**An entry is a matrix, not a deploy.** One model may be servable several ways, and
the fields that distinguish them move together:

```yaml
apiVersion: catalog.swiss/v1
name: qwen3.6-35b-a3b
servedName: qwen
source:
  hf: Qwen/Qwen3.6-35B-A3B    # identity, not a path
  requiredGlobs: [config.json, "*.safetensors"]
variants:
  - id: sglang-tp2
    default: true
    engine: sglang
    chart: { name: sglang, version: "0.8.0" }
    image: { repository: ghcr.io/modelsphere/sglang, tag: v0.5.15-cu129 }
    requires: { gpus: 2, topology: single-node }
    values:
      extraArgs: [--tp-size=2, --mamba-radix-cache-strategy=extra_buffer, ...]
      startupProbe: { periodSeconds: 15, failureThreshold: 80 }
```

## LLMService backend

A release is stored either as a helm release swiss drives itself, or as an LLMService the operator drives. Handlers call one backend and stop there. `internal/llmsvc` is the mapping and the client.

`server.applyWith` picks the backend for a **new** install. A release already in the cluster stays on the backend that owns it.

| `server.applyWith` | CRD served | new install |
| --- | --- | --- |
| `helm` (default) | either | helm |
| `llmsvc` | yes | llmsvc |
| `llmsvc` | no | helm, and `/api/cluster` carries a warning |

Ownership is read off the cluster. An unserved CRD makes an LLMService invisible.

| LLMService `<ns>/<release>` | `swiss-plan-<release>` ConfigMap | CRD served | backend |
| --- | --- | --- | --- |
| yes | no | yes | llmsvc |
| yes | yes | yes | llmsvc, clean-up unfinished |
| no | yes | either | helm |
| yes | either | no | helm if the ConfigMap exists, otherwise not managed |
| no | no | either | not managed (untracked, as today) |

| | helm | llmsvc |
| --- | --- | --- |
| install | helmfile. An existing release, tracked or untracked, is refused | `exec.Lookup` + `exec.Check(Install)` first, so an existing helm release is still refused. Then create the LLMService. `AlreadyExists` is the same 409 |
| upgrade | helmfile. Missing or pending is refused | get, then update with the fetched `resourceVersion`. `expectRevision` is checked against `status.helm.revision`. Phase `Applying` is refused like a pending helm release. `Conflict` is 409 "the release moved under you; diff again" |
| result | helmfile output, then `status.yaml` | wait, inside the same 15 minute budget, for `observedGeneration >= generation` and phase `Applied` or `Failed`. The body carries the helm revision and a one-line summary. `Failed` is the operator's `status.message` |
| uninstall | `helm uninstall`, then delete the plan ConfigMap and archive Secrets | delete, then wait until the object is gone. The operator finalizer uninstalls helm |
| rollback | re-apply the archived plan, action `rollback` | the same flow through this backend |
| revisions | archive Secrets, plus the live helm revision | `status.history` joined with ControllerRevisions, then any legacy `swiss.plan.v1.<release>.v*` Secrets, listed after that history |
| status | `status.yaml` | `LLMService.status` (phase, message, helm revision, conditions) |
| force-conflicts | helm flag, recorded on `status.yaml` | annotation `serving.modelsphere.dev/force-conflicts` set to the generation the write produces |

A `chartPath` plan is refused with 400 when the llmsvc backend would run it. The operator has no filesystem path. An LLMService with no `swiss.modelsphere.dev/` annotation is external: status, probe and chat still read it, and upgrade is refused.

An upgrade keeps every annotation outside `swiss.modelsphere.dev/`, replaces that prefix from the plan (a stale note is dropped), and removes `serving.modelsphere.dev/force-conflicts` unless this apply sets it. The generation that annotation names comes from the spec. Annotation-only writes leave it where it is.

`/api/deployments` is the union of `swiss-plan-*` refs and LLMServices, sorted and paged as before. Each row has `backend`. External rows set `external`. Rows found under both set `cleanupPending` and say the clean-up is unfinished. With the CRD absent the list is the helm list plus `"backend": "helm"`.

`/api/cluster` adds:

```json
"llmservice": {"served": true, "applyWith": "llmsvc", "newInstalls": "llmsvc"}
```

`served` is API discovery. `newInstalls` is the backend a new install will actually use. A discovery failure is cached as not served, logged once per TTL, and warned on `/api/cluster`. `Forbidden` on get or list is cached the same way: the CRD still counts as served, LLMServices stay invisible, and the page warns `LLMService CRD is served but swissd has no llmservices grant; set rbac.applyWith`. With `server.applyWith: llmsvc`, either case installs with helm.

`POST /api/diff` stays helmfile for both backends and still returns the live helm revision and whether that release exists.

Deferred:

- Migration, including a migrate action, `swiss migrate`, and migrate-back.
- An operator dry-run for diff.
- Operator health. The cluster page reports discovery only. A written LLMService with no operator waits out the 15 minute apply budget.
- The CLI. `swiss install` and `swiss apply` stay on helm.

## RBAC

The chart's deploy switch is `config.allowDeploy`. It gates the write endpoints and the write grant together. `config.applyWith` is the new-install backend (`helm` or `llmsvc`), rendered as `server.applyWith`. `helm` is left out of the rendered config because it is already swissd's default. `rbac.applyWith` chooses the grant on the release Role, or the releases ClusterRole when `rbac.scope` is `cluster`.

| `config.allowDeploy` | `rbac.applyWith` | Release Role |
| --- | --- | --- |
| false | `helm` (default) | today's read grant |
| true | `helm` | today's deploy grant (`swiss.deployRules`) |
| false | `both` or `llmsvc` | today's read grant, plus `llmservices` get/list/watch and `controllerrevisions` get/list |
| true | `both` | today's deploy grant, plus `llmservices` get/list/watch/create/update/patch/delete and `controllerrevisions` get/list |
| true | `llmsvc` | the read grants (`secrets`, `configmaps` and `pods` get/list, and `swiss.objectReadRules`), `llmservices` CRUD, and `controllerrevisions` get/list. `swiss.deployRules` is not included |

The namespaces ClusterRole (`namespaces` get/create/patch) is still created only when `config.allowDeploy` is set, including `rbac.applyWith: llmsvc`.

| Refused | Why |
| --- | --- |
| `config.applyWith` other than `helm` or `llmsvc` | unknown backend |
| `rbac.applyWith` other than `helm`, `both` or `llmsvc` | unknown grant |
| `config.applyWith: llmsvc` with `rbac.applyWith: helm` | swissd would write LLMServices it cannot |
| `rbac.applyWith: llmsvc` with `config.applyWith: helm` | swissd would run helm without the deploy grant |
