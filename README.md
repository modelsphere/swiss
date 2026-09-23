# swiss

A deploy control plane for the sglang and vllm charts. Design and rationale:
[`docs/swiss-design.md`](docs/swiss-design.md).

## Configure

One document, read by both binaries. Found via `--config`, `$SWISS_CONFIG`,
`./swiss.yaml`, `~/.config/swiss/swiss.yaml`.

```yaml
catalog: https://models.example.com/swiss-catalog/
cluster:
  name: prod
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

swiss plan --model modelforge --release fallback-modelforge-01 \
           --service-id fallback-modelforge-01 --explain
swiss plan   --model modelforge --release fallback-modelforge-01 -o plan.json

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
POST /api/login /api/logout        GET /api/session      (open)
GET  /api/cluster /api/peers /api/catalog /api/catalog/{model}
GET  /api/releases /api/nodes /api/deployments /api/runs
PUT  /api/profile
POST /api/plans /api/diff /api/apply /api/install     (allowDeploy only)
```

## Deploy
```sh
docker build -t harbor.4pd.io/hardcore-tech/swissd:$(./hack/bump.sh) .

helm install swiss ./helm/swiss -n swiss --create-namespace \
  --set config.cluster.name=prod-b300 \
  --set 'rbac.namespaces={modelforge,kimi}'
```

`rbac.namespaces` is rendered into both the Roles and `cluster.namespaces`, so
swissd lists only namespaces it was granted — a Role cannot authorise a
cluster-wide list. `rbac.scope: cluster` drops the list and reads everything.

Deploy mode needs a volume and one replica: the database holds the audit log and
the rollback history, neither of which is cluster state. Upgrades do not depend
on it -- the whole plan is written beside every release, and swissd falls back to
reading it from there.

One instance per cluster, inside the cluster it manages. Read the RBAC section of
the design doc before installing: `rbac.scope` decides whether swissd can read
every Secret in the cluster.
