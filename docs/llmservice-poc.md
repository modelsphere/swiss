# LLMService POC: swiss applies through llm-operator, side by side with helm

swiss stops running helm for a release once it is migrated: it writes an
`LLMService`, and llm-operator reconciles that into the helm release. This
milestone ships both paths live in one swissd, and a swiss-side migration
between them. Nothing that works today stops working.

```
                         ┌─ helm backend ──────▶ helmfile apply      (today, unchanged)
swiss: compose ─▶ diff ─▶│
                         └─ llmsvc backend ────▶ write LLMService ─▶ llm-operator ─▶ helm SDK
```

## Scope

| In | Out (later milestones) |
|---|---|
| `LLMService` v1alpha1 CRD and controller in llm-operator | operator dry-run, so swiss can drop helm for diff |
| swiss: a backend per release, helm or llmsvc | drift correction (periodic re-apply) |
| swiss: migrate a release helm → llmsvc, and back | `spec.catalog` source (operator composes from a catalog) |
| swiss: views over both kinds of release | removing the helm backend, plan ConfigMap and archives |
| reduced-RBAC mode available, not default | GitOps conflict handling beyond "do not write" |

**Nothing breaks** means:

- a swissd without the CRD behaves exactly as today: the llmsvc backend is hidden;
- a release keeps its backend until someone migrates it;
- a migration that cannot adopt without changing the release aborts and leaves
  the release as it was;
- rollback reaches revisions from before a migration.

## The API

`serving.modelsphere.dev/v1alpha1`, kind `LLMService`, plural `llmservices`,
short name `llmsvc`, namespaced. One per helm release, in the release's
namespace; `metadata.name` is the release name, which is the serviceId.

```yaml
apiVersion: serving.modelsphere.dev/v1alpha1
kind: LLMService
metadata:
  name: qwen
  namespace: models
  annotations:                                # swiss provenance; absent when swiss did not write it
    swiss.modelsphere.dev/plan-hash: sha256:…
    swiss.modelsphere.dev/catalog: /models/index.json
    swiss.modelsphere.dev/catalog-name: public
    swiss.modelsphere.dev/catalog-ref: sha256:…
    swiss.modelsphere.dev/entry-digest: sha256:…
    swiss.modelsphere.dev/profile: prod
    swiss.modelsphere.dev/action: upgrade      # install | upgrade | rollback | migrate
    swiss.modelsphere.dev/note: "bump to 1.1.0"
spec:
  chart:
    name: sglang
    repo: oci://ghcr.io/modelsphere/charts    # https://… or oci://…
    version: ">=0.8.0"                        # exact or a range
    credentialsRef: {name: chart-pull}        # optional: a Secret in this namespace
  layers:                                     # merged in order, last writer wins
    - {name: catalog, values: {…}}
    - {name: site,    values: {…}}
    - {name: derived, values: {…}}
    - {name: form,    values: {…}}
    - {name: edit,    values: {…}}
  model:                                      # optional, descriptive
    {name: qwen3.6-35b-a3b, version: 1.1.0, variant: sglang-tp2-h100, hf: Qwen/Qwen3.6-35B-A3B, engine: sglang}
  suspend: false
status:
  observedGeneration: 7
  phase: Applied                              # Pending | Applying | Applied | Failed
  conditions: [Applied, Adopted]              # standard metav1.Condition
  message: ""                                 # helm's error on Failed
  chart: {name: sglang, version: 0.8.6}       # what spec.chart.version resolved to
  appliedHash: sha256:…                       # over status.chart + merged layers
  helm: {revision: 12, status: deployed}
  history:                                    # newest first, bounded (default 10)
    - {revision: 12, hash: sha256:…, appliedAt: …, controllerRevision: qwen-7c9d…}
```

Rules:

| Field | Rule |
|---|---|
| `spec.layers` | `x-kubernetes-list-type: map` keyed on `name`: names unique, ownership per layer under server-side apply. Order is the meaning, so one writer per resource. Empty is valid (chart defaults) |
| `spec.chart.version` | resolved once per spec change and pinned in `status.chart`; kept while the range allows it, so a new chart in the registry moves nothing (the same rule as `chart.Resolve`) |
| `spec.model` | descriptive: labels, printer columns, swiss's catalog-move check. Not used to render |
| `spec.suspend` | stop reconciling; status keeps the last outcome |
| annotations | swiss's only; the operator copies `swiss.modelsphere.dev/*` into each history snapshot and otherwise ignores them |

