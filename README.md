# swiss

A deploy control plane for the sglang and vllm charts. Design and rationale:
[`docs/swiss-design.md`](docs/swiss-design.md).

```
cmd/swiss/     CLI          cmd/swissd/  HTTP server
web/           the SPA, embedded into swissd
helm/swiss/    the chart that deploys swissd
internal/      values catalog site compose plan config render exec cluster store server
examples/      a site profile and a swiss.yaml
```

## Configure

One document, read by both binaries. Found via `--config`, `$SWISS_CONFIG`,
`./swiss.yaml`, `~/.config/swiss/swiss.yaml`.

```yaml
catalog: https://models.example.com/swiss-catalog/
cluster:
  name: prod-b300
  profile:
    file: ./examples/site-prod.yaml   # or configMap: swiss/site-profile
server:
  addr: ":8080"
  allowDeploy: false
```

The CLI works without one (`--catalog` / `--profile`); swissd requires one.

## CLI

```sh
go build -o swiss ./cmd/swiss

swiss catalog list
swiss catalog show kimi-k2.5

swiss plan --model qwen3.6-35b-a3b --release fallback-modelforge-01 \
           --service-id fallback-modelforge-01 --explain
swiss plan   --model qwen3.6-35b-a3b --release fallback-modelforge-01 -o plan.json

swiss render  --plan plan.json     # helm template; values.schema.json runs here
swiss diff    --plan plan.json     # exit 2 when something would change
swiss apply   --plan plan.json     # release must exist
swiss install --plan plan.json     # release must not exist
```

`--explain` shows which layer set each value. `emit` is not implemented.

## Server

```sh
cd web && npm install && npm run build && cd ..
go build -o swissd ./cmd/swissd
./swissd --config examples/swiss.yaml
```

```
GET  /api/cluster /api/peers /api/catalog /api/catalog/{model}
GET  /api/releases /api/nodes /api/deployments /api/runs
POST /api/plans /api/diff /api/apply /api/install     (allowDeploy only)
```

`go build` works without node installed: `web/dist` is committed empty, and a
binary with no UI serves an explanatory 404 while the API keeps working.
`-web-dir` serves the SPA from disk instead.

## Deploy

`helm/swiss/Chart.yaml`'s `appVersion` is the only version. `hack/bump.sh` moves
it and the chart version together, and prints the build commands; `image.tag` is
empty so helm follows `appVersion`, and the binaries take it through ldflags.

```sh
./hack/bump.sh              # show
./hack/bump.sh patch        # or minor | major | 0.3.1, plus --tag

docker build -t harbor.4pd.io/hardcore-tech/swissd:$(./hack/bump.sh) \
  --build-arg VERSION=$(./hack/bump.sh) .

helm install swiss ./helm/swiss -n swiss --create-namespace \
  --set config.cluster.name=prod-b300 \
  --set 'rbac.namespaces={modelforge,kimi}'
```

`rbac.namespaces` is rendered into both the Roles and `cluster.namespaces`, so
swissd lists only namespaces it was granted — a Role cannot authorise a
cluster-wide list. `rbac.scope: cluster` drops the list and reads everything.

One instance per cluster, inside the cluster it manages. Read the RBAC section of
the design doc before installing: `rbac.scope` decides whether swissd can read
every Secret in the cluster.

## Test

```sh
go test ./...
cd web && npm run typecheck
helm lint ./helm/swiss --set config.cluster.name=x --set 'rbac.namespaces={y}'
```
