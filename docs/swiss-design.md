# Swiss: a deploy control plane for the sglang and vllm charts

**Status:** 2026-09-20. The charts, `helmfile.yaml` and the `Makefile` targets are
unchanged by this document; Swiss feeds them rather than replacing them.

What exists: both binaries. `swiss` composes a model from the catalog, a site
profile and deploy-time overrides, then renders, diffs, applies or installs it.
`swissd` serves the same core over HTTP with a read-only SPA embedded in it, and
a write path behind `server.allowDeploy`. Not built: preflight, `emit`,
auth.

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
| **swiss-catalog** | published static files, data only | none | built |
| **core** | Go module, `internal/` | library | built |
| **`swiss`** | binary | CLI, stateless | built |
| **`swissd`** | binary | HTTP server | built |
| **web UI** | React + TypeScript, `embed.FS` into `swissd` | — | read-only |

The SPA is embedded in the server binary rather than served separately: one
artifact to deploy, no CORS, no second nginx to configure. Deployed things are
therefore the catalog repo, `swissd`, and the charts in harbor.

`swiss` and `swissd` come out of one Go module and share everything that matters.

The chart lives in `swiss/helm/swiss` rather than the parent repo's `charts/`, so
that directory is the whole of Swiss — Go, web, image and deployment together —
and splitting it into its own repo later is a move rather than a reassembly. It
is deliberately **not** in `helmfile.yaml`: that file is the model-release
inventory ("every sglang release running in production, and nothing else"), and
swissd is not a model. That also keeps swissd out of its own inventory view.

The image is four stages: the SPA is built with node, embedded into the Go
binary, helm/helmfile/helm-diff are fetched as pinned static binaries, and the
result runs on `distroless/static` as non-root with a read-only root filesystem.
No shell, no package manager, no libc.

## The catalog

A published static site: `index.json` plus the entry documents it points at,
served over HTTPS or read from a directory. There is no git protocol and no
cloning — making the consumer speak git would buy nothing a static file server
does not already give it.

```
swiss-catalog/
  index.json                 generated, committed, the published surface
  schema/entry.schema.json
  models/<name>/entry.yaml
  hack/{build-index,validate,serve}.sh
```

`index.json` is fetched once to render a listing; an entry is fetched only when a
model is opened. A listing must not cost one request per model, which is why
`IndexModel` is a separate type from `Entry` and carries no variant `values` —
nothing can compose from a summary and render a model with half its flags
missing.

### Models are versioned, and a pin is locked

A model publishes versions like a package: `models/<name>-<version>.yaml`, named
the way helm names a chart archive, each with its own variants, image and flags. A deploy pins one — `--model-version 1.0.0`,
or the latest when it does not say — and the plan records **the version and a
sha256 of the entry file**:

```yaml
source:
  model: modelforge
  version: "1.0.0"
  digest: sha256:2ddfe906daeb...
  variant: sglang-tp2
```

`index.json` carries that digest per version, and a fetch whose bytes do not
match is refused as a rewritten release rather than used. That is the lock: it
makes "pinned to 1.0.0" mean something in a repo anyone can push to, and it is
what lets a recompose prove it started from the same model config as last time.

A published version is therefore immutable — a fix is a new version, never an
edit — and the catalog's own CI refuses a commit that changes a published file.

**The catalog reference is a digest of the index bytes**, not a git SHA. Over
plain HTTP no SHA is available, and an ETag is the server's opinion rather than
the content's; hashing what was actually read gives every source the same kind of
reference, and two consumers that fetched the same bytes agree without
coordinating. That reference is recorded in every plan.

An index is remote input. Paths in it are checked before they are fetched, a
`count` that disagrees with the list is refused rather than guessed at, and an
entry whose name disagrees with the index it came from is an error — one of them
is stale, and composing from the wrong one deploys a model nobody chose.

`apiVersion: catalog.swiss/v1` is a version marker in a document, **not** a
Kubernetes CRD. Nothing is registered with an API server. The group-shaped name
is borrowed convention and is a known trip hazard in this repo, which contains
real CRDs in exactly that shape; see Open.