Two operator annotations are requests rather than state:

| Annotation | Meaning |
|---|---|
| `serving.modelsphere.dev/deletion-policy: Orphan` | on delete, remove the finalizer without uninstalling the helm release. Set during a migration until adoption succeeds |
| `serving.modelsphere.dev/force-conflicts: "<generation>"` | take fields other managers own on the apply for that generation only; recorded in `status.history`, ignored for any other generation |

## llm-operator

1. `kubebuilder edit --multigroup=true` and the moves in its `AGENTS.md`, then
   `kubebuilder create api --group serving --version v1alpha1 --kind LLMService`.
2. Controller, per reconcile:

```
suspend? ─yes─▶ done
   │no
deleting? ─yes─▶ Orphan? ─no─▶ helm uninstall ─▶ drop finalizer
   │no                 └yes──────────────────────▶ drop finalizer
add finalizer
resolve chart version (keep status.chart while in range)
hash = H(status.chart, merge(layers))
hash == status.appliedHash? ─yes─▶ done
   │no
release exists, never applied by us? ─yes─▶ adopt
   │no
helm install | upgrade ─▶ ControllerRevision{spec, swiss annotations} ─▶ status
```

3. **Adopt** (first reconcile of a release the operator did not install):
   chart name and version and the release's user-supplied values must equal
   `status.chart` and `merge(layers)`. Equal: record `appliedHash`, condition
   `Adopted=True`, history entry for the release's current revision, no helm
   call. Not equal: `phase: Failed`, `Adopted=False`, reason `Drift`, and no
   upgrade -- the migration aborts on this.
4. Helm through the SDK (`pkg/action`), release storage unchanged, so `helm
   list` and swiss's untracked view keep working. Chart pulls from https or
   `oci://`, anonymous or `credentialsRef`.
5. Range resolution: a copy of swiss `internal/chart` (Spec, Resolve, Versions)
   with its tests. Go cannot import swiss's `internal/`; moving it to an
   importable package is a later clean-up.
