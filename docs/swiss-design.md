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

Three things are deployed: the catalog, `swissd`, and the charts in harbor.

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

`internal/values/ownership.go` maps every values path to exactly one layer and
checks each layer before anything merges, so a rejected key never half-applies.
Unmatched paths default to the form layer, so a new chart key becomes settable
with no code change.

Two consequences worth stating outright:

- **The catalog may not own site keys.** It is a public repo, so a namespace or a
  harbor URL in an entry is wrong for every reader. Its CI enforces this, and that
  check is what keeps the layering honest.
- **Feature flags are always written down.** `scaler`, `sloRequirement`, `cart` and
  `serviceMonitor` default to *enabled* in the charts, so compose emits every flag
  explicitly rather than letting a chart default decide for a release that never
  asked.

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
swiss plan    --model modelforge --release glm-53 -o plan.json
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
  model: modelforge
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
name: modelforge
servedName: kimi
source:
  hf: modelforge/Qwen3.6-35B-A3B-793303    # identity, not a path
  requiredGlobs: [config.json, "*.safetensors"]
variants:
  - id: sglang-tp2
    default: true
    engine: sglang
    chart: { name: sglang, version: "0.8.0" }
    image: { repository: harbor.4pd.io/hardcore-tech/sglang, tag: v0.5.15-cu129 }
    requires: { gpus: 2, topology: single-node }
    values:
      extraArgs: [--tp-size=2, --mamba-radix-cache-strategy=extra_buffer, ...]
      startupProbe: { periodSeconds: 15, failureThreshold: 80 }
```

Bundling them is not tidiness. A form that lets someone pick `--tp-size=8` and a
2-GPU node independently will be used to do exactly that, and it fails forty
minutes later as an OOM. The same applies to the image: engine flags are renamed
between releases and argparse *exits* on an unknown one, so a tag bump without a
flag change is a CrashLoopBackOff that never loads the model.

Three fields are projected rather than copied, because the chart schema refuses a
second spelling of each: `model.name` from `servedName`, `model.gpus` from
`requires.gpus`, `image.tag` from the variant's image.

The catalog is validated at load, every time — it is fetched from a repo this
cluster does not control. `apiVersion: catalog.swiss/v1` is a version marker in a
document, **not** a Kubernetes CRD; nothing is registered with an API server.