Because the repo is public, site-specific keys are not merely discouraged there,
they are unusable — a namespace, an `outputConfigMap` or a harbor URL in a public
entry is wrong for every reader including you. CI on the catalog rejects any key
owned by the site layer. That check is what keeps the layering below honest, and
it is about thirty lines.

### An entry is a matrix, not a deploy

A catalog entry is not one deploy's values file. One model may be servable in
several ways, and the fields that distinguish them — `extraArgs`, `requires.gpus`,
`lws.size`, the engine image — only ever move together.

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
2-GPU node independently will eventually be used to do exactly that, and the
failure arrives forty minutes later as an OOM in a log nobody is watching.

The production catalog proves the point. Two entries are the same Qwen family on
the same hardware, and their flags are not interchangeable:

```
glm5.1   v0.5.10.post1    --tp=2       --mamba-scheduler-strategy=…
modelforge         v0.5.15-cu129    --tp-size=2  --mamba-radix-cache-strategy=…
```

The flag was renamed between those images and argparse **exits** on an unknown
one, so a tag bump without a flag change is a CrashLoopBackOff that never loads
the model. Image and `extraArgs` belong to one object or the pairing is only a
convention.

**Three fields are projected, not copied**, because the schema refuses a second
spelling of each: `model.name` from `servedName`, `model.gpus` from
`requires.gpus`, `image.tag` from the variant's image. Two spellings of one fact
drift — the same line the charts take with `nvidia.com/gpu`.

`chart` names a name and a version, never a repository — the site maps that to
harbor. `image` is the canonical reference; the site rewrites it to a mirror.
Both follow from the catalog being public.

`source.hf` is an identity, not a location. The chart mounts weights from
`model.localPath` on the host, which is a fact about a cluster, so the catalog
cannot supply it.

### The catalog is validated at load, every time

It is fetched over a network from a repo this cluster does not control, so it is
untrusted input at the point of use; trusting it because some CI somewhere was
supposed to have run is hoping, not validating. The JSON schema in the catalog
repo is the authority on shape; the Go loader re-checks the subset a consumer
must not take on trust, plus the cross-variant rules a per-file schema cannot
see — notably `lws.size` against `requires.nodes`, which does not fail loudly
when it disagrees, it hangs the group at rendezvous waiting for a peer that was
never scheduled.

## Layers

Four inputs, disjoint key sets, strict precedence. Disjointness is the whole
design: if a key can be set by two layers, every future question about this system
becomes a question about precedence, and there is no good answer to any of them.

| layer | owns | examples |
| --- | --- | --- |
| chart defaults | everything not claimed below | `charts/*/values.yaml` |
| **catalog variant** | model identity, and how it parallelizes | `model.{name,mountPath,hostPathType,gpus}`, `extraArgs`, `lws.{enabled,size}`, `env`, `volumes`, `modelCheck.requiredGlobs`, `image` |
| **site profile** | what makes it work here | `cache.hostPath`, the registry rewrite for `image.repository`, `scaler.serverAddress`, `modelRoute.nginx.outputConfigMap`, `modelRoute.monitor.outputConfigMap`, `serviceMonitor.labels`, namespace, and the path template `model.localPath` defaults from |
| **plan editor** | the escape hatch, applied last | any key, exempt from ownership, labelled `edit` |
| **user form** | how much, where, how routed | **`serviceId`**, **`model.localPath`**, `replicaCount`, `scaler.{minReplicas,maxReplicas,scaleDown}`, `nodeSelector`, `affinity`, `tolerations`, `priorityClassName`, `schedulerName`, `podDisruptionBudget`, `modelRoute.*`, `cart.*`, `sloRequirement.extraSpec` |

Two of those are central enough to be named fields rather than `--set` keys.
`serviceId` is the identity `modelRoute`, `sloRequirement` and the scaler all key
off, and it is per release rather than per model or per cluster. It is the
**primary** field: the release name and the openresty route both follow it until
someone types their own. Three fields holding the same string, each free to drift,
is how a release ends up with a scaler watching one id and a route publishing
another.
`serviceMonitor` splits the same way `image` does: whether to scrape this release
is the deploy's call, which Prometheus picks it up is the site's, so
`serviceMonitor.enabled` is the form's and `serviceMonitor.labels` stays with the
site.

