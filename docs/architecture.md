# Swiss architecture

Swiss is a deploy control plane for the sglang and vllm charts. It composes a
model release from a public catalog, a site profile and deploy-time settings,
then hands it to helm. It feeds the existing charts, `helmfile.yaml` and the
`Makefile` targets rather than replacing them.

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
helm repo (`https://modelsphere.github.io/helm-charts` by default). The catalog
is its own project: [model-catalog](https://github.com/modelsphere/model-catalog).

```mermaid
flowchart LR
  catalog[(swiss-catalog)] --> core
  profile[(site profile)] --> core
  cli[swiss CLI] --> core
  ui[web UI] --> swissd --> core
  core[core: compose, diff, apply] --> helm[helm / helmfile]
  core --> llmsvc[LLMService]
  helm --> charts[sglang / vllm charts]
```

## Layers

Composition is a few inputs with key sets and strict precedence. Later layer overrides.

| layer | owns |
| --- | --- |
| helm chart defaults | things defaults in sglang/vllm chart values.yaml |
| **site defaults** (`defaults`) | the site's preferences, config it in `defaults.features` in the profile, the `cart`, `serviceMonitor`, `sloRequirement` and `scaler` `enabled` flags, unset on |
| **catalog variant** | model deployment recipes define in [model-catalog](https://github.com/modelsphere/model-catalog) and how it parallelizes: `model.*`, `extraArgs`, `lws.*`, `env`, `image.repository` |
| **site profile** | what makes it work here: the registry mirror, cache paths, route ConfigMaps, namespace, the `model.localPath` template, the default scheduler and priority class |
| **derived** | swiss's rules, above the variant: a section the form filled in is switched on, `cache.maxSlotsPerNode`, `model.gpuResource` and the `requires.gpuProduct` node affinity (see [GPU placement](#gpu-placement)) |
| **user form** | how much, where, how routed: `serviceId`, `replicaCount`, `scaler.*`, `modelRoute.*`, scheduling |
| **plan editor** | the escape hatch, applied last — any key, exempt from ownership, labelled `edit` |

## GPU placement

swissd resolves placement in `compose` before `compose.Compose` writes the chart values. Inputs are the variant's `requires` (`gpus`, `vendor` — empty means nvidia — and `gpuProduct`), the request's `gpuProducts` and `forceGpuProducts`, and node inventory: `server.gpuProductLabels` over `DefaultGPUProductLabels`, then `nodes.gpuProductAliases`. The vendor default resource is `Requires.ResourceName()` from the `accelerators` table.

```mermaid
flowchart TD
  inputs["Inputs: requires, gpuProducts, forceGpuProducts, inventory"]
  chk{"Outside requires.gpuProduct, and the variant lists some?"}
  rej["400, or 502 when force cannot read nodes"]
  pick["Products: request gpuProducts, else requires.gpuProduct"]
  route{"Which resource lookup?"}
  cpu["Vendor default. No model.gpuResource"]
  look["Resolve one resource from inventory"]
  many["400: narrow to one resource"]
  vend["Vendor default"]
  wide{"Widened request on a different resource?"}
  bad["400: not the variant resource"]
  aff["Affinity: label keys of the resource, plus alias terms"]
  wr["model.gpus. model.gpuResource when gpus > 0 and not nvidia.com/gpu"]
  gate{"Derived model.gpuResource, and the chart is below the minimum?"}
  old["Refused: vllm >= 0.9.1, sglang >= 0.9.29"]
  plan["Plan"]
  inputs --> chk
  chk -->|no| pick
  chk -->|"yes, forced and on the cluster"| pick
  chk -->|"yes, otherwise"| rej
  pick --> route
  route -->|"gpus == 0, request products"| cpu
  route -->|"gpus == 0, no gpuProducts: vendor default"| wr
  route -->|"gpus > 0, no products: vendor default"| wr
  route -->|"gpus > 0, with products"| look
  look -->|one| wide
  look -->|several| many
  look -->|"none or unreadable"| vend
  cpu --> aff
  vend --> wide
  wide -->|yes| bad
  wide -->|no| aff
  aff --> wr
  wr --> gate
  gate -->|yes| old
  gate -->|no| plan
```

- Request `gpuProducts` when `gpus == 0` still get form-layer affinity on the vendor default's label keys. Request affinity is the form layer; catalog affinity is derived only while required `nodeSelectorTerms` is unset on the catalog and site layers.
- A node's resource is the first sorted label-map name with a positive allocatable, and its product is the first non-empty value of that resource's keys. An alias overrides that product when the node has a GPU resource; the selector with the most labels wins.
- Fallback warns when nodes are unreadable, or when a non-nvidia vendor has no recorded resource for the products. Nvidia with nothing recorded, and a deploy with no products, stay quiet.
- A forced product that is absent is 400, and unreadable nodes on that force check are 502. A widened request must match the resource nodes with `requires.gpuProduct` report, or the vendor default when none do; narrowing is unchecked.
- Terms are ORed, and labels inside an alias are ANDed. No terms is a 400 for request products and a warning that leaves catalog products unpinned.
- The chart gate reads only the derived `model.gpuResource`; an unknown chart name or an unparsable version skips it. The CLI has no inventory, so only emit-and-gate runs against the vendor default, and tolerations are not derived.

## Pipeline

```
catalog entry ─┐
site profile  ─┼─► compose ─► Plan ─► diff ─► apply | install ─► helm
form / flags  ─┘             (hash)     ↓
                                   revision lock
```

`applyWith` on the site profile picks the backend for a new install: `helm` (default) or `llmsvc` (LLMService). A release already in the cluster stays on the backend that owns it.

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
| `GET /api/autoscaling`, `GET /api/releases/{ns}/{release}/autoscaling` | the autoscale loop's last pass per PD release with the form SLO on; in memory only, lost on restart |
