# Swiss: a deploy control plane for the sglang and vllm charts

**Status:** P0 built, 2026-09-20. The charts, `helmfile.yaml` and the `Makefile`
targets are unchanged by this document; Swiss feeds them rather than replacing
them.

What exists: the `swiss` CLI composes a model from the catalog, a site profile
and deploy-time overrides, and renders it through `helm template` against the
real charts. `catalog list|show`, `plan`, `plan --explain` and `render` work.
`diff`, `emit` and `apply` are subcommands that fail loudly. `swissd` does not
exist. See `../README.md`.

Today a release is created by hand: copy a values file from `deploys/production/`,
edit it, add an entry to `helmfile.yaml`, `make diff`, `make apply`. That works,
and the parts of it that are good — a committed desired state, a schema that
rejects typos, a diff before every apply — are worth keeping exactly as they are.
What it does not give you is a way for someone who does not know this repo to
deploy a model, or a way to publish "here is how this model should be served"
separately from "here is how our cluster is wired".

Swiss splits those two and puts a web form over the second.

| what | where |
| --- | --- |
| how a model should be served | the catalog, a public git repo |
| how this cluster is wired | a site profile |
| how much of it to run, and how it is routed | the web form, or `swiss` flags |
| what is actually deployed | `helmfile.yaml` + `deploys/`, unchanged |