`model.localPath` has a site-wide default built from the path template, but
weights move and a deploy has to be able to say where they are -- a template with
no override just means the first irregular model cannot be deployed at all.

The site profile lives in this repo, alongside `deploys/`. Namespace, route name
and `serviceId` truth already lives here — `helmfile.yaml` documents at length how
it was reconstructed — and `make verify` already checks part of it. A second repo
would split that in half.

**Ownership is a table, not a convention.** `internal/values/ownership.go` maps
every values path to exactly one layer, longest prefix wins, and each layer is
checked before anything merges — so a rejected key never half-applies and the
error names every offending path at once.

### Site defaults and derived values

A short, enumerated set is filled in before the form is merged, so an explicit
override simply wins and is attributed to the form. `model.localPath` comes from
the site's path template this way.

### Features are opt-in, and their "off" is written down

`scaler`, `sloRequirement`, `cart` and `serviceMonitor` all default to **enabled**
in `charts/sglang`. A deploy that mentions none of them would silently get an
autoscaler, an SLO object, a router and a ServiceMonitor — and that set is a
chart default, so it can change under a release that never asked for any of it.

So compose writes every flag down, with per-feature defaults rather than one
blanket answer — `cart` on, `modelRoute`, `sloRequirement`, `scaler`,
`serviceMonitor` and `metricsMock` off. A section the form touched is one the
deploy wants, so it is switched on explicitly: filling in scaling numbers turns
the scaler on, naming a route turns routing on, and an explicit `enabled: false`
from the form still wins over both.

### The plan editor is the fifth input, and the only one exempt

Four layers with disjoint keys is right until a deploy needs a key no layer
claims, or one the catalog got wrong. Rather than let that pressure erode the
ownership table one exception at a time, there is one honest escape hatch: a YAML
box applied **after every layer**, exempt from the ownership check, and labelled
`edit` wherever the plan is shown.

It is kept apart from the form's overrides in the stored plan, so an upgrade
carries it forward or drops it deliberately rather than silently. The rule it
preserves: an override that bypasses the design is fine as long as nobody can
apply it without seeing that they did.

Scheduling lives behind an **Advanced** fold: priority class and scheduler as
plain fields, and one YAML box merged into the deploy layer for the structured
ones — `nodeSelector`, `tolerations`, `affinity`, `resources`, `strategy`. It is
parsed on the server rather than in the browser, so there is one parser and the
ownership check still decides what it may contain: a `extraArgs` typed there is
refused exactly as it would be from a flag. The placeholder shows the shape
rather than a blank box, since these are keys people copy from a values file.

The web sends all of them regardless — three toggles plus the scaler, derived
from whether any scaling input was filled. A flag left out of the request would
hand the decision back to the chart.

A derived value is also skipped when the section it belongs to is off.
`cache.maxSlotsPerNode` is only computed when the site enabled the cache,
because `cache` does not exist in every chart version and a values file carrying
a key the chart has never heard of is **rejected by its schema rather than
ignored** — which is the whole point of the schema being closed. `cache.maxSlotsPerNode` is the clear case: the chart's own comment gives the
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
cmd/swissd/       HTTP server
web/              the SPA, embedded into swissd
helm/swiss/       the chart that deploys swissd
internal/
  values/    values trees, helm merge semantics, the ownership table
  catalog/   index + entry fetch over https or a path; validates what it loads
  site/      site profile: path template, mirror, route ConfigMaps
  compose/   the four-layer merge -- the heart
  plan/      the Plan type
  config/    the one config document, shared by both binaries
  render/    helm template
  exec/      materialises a plan and runs helmfile against it
  cluster/   Probe + Writer: kubeconfig | in-cluster ServiceAccount | fake
  store/     sqlite: plans, deployments, audit log
  server/    swissd: read API, write API, SPA serving
```

### One config document, for both binaries

```yaml
catalog: https://models.example.com/swiss-catalog/
cluster:
  name: prod-b300
  profile:
    configMap: swiss/site-profile     # or file: ./profile.yaml -- exactly one