6. RBAC: llmservices and status and finalizers; controllerrevisions; what the
   sglang and vllm charts render (swiss's `allowDeploy` grant, copied); helm's
   release Secrets in managed namespaces.
7. Tests: envtest for reconcile, adopt, orphan delete, suspend, force-conflicts
   once; kind e2e installing the sglang chart with a CPU stand-in image.

## swiss

### A backend per release

Which backend owns a release is read off the cluster, never configured per
release:

| Found | Backend |
|---|---|
| `LLMService` `<ns>/<release>` | llmsvc |
| `swiss-plan-<release>` ConfigMap | helm |
| both | llmsvc (a migration that did not finish its clean-up; the view says so) |
| neither | not managed by swiss (untracked, as today) |

New installs use `server.applyWith` (`helm` default, or `llmsvc`). It falls
back to helm, with a warning on the cluster page, when the CRD is not served.
llmsvc needs `llmservices.serving.modelsphere.dev` in API discovery.

Both implement one interface, so the handlers stop knowing which runs:

```go
type backend interface {
	Current(ctx, ns, release) (*plan.Plan, error)                  // what is deployed, as a plan
	Apply(ctx, p *plan.Plan, mode exec.Mode, o applyOpts) (applyResult, error)
	Uninstall(ctx, ns, release) error
	Revisions(ctx, ns, release) ([]revision, error)
	Archived(ctx, ns, release, rev int) (*plan.Plan, error)
}
```

`helmBackend` is today's code moved behind it. `llmsvcBackend`:

| Operation | Today (helm) | llmsvc |
|---|---|---|
| install | helmfile, `exec.Check` refuses an existing release | create; AlreadyExists is the same refusal |
| upgrade | helmfile, refuses a missing or pending release | update with the `resourceVersion` read at diff time; Conflict = "moved under you" (`expectRevision`) |
| apply result | helmfile output, synchronously | write, then wait for `observedGeneration == generation` and a terminal phase (same 15m budget); result = phase, helm revision, message |
| uninstall | `helm uninstall` | delete, wait for the finalizer |
| rollback | re-apply an archived plan | write the archived spec + annotations back (action `rollback`) |
| revisions | plan Secret archives | `status.history` + ControllerRevisions, then legacy archives from before the migration |
| status phase | `status.yaml` in the ConfigMap | `LLMService.status` |
| force-conflicts | helm flag | the one-shot annotation |

Plan ↔ LLMService is one mapping, both directions, tested as a round trip
(`plan → LLMService → plan` keeps the plan hash):

| Plan | LLMService |
|---|---|
| release name, namespace | `metadata` |
| layers (catalog, site, derived, form, edit; empty ones left out) | `spec.layers`, in `plan.Layers` order |
| chart name, version, repo or path | `spec.chart` (a `chartPath` plan cannot use llmsvc: the operator has no path) |
| model, version, variant, hf, engine | `spec.model` |
| catalog, catalogName, ref, digest, profile, hash | annotations |
| createNamespace | swiss creates the namespace before the write |
| helmfile | not carried |

### Views

- Deployments list: the union of `swiss-plan-*` ConfigMaps and LLMServices, each
  row badged with its backend; LLMServices without swiss annotations are
  "external", with status, probe and chat, and no upgrade.
- Release detail, revisions, rollback, uninstall: through the release's backend.
- Cluster page: whether the CRD is served and the operator is running, and the
  new-install default.

### Migration

A per-release action, **Migrate to LLMService**, and `swiss migrate <ns>/<release>`
(`--all` for every helm-backend release). Refused unless the CRD is served,
the release is `deployed`, and no apply is in progress (`status.yaml` phase is
not `applying`).

```
read swiss-plan-<release> + its plan
  ─▶ create LLMService  (spec from the plan, swiss annotations,
                         action: migrate, deletion-policy: Orphan)
  ─▶ wait: Adopted=True ─────────────▶ remove deletion-policy
  │                                     ─▶ delete swiss-plan-<release>
  │                                     ─▶ keep swiss.plan.v1.<release>.v* archives (read-only)
  └▶ Adopted=False / timeout ────────▶ delete LLMService (orphaned: release untouched)
                                        ─▶ report the drift; ConfigMap was never touched
```

- No helm revision is created by a migration: adoption compares, it does not upgrade.
- The archives stay so rollback reaches pre-migration revisions; the llmsvc
  backend lists them after its own history.
- **Migrate back** (the escape hatch): set `deletion-policy: Orphan`, write
  `swiss-plan-<release>` from the current spec and annotations, delete the
  LLMService. The release, its revisions and its archives are untouched.

### RBAC (swiss chart)

| `rbac.allowDeploy` + backend | Grant |
|---|---|
| helm only (default) | today's grant, unchanged |
| helm + llmsvc | today's grant, plus llmservices CRUD and controllerrevisions get/list |
| llmsvc only (new `rbac.applyWith: llmsvc`) | llmservices CRUD, controllerrevisions get/list, namespaces create, and the read grants -- no Secret, Role or workload writes |

## Tests and done

| Where | What |
|---|---|
| swiss unit | mapping round trip; backend selection table; llmsvc apply/upgrade/conflict/rollback against a fake client |
| swiss server | every existing deploy, upgrade and rollback test passes unchanged on the helm backend; the same flows on llmsvc |
| swiss migration | adopt succeeds; drift aborts and leaves the ConfigMap; timeout aborts; migrate back restores; rollback across the boundary |
| llm-operator | envtest as above; kind e2e |
| end to end (kind) | install on helm, migrate, upgrade on llmsvc, roll back to a pre-migration revision, migrate back |

Done when the end-to-end run passes and a swissd without the CRD passes the
current suite untouched.

## Open

- Bounded history: 10 ControllerRevisions by default -- enough, or match helm's `--history-max`?
- Adoption equality: the release's user-supplied values against `merge(layers)`
  exactly, or after dropping keys equal to chart defaults (a hand-applied
  release may spell defaults out)?
- Operator health: swiss checks the operator Deployment's readiness, or a lease;
  a written LLMService with no operator waits forever otherwise.