The last two rows are the CLI path, where a git repo sits beside Swiss. The
server path has no repo and persists differently; both are set out under
[Two paths, and where state lives](#two-paths-and-where-state-lives).

## Components

Five pieces, three of them deployed.

| piece | artifact | runtime | state |
| --- | --- | --- | --- |
| **swiss-catalog** | public git repo, data only | none | built |
| **core** | Go module, `internal/` | library | built |
| **`swiss`** | binary | CLI, stateless | P0 built |
| **`swissd`** | binary | HTTP server | not started |
| **web UI** | React + TypeScript, `embed.FS` into `swissd` | — | not started |

The SPA is embedded in the server binary rather than served separately: one
artifact to deploy, no CORS, no second nginx to configure. Deployed things are
therefore the catalog repo, `swissd`, and the charts in harbor.

`swiss` and `swissd` come out of one Go module and share everything that matters.
Go because helm and helmfile are Go — the render path can use the helm SDK in
process and read `charts/*/values.schema.json` directly, instead of shelling out
and juggling chart tarballs.

## The catalog

A public git repo of model entries, pinned by commit SHA. Not a branch: a public
catalog that moves under you is a supply-chain surface, and the SHA is what makes
a deploy reproducible six months later.

```
swiss-catalog/
  index.json                 generated, the published surface
  schema/entry.schema.json
  models/glm-5.3/entry.yaml
```

Because the repo is public, site-specific keys are not merely discouraged there,
they are unusable — a namespace, an `outputConfigMap` or a harbor URL in a public
entry is wrong for every reader including you. CI on the catalog rejects any key
owned by the site layer. That check is what keeps the layering below honest, and
it is about thirty lines.

### An entry is a matrix, not a deploy

`model.yaml` at the repo root is one worked example of one model on one machine.
A catalog entry is not that shape. GLM-5.3 on 8×B300 at `--tp-size=8` and the same
weights on 2 GPUs at `--tp-size=2` are one model and two **variants**, and the
fields that distinguish them — `extraArgs`, `model.gpus`, `lws.size` — only ever
move together.

```yaml
apiVersion: catalog.swiss/v1
name: glm-5.3
displayName: GLM 5.3
source:
  hf: zai-org/GLM-5.3            # identity, not a path
  sizeGiB: 700
  requiredGlobs: [config.json]   # feeds modelCheck.requiredGlobs
variants:
  - id: sglang-tp8-b300
    engine: sglang
    chart: { name: sglang, version: "0.8.0" }
    image: { repository: lmsysorg/sglang, tag: v0.5.19 }
    requires:
      gpus: 8
      gpuProduct: [NVIDIA-B300-SXM6-AC]
      topology: single-node
    values:
      model: { mountPath: /model, hostPathType: Directory, gpus: "8" }
      extraArgs:
        - --tp-size=8
        - --mem-fraction-static=0.85
        - --reasoning-parser=glm45
      env: [...]
      volumes: [...]

  - id: sglang-pp2-lws-h100
    engine: sglang
    chart: { name: sglang, version: "0.8.0" }
    requires: { gpus: 8, gpuProduct: [NVIDIA-H100-80GB-HBM3], topology: lws, nodes: 2 }
    values:
      lws: { enabled: true, size: 2 }
      extraArgs: [--tp-size=8, ...]
```

Bundling them is not tidiness. A form that lets someone pick `--tp-size=8` and
a 2-GPU node independently will eventually be used to do exactly that, and the
failure arrives forty minutes later as an OOM in a log nobody is watching. The
variant is the unit precisely because its fields cannot be chosen separately.

`chart` names a name and a version, never a repository — the site maps that to
harbor. `image` is the canonical upstream reference; the site rewrites it to a
mirror. Both follow from the repo being public.

`source.hf` is an identity, not a location. The chart mounts weights from
`model.localPath` on the host, which is a fact about a cluster, so the catalog
cannot supply it: the site profile derives the path from `source.hf`, and
preflight checks it exists before anything is applied.

## Layers

Four inputs, disjoint key sets, strict precedence. Disjointness is the whole
design: if a key can be set by two layers, every future question about this system
becomes a question about precedence, and there is no good answer to any of them.

| layer | owns | examples |
| --- | --- | --- |
| chart defaults | everything not claimed below | `charts/*/values.yaml` |
| **catalog variant** | model identity, and how it parallelizes | `model.{name,mountPath,hostPathType,gpus}`, `extraArgs`, `lws.{enabled,size}`, `env`, `volumes`, `modelCheck.requiredGlobs`, `image` |
| **site profile** | what makes it work here | `model.localPath`, `cache.hostPath`, the registry rewrite for `image.repository`, `scaler.serverAddress`, `modelRoute.nginx.outputConfigMap`, `modelRoute.monitor.outputConfigMap`, namespace |
| **user form** | how much, where, how routed | `replicaCount`, `scaler.{minReplicas,maxReplicas,scaleDown}`, `nodeSelector`, `affinity`, `tolerations`, `priorityClassName`, `schedulerName`, `podDisruptionBudget`, `modelRoute.*`, `cart.*`, `sloRequirement.extraSpec` |

The site profile lives in this repo, alongside `deploys/`. Namespace, route name
and `serviceId` truth already lives here — `helmfile.yaml` documents at length how
it was reconstructed — and `make verify` already checks part of it. A second repo
would split that in half.

### Derived values

A short, enumerated set is computed from variant × site rather than being asked
for. `cache.maxSlotsPerNode` is the clear case: the chart's own comment gives the
rule as 1 for an 8-GPU model, 4 for a 2-GPU one, which is ⌊node GPUs ÷
`model.gpus`⌋ and needs no human.

Two constraints keep this from becoming magic: the rules are listed by name in
one file, and a derived value is always overridable and always visible in the
rendered diff. A value that appears in a cluster without appearing in a diff is
the thing this whole design is trying to avoid.

**Derived is a provenance label, not an owner.** Ownership says which layer *may*
write a path; provenance says which layer *did*. `cache.*` is owned by the site,
so the site can set `maxSlotsPerNode` outright and the derived rule steps aside —
it only fills a path its real owner left empty. The first implementation gave
`derived` its own row in the ownership table, which made the computed value
unsettable by the one layer that knows the node shapes it is guessing from; the
compose tests caught it.

Unmatched paths default to the **form** layer rather than to rejection. When the
charts grow a key it becomes settable at deploy time with no code change. Failing
closed would block deploys on a key nobody has classified yet.

### Engines

`charts/sglang` (v0.8.0, 40 top-level keys) and `charts/vllm` (v0.4.0, 37)
have identical key sets apart from `lws`, `cache` and `healthEndpointGeneration`,
all three sglang-only. This is one form with three conditional sections, driven
off the engine named in the variant — not two products, and not two code paths.

## The Plan

`swiss` and `swissd` are both thin. Everything real is in `internal/`, and the
rule enforcing that is: **no compose, validate, render or apply logic outside
`internal/`.** If the server needs something the CLI cannot reach, it is in the
wrong package.

```
cmd/swiss/        CLI
cmd/swissd/       HTTP server; adds auth, job history, PR creation, the web UI
internal/
  catalog/   fetch by commit SHA, verify, index
  compose/   four-layer merge, ownership check, derived rules
  validate/  the chart's own values.schema.json, plus preflight
  render/    helm SDK template, in process; diff
  plan/      the Plan type
  emit/      values file, helmfile release entry, provenance sidecar
  cluster/   ClusterProbe: kubeconfig | in-cluster ServiceAccount | fake
```

What makes two frontends worth more than one — rather than the same thing built
twice — is that both produce and consume the same serializable `Plan`:

```
swiss plan --model glm-5.3 --variant sglang-tp8-b300 \
           --profile prod --set scaler.maxReplicas=8  > plan.json
swiss diff  plan.json      # helmfile diff; touches nothing
swiss emit  plan.json      # writes deploys/prod/<release>.yaml + the helmfile entry
swiss apply plan.json
```

The web form builds the same document. CI consumes it. Either side can hand a
plan to the other, which is what makes the CLI useful in a pipeline instead of
being a debugging tool that rots.

### A plan records its layers separately

Not one flat `overrides` map. The three inputs are stored apart, alongside the
composed result:

```yaml
inputs:
  catalog: {extraArgs: [...], startupProbe: {...}}   # model-shaped
  site:    {model.localPath: ..., image.repository: ...}
  form:    {replicaCount: 2, scaler: {...}, modelRoute: {...}}
values:    {the composed document helm receives}
provenance: {extraArgs: catalog, model.localPath: site, ...}
```

Only `form` is unrecoverable — it is user intent. `catalog` re-derives from
`source.ref` plus `variant`, `site` from the profile name. All three are stored
anyway, so the artifact still reads correctly when the catalog is unreachable.

### Model config moves by version bump, not by edit

The split above is a mutability boundary, not just bookkeeping. Scheduling,
routing, scaling and replicas are the form's and can be edited freely. Engine
flags, probes and model paths belong to the catalog and are **read-only** to the
deploy side; they move only when the catalog reference moves:

```
swiss upgrade --release glm-53 --catalog-ref <new-ref>
```

which recomposes the new catalog layer against the same form layer, produces a
new plan, and diffs it. Choosing a different *variant* is still allowed from the
form — that is selecting among options the catalog published, not editing them.
The rule is **select, don't edit**, and it is already enforced: `CheckOwnership`
refuses a form write to `extraArgs` today.

Each deployment therefore records the catalog reference it is on, so a release
can be reported as "two catalog revisions behind" and the diff shows what the
model's author changed before anyone takes it.

`ClusterProbe` is the one abstraction worth taking seriously: the CLI supplies a
kubeconfig, the server an in-cluster ServiceAccount, tests a fake. Preflight then
runs offline in unit tests, which is the difference between preflight rules that
get written and preflight rules that get talked about.

## Preflight

Each of these corresponds to a failure this repo has already had or already
documents.

1. **Schema.** `values.schema.json`, the chart's own. The `failurThreshold` class
   of bug: a misspelling that helm ignores, the API server prunes, and a probe
   then kills a 40-minute cold load after 45 seconds.
2. **Release identity.** Name and namespace against `helm list -A`. `helmfile apply`
   is `helm upgrade --install`; a name or namespace that does not match what is
   live installs a *second* release, which here means two engines on one set of
   GPUs.
3. **Route collision.** `modelRoute.nginx.route` must be unique per
   `outputConfigMap` — two models writing one key into openresty's shared
   ConfigMap, the collision `docs/blue-green-migration.md` says the namespace
   split does not catch.
4. **`serviceId` uniqueness.** A serviceId matching more than one decision is an
   error to the scaler, not a choice.
5. **Fit.** The variant's `requires.gpus` and `requires.gpuProduct` against nodes
   that actually match the requested affinity.
6. **Weights.** `model.localPath` present on candidate nodes. `hostPathType:
   Directory` already refuses to start the pod, but preflight says so in a second
   rather than as a FailedMount event someone has to go find.
7. **Diff.** `helmfile diff` is shown before every apply. No path through the web
   skips it.

## Provenance

`values.schema.json` rejects unrecognised keys, which is exactly why it is
valuable — and it means Swiss cannot record where a values file came from inside
that file. Helm would reject the metadata. Provenance goes in a sidecar:

```
deploys/prod/modelforge-01-glm.yaml         plain values; schema-clean
deploys/prod/modelforge-01-glm.swiss.json   catalog repo@sha, model, variant,
                                            chart digest, plan hash
```

The values file stays a normal helm values file that `make diff` and a human
reader both understand, with nothing about Swiss in it.

The same document is also written **into the cluster** on every apply, as
`swiss-plan-<release>` in the release namespace, in YAML. Helm's own release
Secret already stores 20 revisions of the merged values (`historyMax`), but it
stores no layering — values go in, provenance is lost. The pair makes each
release self-describing:

| | answers |
| --- | --- |
| helm release Secret | what values is this release running, and what did it run before |
| `swiss-plan-<release>` | which catalog ref, model, variant, profile and form inputs produced them |

It is YAML because it is read next to helm values and chart templates, which are
all YAML. The plan's `hash` stays computed over canonical JSON, whose key order
is stable — hashing the YAML would make the hash depend on emitter formatting,
and a re-serialize would then look like a config change.

## Diff, apply and install

Swiss owns composition and preflight. **Helm owns the cluster.** Three-way merge
is not Swiss's to reimplement: `helmfile.yaml` already passes
`diffArgs: --three-way-merge` and `docs/deploy.md` already makes people install
the `helm-diff` plugin. A second implementation of "what will change" is a second
answer that can disagree with the first, which is the drift every comment in that
repo is written against.

So `swiss diff` shells to `helm diff upgrade --install ... --three-way-merge`,
and repo-mode apply shells to `helmfile`. Both honour a `--helm-binary` override,
for the reason `docs/deploy.md` gives: the helm here is v4 while much of the diff
ecosystem still assumes v3.

`swiss diff` passes helm-diff's `--detailed-exitcode` through — **0 unchanged,
2 changed, 1 error** — which is what makes the P0 exit criterion scriptable
rather than something a person eyeballs. It emits `-o json` from the first
version, because retrofitting structured output onto a command people already
parse with `grep` is worse than having it early.

### Two diffs, and conflating them is the trap

| | question | needs a cluster |
| --- | --- | --- |
| `swiss diff` | what changes if this is applied to the **live release** | yes |
| `swiss emit --check` | does the composed plan match **what is committed** | no |

The second is the P0 exit criterion and mirrors `make check`. Keep the verbs
apart; one command that sometimes means either is how people stop trusting it.

### `apply` and `install` are separate verbs, deliberately

```
swiss apply   --release glm-53     # release MUST exist; refuses to create
swiss install --release glm-53     # release must NOT exist; refuses to upgrade
```

Helm's `upgrade --install` is forgiving, and that forgiveness is the hazard
`helmfile.yaml` spends thirty lines on: a name or namespace that does not match
what is live installs a *second* release — two engines on one set of GPUs, two
ModelRoutes writing one openresty key. That is not a case for a friendlier
default. It is a case for making creation a word you have to type on purpose.
`swiss install --adopt` handles step-0 adoption and requires an empty diff first.

### Apply does not wait, because waiting is a lie here

`helmDefaults` sets `wait: false`, `atomic: false`, `cleanupOnFail: false`, and
the comments there explain why: "failed" usually means "still loading", and
auto-rolling-back a 40-minute load is the expensive wrong answer.

So apply returns as soon as the upgrade is accepted, and watching is a separate
command:

```
swiss status glm-53
  pods     2/2 scheduled, 0/2 ready
  startup  probe 48/80 (12m elapsed, ~20m expected)   <- loading, not failing
  route    modelforge-0.1 not yet in openresty-conf
```

That distinction — loading versus broken — is what the whole probe configuration
exists to encode, and `--wait` cannot express it.

Two refusals belong here: apply is refused while the release is in
`pending-upgrade` (common on these load times, and stepping on it is how a
release gets genuinely stuck), and preflight failures are hard errors with no
bypass flag. A bypass added before anyone has hit a false positive becomes the
thing people use instead of fixing the rule.

## Two paths, and where state lives

Swiss has a CLI path with a git repo beside it and a server path with none. They
persist differently, and one rule covers both:

> **Every applied deploy must be reconstructible from the cluster alone.**

The store of record may be git or a database. Neither may be the *only* place a
plan exists once it has been applied — which is what the `swiss-plan-<release>`
ConfigMap above guarantees.

### The git path (CLI)

Desired state is git; there is no database.

```
deploys/production/
  profile.yaml                the site profile for this cluster
  glm-53.yaml                 values, schema-clean
  glm-53.swiss.yaml           the plan
helmfile.yaml                 inventory; `swiss emit` edits it
rendered/                     committed render
```

`swiss emit` writes those and opens a PR; apply runs helmfile against the merged
commit. `make verify | diff | render | check` keep working unchanged, and a
second cluster is `deploys/<env>/profile.yaml` plus a helmfile environment rather
than a restructure.

Releases are keyed by **(cluster, namespace, release)** — never by model. One
model has many releases: blue/green, and a fallback tier. Anything keyed by model
name breaks on the first migration `docs/blue-green-migration.md` describes.

### The server path (`swissd`)

No repo beside it; management happens in the web UI. Desired state is therefore
the database, with the cluster as the backstop.

| store | holds |
| --- | --- |
| **SQLite** | `plan` (immutable, keyed by hash), `deployment` (release → current plan, live revision, version), `run` (append-only audit), `draft`, `catalog_cache` |
| **cluster** | `swiss-plan-<release>` per release; the site profile as a ConfigMap in swissd's namespace |
| **helm** | 20 revisions of real values, already |

`plan` immutable plus `deployment` pointing at one gives history and rollback for
free: reverting is applying an earlier plan row. Plans are never mutated in place.

SQLite on a PVC, one replica. Nothing here justifies Postgres until `swissd` runs
more than one replica, and the migration stays cheap precisely because a wipe
costs audit history and drafts rather than the inventory.

The site profile is a ConfigMap rather than a database row: it describes a
cluster, so it lives in the cluster it describes, and a lost database does not
take the route ConfigMap names and mirror config with it.

### What git was doing for free, and must be replaced

Two things, and the second is the one easily missed:

1. **Review** → an approval gate. `requireApproval: true` on a protected profile
   means apply needs a second person's sign-off on a specific plan hash. Plus the
   diff is not skippable: apply is only reachable from a rendered diff the
   operator has seen.
2. **Merge conflicts** → optimistic locking on the live helm revision. A diff
   records the revision it was computed against; apply asserts it has not moved.
   Without this, two operators diff the same release, both apply, and the second
   silently wins with no signal at all.

### Cluster-wide registries are read live, never cached as truth

The openresty ConfigMap (one key per route), the monitor ConfigMap and the
decision server's `serviceId` space are cluster-scoped name spaces. A table
listing "routes in use" is wrong the moment someone installs a release by hand —
which is the scenario `make verify` exists to catch. Preflight reads the live
ConfigMap keys every time. Cache them for a dropdown; never let the cache answer
"is this name free".

### Disaster recovery, written before it is needed

```
swissd reconcile --cluster prod
```

scans namespaces for helm releases and `swiss-plan-*` ConfigMaps and repopulates
`plan` and `deployment`. A database wipe then costs exactly: audit history,
drafts and approvals. A rebuild path that has never been run is not a rebuild
path.

Git remains available to the server as an optional **sink** — emitted values and
plans pushed for archival — but never as a dependency, and nothing reads back
from it.

## Phases

- **P0 — `swiss` only.** Catalog repo, entry schema, and fetch → compose →
  validate → render. No server, no database, no web. **Done.**
- **P1 — `swiss diff`.** Read-only, needs no repo write, and settles the exit
  criterion below. Highest value per line in the project, which is why it moved
  ahead of `emit`: the first thing to learn is whether catalog plus profile
  reproduces the live releases, before any code exists that writes files you
  would then have to redo.
- **P2 — preflight** as its own command, so the rules can be trusted in isolation
  before they gate anything.
- **P3 — the write path.** `emit`, `emit --check`, PR creation.
- **P4 — `apply`, `install`, `status`, rollback.**
- **P5 — `swissd`** around the same core: read-only web UI first, then the write
  path with the approval gate and revision locking.
- **P6** — multiple clusters and profiles, RBAC.

### P0's exit criterion

Reproduce all three live releases from catalog plus site profile, and get an
**empty `helmfile diff`** against the existing `deploys/production/*.yaml`.

That empty diff is the entire proof. It says the layering holds for the releases
that already exist, that nothing was quietly lost in the merge, and that adopting
Swiss changes no running workload. If a key will not fit the four layers, P0 is
where that is cheap to learn — before the form, the server or the database exist
to be rewritten around it.

`make render` and `make check` already produce a committed render per release, so
this check is mechanical rather than a judgement call.

## Prerequisite

Charts must be packaged and pushed to harbor before P1 — the "Moving off the
local chart path" section of `docs/deploy.md`. `swiss` can run against the local
chart path; `swissd` cannot. Per that section, `helmfile diff` must still come
back empty after the switch, which is the check that what is in harbor is the
tree these values were tested against.

## Open

- **Catalog trust.** Pinning by SHA and requiring a chart digest covers accidents.
  It does not cover a compromised catalog repo, and signing is not specified here.
- **Site profile format.** Assumed to be one YAML file per environment next to
  `deploys/`. Whether the derived-value rules live in it or in `internal/compose`
  is unsettled; the argument for `internal/` is that a rule like
  `maxSlotsPerNode` is a property of the chart, not of a cluster.
- **Standard labels.** `docs/deploy.md` notes nothing rendered carries
  `helm.sh/chart`, so a live object cannot report which chart version produced it.
  Swiss's sidecar records this at emit time, which is not the same as reading it
  off the cluster.
- **Per-model catalog versioning.** The catalog is pinned as a whole, by one
  commit. That means any change to any model moves every consumer's pin, so
  "upgrade this model's config" and "pick up an unrelated model's fix" cannot be
  separated. The intended direction is versioning at the model level the way
  Linux package management does it — `models/<name>/<version>.yaml` rather than
  one `entry.yaml` — which would also make `swiss upgrade` a per-model bump.
  Deferred, and it changes the entry layout, `index.json` and the plan's
  `source.ref`.
- **Chart digests.** `chart.version` is a tag, and a tag in harbor can be
  re-pushed. Recording the resolved digest at apply time is what would make
  "what is actually running" unambiguous — the same worry `docs/deploy.md` has
  about proving the artifact in harbor matches the tree these values were tested
  against.