server:                               # parsed and ignored by the CLI
  addr: ":8080"
  allowDeploy: false
  peers: [{ name: dev, url: https://swiss.dev.internal }]
```

They are one tool: the CLI and the server compose the same plans against the same
catalog and the same cluster wiring, and two formats would eventually disagree.
Found via `--config`, `$SWISS_CONFIG`, `./swiss.yaml`,
`~/.config/swiss/swiss.yaml`.

Relative paths in it resolve against the **config file**, not the working
directory: it is discovered, so it may come from `~/.config` while the shell is
anywhere at all, and "next to this file" is also the only reading that survives
the file being moved or mounted elsewhere.

The profile source is named, never sniffed. `swiss/site-profile` is a plausible
relative path as well as a plausible ConfigMap reference, and guessing wrong
means composing against another cluster's wiring.

What makes two frontends worth more than one — rather than the same thing built
twice — is that both produce and consume the same serializable `Plan`:

```
swiss plan --model modelforge --release fallback-modelforge-01 \
           --service-id fallback-modelforge-01 -o plan.json
swiss diff   --plan plan.json     # exit 2 when something would change
swiss apply  --plan plan.json     # release must exist
swiss install --plan plan.json    # release must not exist
```

**A plan verifies its own hash on read**, so a hand-edited plan is an error
rather than a surprise in a cluster.

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
new plan, and diffs it. `apply` is the execution half — it is `helm upgrade`
underneath — and the recompose is `POST /api/plans {"fromRelease": "..."}`: the
release's stored plan supplies the model, the variant and every deploy input, so
only the catalog layer moves.

The deploy page is four tabs — **Plan**, **Diff**, **Apply/Install**, **Status**
— each gated on the one before it: no diff without a plan, no apply without a
diff, no status until something is deployed. The apply tab is a summary and a
button, not another form, so the thing being approved is the plan that was
already diffed.

Status polls pods and adds one check nothing else does: **ask the openresty
entrypoint, not the pod**. A ready pod behind a route that was never published
serves nobody, and closing that gap is what the whole ModelRoute path exists for.
The check calls `/<route>/v1/models` through the entrypoint Service and reports
the model ids it got back.

The web puts that behind one screen. It names what changes before anything else
— model version, chart version, entry digest, variant — then shows the site
values and the deploy settings being carried forward unchanged, then the diff,
and only then the approve button. Nothing is editable there: an upgrade that also
lets you retype the form is two changes landing as one, and the diff can no
longer tell you which of them did something. Choosing a different *variant* is still allowed from the
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
| `swiss-plan-<release>` | which catalog ref, model, variant, profile and form inputs produced them, and how the last apply ended |

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

**helmfile needs files, not a repository.** So both paths materialise a plan into
a temp directory — `values.yaml` plus a one-release `helmfile.yaml` — and run
helmfile against it, then discard it. The server needs no checkout, and there is
exactly one executor rather than a repo path and a server path that can disagree.

```yaml
helmDefaults:
    wait: false
    atomic: false
    cleanupOnFail: false
    createNamespace: true
    historyMax: 20
    diffArgs: [--three-way-merge]
releases:
    - { name: glm-53, namespace: modelforge, chart: oci://…/sglang, version: "0.8.0",
        values: [values.yaml] }
```

The release is applied through **one values document per layer**, in merge
order, rather than one flattened file: the declaration then shows the layering
instead of hiding it, and a reviewer can read the catalog's contribution without
separating it from the site's by eye.

`createNamespace` is **off** by default, unlike the repo's own `helmfile.yaml`.
Under helm v4 that flag applies the Namespace object server-side, so it needs
`patch` on namespaces even when the namespace already exists -- a cluster-scoped
privilege swissd has no other reason to hold, for namespaces an admin created
for it when granting the Roles. The site profile can turn it on.

Those `helmDefaults` are the point, and a test asserts them against the repo's
own `helmfile.yaml`: swissd must behave *identically* to `make apply`, not
approximately like it. Local chart paths are made absolute, because helmfile runs
with its working directory inside the temp dir.

`--helm-binary` is honoured for the reason `docs/deploy.md` gives: the helm here
is v4 while much of the diff ecosystem still assumes v3. The image pins helm,
helmfile and helm-diff, so that mismatch lives in one place.

`swiss diff` passes helm-diff's `--detailed-exitcode` through — **0 unchanged,
2 changed, 1 error** — which is what makes the exit criterion scriptable rather
than something a person eyeballs.

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

### The apply outlives the request

Preconditions run on the caller's context; everything from the write-ahead
onward does not. helmfile runs under `exec.CommandContext`, so while the apply
held the request's context, a browser navigating away sent SIGKILL to helm
mid-upgrade — and a half-applied upgrade leaves the release in
`pending-upgrade`, precisely the state the refusal above will not touch. A
client disconnect must not be able to wedge a release that only a hand
`helm rollback` can clear.

The bookkeeping after the apply is detached for the same reason. On a cancelled
context the audit row is dropped and `status.yaml` is stranded on `applying`,
which the reconciliation view reports as "an apply was started and never
completed" — about an apply that finished.

This is decoupling from the *client*, not making apply asynchronous. The handler
still waits for helmfile, which returns as soon as the upgrade is accepted
because `wait: false`. What it no longer does is treat a closed socket as a
reason to kill a cluster operation already in flight.

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
the **cluster** — the plan ConfigMap beside each release — and the database is
the operation log, nothing more.

That split is deliberate and it is a durability argument. etcd is replicated,
backed up and the thing an operator already restores after an incident; a sqlite
file on one ReadWriteOnce volume is none of those. So the fact that has to
survive — which plan a release is running — lives in etcd, and the fact nothing
else can reconstruct — what was attempted, by whom, and how it ended — lives in
sqlite. Two stores holding the same fact would disagree the first time an apply
failed between the two writes, and a reader would then have to pick. Nothing
picks, because nothing has two answers to choose from.

The write path is gated by `server.allowDeploy`, off by default. With it off
every mutating endpoint returns 403 saying so, and swissd needs no database and
no write RBAC at all. The chart refuses the half-configured case — `allowDeploy`
on with `rbac.allowDeploy` off would accept applies it has no permission to
perform, failing at the API server a long way from the cause.

**The plan is written before the apply, not after.** A permissions or quota
failure then costs nothing: the request is refused with "plan not recorded,
nothing applied" and the cluster is untouched. Writing it afterwards left a live
release with no plan beside it, which the reconciliation view reports as
`untracked` -- the row that is supposed to mean someone installed by hand.

The ConfigMap carries a second key, `status.yaml`, updated after the apply:

```yaml
phase: applied        # applying | applied | failed
action: install
revision: 4
startedAt: 2026-09-20T10:00:00Z
error: ""
```

So a release whose apply failed, or whose apply never returned, says so beside
itself rather than looking like a healthy deploy. The post-apply status write is
best effort -- the plan is already recorded by then and only the phase can go
stale.

| store | holds | answers |
| --- | --- | --- |
| **cluster** | `swiss-plan-<release>` per release; the site profile as a ConfigMap in swissd's namespace | what is running, and what produced it |
| **SQLite** | `run` (append-only audit), `plan` (immutable, keyed by hash) | what was attempted, and how it ended |
| **helm** | 20 revisions of real values, already | what the values were, without the layering |

There is **no `deployment` table**, and that is the point: a row saying "release
R is on plan H" is a second copy of something etcd already holds, written a few
milliseconds apart from it, with no way to tell which is right when they differ.
`plan` is immutable and keyed by hash, so it is a content-addressed staging area
for the compose → diff → apply handoff, not an inventory. History comes from
`run`: the applies for one release, newest first, each naming the plan hash it
ran and whether it worked. Rollback is applying an earlier hash from that log.

**Nothing in the server is keyed by cluster.** One swissd serves one cluster and
owns one database, so a cluster column would hold one value in every row and
invite queries that can never be answered — a swissd cannot reach another
cluster's releases, database or profile, and should not pretend it can. A release
is identified by namespace and name. `cluster.name` exists only as a label: it
appears in API responses and logs so the web knows which cluster it is looking
at.

Seeing several clusters is a **front-end** concern and nothing else. The switcher
reads `peers` and navigates to another origin; that instance answers for itself.
Any cross-cluster view is the browser talking to several swissds, never one
swissd talking to several clusters.

SQLite on a PVC, one replica — sqlite is a single writer and the volume is
ReadWriteOnce. The chart **refuses** `allowDeploy` without a volume, and refuses
more than one replica with it.

The volume exists for the **audit log**, and only for it. `run` is the only
record of what was attempted -- diffs that changed nothing, applies that failed,
who asked for what -- and none of it is cluster state, so nothing can rebuild it.

What the volume is *not* needed for is seeing or upgrading a release.
`currentPlan` reads the plan ConfigMap and nothing else, so a swissd with no
database at all still lists what is deployed, shows each release's plan, and
recomposes an upgrade from it. Losing the database costs the log and the
history; it costs nothing else. That is a property worth keeping deliberately,
and a test asserts it.

There is deliberately **no reconcile command**. Reconciliation here is a *view*,
not a loop: every live release joined against the plan beside it, computed per
request. Nothing needs repairing because nothing is cached -- the deploy path
reads the cluster every time, and the half a reconcile could not recover is the
half the volume is for.

The loop-shaped version of this — a CRD owning the rendered resources, with a
controller writing observed state back in an event loop — is a later
consideration and would replace the view, not extend it. Today's status
write-back is a single best-effort write after the apply. That is a deliberate
trade, and it carries one unresolved consequence: see Open, where `plan.yaml`
currently holds the attempted plan rather than the applied one.

Nothing here justifies Postgres until `swissd` runs more than one replica.

The site profile is a ConfigMap rather than a database row: it describes a
cluster, so it lives in the cluster it describes, and a lost database does not
take the route ConfigMap names and mirror config with it.

### RBAC is the decision to read before installing

helm stores each release in a **Secret**, and Kubernetes RBAC cannot filter a
secret read by label or name. So "read helm releases" and "read every credential
in those namespaces" are the same grant. There is no third option.

The chart defaults to `rbac.scope: namespaced` with an explicit namespace list,
and refuses to render with an empty one rather than installing something that can
see nothing. That list is rendered into `cluster.namespaces` in swissd's config
as well as into the Roles, from one value: a Role cannot authorise a cluster-wide
list, so a probe that reads `NamespaceAll` under namespaced RBAC is refused
outright rather than returning less. Empty means cluster-wide and needs the
ClusterRole. The cost of that default is real and stated where it is set: a
release in an unlisted namespace is *invisible*, not reported as untracked, which
partly defeats the reconciliation view. `rbac.scope: cluster` buys completeness
for cluster-wide secret reads.

Deploy permissions are a second switch (`rbac.allowDeploy`), granting exactly
what the sglang and vllm charts render and nothing else — core, apps, policy,
rbac, `monitoring.coreos.com`, `leaderworkerset.x-k8s.io`, `autoscaling.4pd.io`,
`inference.x-k8s.io`, `routing.gpucluster.io`. The widest rule in that set is
`roles`/`rolebindings`, needed by the cart subchart.

Kubernetes bounds that: a subject cannot create a Role granting permissions it
does not itself hold. So swissd's grant must be a superset of everything the
charts hand out -- which is why it also carries `coordination.k8s.io` leases and
`pods` watch/patch, the two rules CART's HA Role gives to CART. swissd never uses
them; it only passes them on. Adding a rule to a chart's own RBAC therefore means
adding it here too, or the install fails with "attempting to grant RBAC
permissions not currently held".

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

## Phases

- **P0 — compose and render.** Catalog, entry schema, fetch → compose → validate
  → render. **Done.**
- **P1 — `diff`.** Materialised workspace, helmfile, `--detailed-exitcode`.
  **Done.**
- **P2 — preflight** as its own command, so the rules can be trusted in isolation
  before they gate anything. **Not started** — the `Probe` interface and its fake
  exist; no rule is implemented.
- **P3 — `apply` / `install` / revision locking.** **Done** for the CLI and the
  server; `status` and `emit` are not built.
- **P4 — `swissd`.** Read API, write API behind `allowDeploy`, SQLite, plan
  ConfigMaps, embedded read-only SPA. **Done.** One cluster per instance, no
  cluster keying anywhere in it.
- **P5 — the write path in the UI**, with the approval gate.
- **P6** — auth.

### P0's exit criterion

Reproduce all three live releases from catalog plus site profile, and get an
**empty `helmfile diff`** against the existing `deploys/production/*.yaml`.

**Not yet done.** The catalog now mirrors those three values files and the
composed `model.localPath` matches each of them exactly, but no diff has been run
against a cluster.

That empty diff is the entire proof. It says the layering holds for the releases
that already exist, that nothing was quietly lost in the merge, and that adopting
Swiss changes no running workload. If a key will not fit the four layers, P0 is
where that is cheap to learn — before the form, the server or the database exist
to be rewritten around it.

`make render` and `make check` already produce a committed render per release, so
this check is mechanical rather than a judgement call.

## Prerequisite

Charts must be packaged and pushed to harbor — the "Moving off the local chart
path" section of `docs/deploy.md`. The site profile now names `chartRepo` by
default; `swiss` can still run against a local `chartPath`, and `swissd` cannot,
having no checkout to point at. Per that section, `helmfile diff` must still come
back empty after the switch, which is the check that what is in harbor is the
tree these values were tested against.

## Not built

- **Preflight.** The seven rules above; `internal/cluster` has the interface and
  a fake, and nothing else.
- **`emit`** — the git-path writer.
- **Rollback beyond the current plan.** Only the latest plan sits beside a
  release, so history lives in `run` and nowhere else — and a lost database
  therefore costs the ability to revert to anything but what helm itself kept.
- **Auth.** No identity anywhere, so the audit log records what happened but not
  who.
- **The write path in the UI.** The API has it; the SPA is read-only.
- **Image and chart digests.** Carried, never resolved.

## Open

- **`plan.yaml` holds the attempted plan, not the applied one.** The write-ahead
  replaces the whole ConfigMap before helmfile runs, so a failed upgrade leaves
  the key describing an attempt while the release keeps running the previous
  plan. Now that the cluster is the only source of truth, the reconciliation
  view has no second place to check: it reports the target version for a release
  still on the old one, and if the best-effort `status.yaml` write is also lost,
  it reports it as clean. The fix is a spec/status split inside the same
  ConfigMap — write-ahead to a `pending.yaml`, promote it to `plan.yaml` on
  success — which is also the shape a CRD would take.
- **`servedName` is doing a job it should not.** The fallback release serves a
  Qwen model under the name `kimi` so callers do not change — a deploy-time
  routing decision, but `model.name` is catalog-owned, so today the catalog has
  to assert that a Qwen model is called "kimi". Wants either a form-layer
  override or an explicit alias concept.
- **`apiVersion: catalog.swiss/v1` reads as a CRD group.** It is a document
  version marker and nothing is registered with an API server, but this repo
  contains real CRDs in exactly that shape (`routing.gpucluster.io/v1alpha1`,
  `autoscaling.4pd.io/v1alpha1`), so the ambiguity is likelier here than
  elsewhere. `schemaVersion: 1` would be unmistakable; renaming touches the
  schema, the entries, `index.json` and two Go constants.
- **A plan can still target a namespace swissd cannot reach.** `cluster.namespaces`
  now bounds what it reads, but compose does not check the target namespace
  against it: a plan for an ungranted namespace passes preconditions — which see
  no release there — and `install` proceeds until the API server refuses it.
  Wants a preflight rule.
- **Catalog trust.** Pinning by content digest and requiring a chart digest covers
  accidents. It does not cover a compromised catalog, and signing is not
  specified here.
- **Standard labels.** `docs/deploy.md` notes nothing rendered carries
  `helm.sh/chart`, so a live object cannot report which chart version produced it.
  Swiss's sidecar records this at emit time, which is not the same as reading it
  off the cluster.
- **Chart digests.** `chart.version` is a tag, and a tag in harbor can be
  re-pushed. Recording the resolved digest at apply time is what would make
  "what is actually running" unambiguous — the same worry `docs/deploy.md` has
  about proving the artifact in harbor matches the tree these values were tested
  against.
