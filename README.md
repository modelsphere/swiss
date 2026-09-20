# swiss

Go backend for the deploy control plane described in
[`../docs/swiss-design.md`](../docs/swiss-design.md). **P0: compose and render.**
It writes nothing to a cluster and nothing to the charts repo.

```
cmd/swiss/            the CLI
internal/values/      values trees, helm merge semantics, the ownership table
internal/catalog/     swiss-catalog entries; validates what it loads
internal/site/        site profile: paths, mirror, route ConfigMaps
internal/compose/     the four-layer merge -- the heart
internal/plan/        the Plan, shared with the server that does not exist yet
internal/render/      Renderer interface + a helm exec implementation
internal/cluster/     Probe interface + a fake, for preflight
examples/             an example site profile
```

`swissd` is not here yet. Every package above is written so that adding it is a
new `cmd/`, not a refactor: **no compose, validate, render or apply logic lives
outside `internal/`.** If the server ever needs something the CLI cannot reach,
that thing is in the wrong package.

## Try it

```sh
go build -o swiss ./cmd/swiss
export SWISS_CATALOG=../swiss-catalog SWISS_PROFILE=examples/site-prod.yaml

./swiss catalog list
./swiss catalog show kimi-k2.5

# what each layer contributed, which is the answer to "why is this value here"
./swiss plan --model glm-5.3 --release glm-53 --set scaler.maxReplicas=6 --explain

# helm template against ../charts -- values.schema.json runs here
./swiss render --model glm-5.3 --release glm-53 | head

./swiss plan --model glm-5.3 --release glm-53 -o plan.json
./swiss render --plan plan.json
```

## What the design turns into, in code

**Ownership is a table, not a convention.** `internal/values/ownership.go` maps
every values path to exactly one layer, longest prefix wins. Each layer is
checked before anything merges, so a rejected key never half-applies.

Unmatched paths default to the **form** layer. That direction is deliberate: when
the charts grow a key, it becomes settable at deploy time with no change here.
Failing closed would block deploys on a key nobody has classified yet.

**Derived is a provenance label, not an owner.** `cache.maxSlotsPerNode` is
computed as ⌊node GPUs ÷ model GPUs⌋ — the chart's own rule — but `cache.*` is
owned by the site, so the site can still set it outright and the derived rule
steps aside. Ownership says who *may* write a path; provenance says who *did*.
(The first version of this conflated them, and `--explain` then showed a computed
value the site was forbidden to correct.)

**Three fields are projected, not copied.** `model.name` from `servedName`,
`model.gpus` from `requires.gpus`, `image.tag` from the variant's image. The
catalog schema refuses a second spelling of each, the same line the charts take
with `nvidia.com/gpu`.

**A plan verifies its own hash on read.** A hand-edited plan is an error, not a
surprise in a cluster.

**The catalog is validated at load, every time.** It is a public repo fetched
over a network; trusting it because some CI was supposed to have run is hoping,
not validating. `Entry.Validate` also carries the cross-variant rules a per-file
JSON schema cannot see — notably `lws.size` vs `requires.nodes`, which does not
fail loudly when it disagrees, it hangs the group at rendezvous.

## Not built

- `diff`, `emit`, `apply` — present as subcommands that fail loudly. A subcommand
  that silently does nothing is worse than one that is missing.
- **Preflight.** `internal/cluster` has the interface and a fake; no rule is
  implemented. The seven rules are listed in the design doc.
- **Catalog over git/HTTP.** `catalog.Load` reads a local checkout. `Catalog.Ref`
  reads `.git/HEAD` directly, so a plan composed from a tarball records no ref.
- **helm SDK.** `render.Exec` shells out. `helm.sh/helm/v4` is the intended home
  — v4 specifically, per the warning in `../docs/deploy.md`. The interface is
  there so that swap changes one file.
- **Image digests.** Carried through the catalog, never resolved.

## P0 exit criterion

Reproduce the three live releases from catalog plus profile and get an empty
`helmfile diff` against `../deploys/production/*.yaml`. Nothing here has been
checked against those yet — the catalog entries are illustrative, and the real
values files have site details these entries do not model.
