# swiss

Go backend for the deploy control plane described in
[`docs/swiss-design.md`](docs/swiss-design.md). **P0: compose and render.**
It writes nothing to a cluster and nothing to the charts repo.

```
cmd/swiss/            the CLI
cmd/swissd/           the server (read path)
web/                  the SPA, embedded into swissd
internal/values/      values trees, helm merge semantics, the ownership table
internal/catalog/     swiss-catalog entries; validates what it loads
internal/config/      the one config document, shared by both binaries
internal/site/        site profile: paths, mirror, route ConfigMaps
internal/compose/     the four-layer merge -- the heart
internal/plan/        the Plan, shared with the server that does not exist yet
internal/render/      Renderer interface + a helm exec implementation
internal/cluster/     Probe interface, a fake, and a client-go implementation
internal/server/      swissd: config, read API, SPA serving
examples/             an example site profile and swissd config
```

`swissd` serves the **read path** only: it browses the catalog, lists live
releases, and reports which of them Swiss knows the provenance of. It writes
nothing, to a cluster or anywhere else, and holds no database.

The rule that keeps the two frontends honest: **no compose, validate, render or
apply logic lives outside `internal/`.** If the server needs something the CLI
cannot reach, that thing is in the wrong package.

## Try it

One config document, read by **both** binaries — they compose the same plans
against the same catalog and cluster wiring, and two formats would eventually
disagree. `server:` is ignored by the CLI.

```yaml
# swiss.yaml
catalog: https://models.example.com/swiss-catalog/
cluster:
  name: prod-b300
  profile:
    file: ./deploys/production/profile.yaml   # or configMap: swiss/site-profile
server:
  addr: ":8080"
  peers:
    - { name: dev, url: https://swiss.dev.internal }
```

Found in order: `--config`, `$SWISS_CONFIG`, `./swiss.yaml`,
`~/.config/swiss/swiss.yaml`. Relative paths inside it resolve against the
**config file**, not the working directory — it may be discovered from
`~/.config` while your shell is anywhere. The CLI works without a config at all
(`--catalog` / `--profile`); swissd requires one.

```sh
go build -o swiss ./cmd/swiss

./swiss catalog list
./swiss catalog show kimi-k2.5

# what each layer contributed, which is the answer to "why is this value here"
./swiss plan --model glm-5.3 --release glm-53 --set scaler.maxReplicas=6 --explain

# helm template against ../charts -- values.schema.json runs here
./swiss render --model glm-5.3 --release glm-53 | head

./swiss plan --model glm-5.3 --release glm-53 -o plan.json
./swiss render --plan plan.json
```

A `configMap:` profile is refused by the CLI with a message saying why: only
swissd has a cluster probe to read it with.

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

## Deploying

```sh
docker build -t harbor.4pd.io/hardcore-tech/swissd:0.1.0 --build-arg VERSION=0.1.0 .

helm install swiss ../charts/swiss -n swiss --create-namespace \
  --set config.cluster.name=prod-b300 \
  --set 'rbac.namespaces={modelforge,kimi}'
```

The image is three stages: the SPA is built with node, embedded into the Go
binary, and the result runs on distroless/static as non-root with a read-only
root filesystem. No shell, no package manager, no libc.

`charts/swiss` deliberately does **not** go in `helmfile.yaml` -- that file is
the model-release inventory ("every sglang release running in production, and
nothing else"), and swissd is not a model. It also keeps swissd out of its own
inventory view.

**RBAC is the decision to read before installing.** helm stores releases in
Secrets and Kubernetes cannot filter a secret read by label, so "read helm
releases" and "read every credential in these namespaces" are one grant. The
chart defaults to `rbac.scope: namespaced` with an explicit namespace list, and
refuses to render with an empty one rather than installing something that can
see nothing. `rbac.scope: cluster` buys a complete untracked-release view at the
cost of cluster-wide secret reads; the chart says so where you set it.

## Running swissd locally

```sh
cd web && npm install && npm run build && cd ..
go build -o swissd ./cmd/swissd
./swissd --config examples/swiss.yaml
```

One instance per cluster, running inside the cluster it manages: in-cluster
ServiceAccount, its own site profile, its own view. The "one web across
clusters" is a switcher over `peers` in the config -- every instance serves it,
so any one of them is a valid entry point.

`go build` works without node installed: `web/dist` is committed empty, and a
binary with no UI in it serves an explanatory 404 while the API keeps working.

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
