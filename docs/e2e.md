# End-to-end test on kind

`make test-e2e` stands swissd up on a local kind cluster and drives a deploy through its API.

```
make test-e2e
  -> setup-test-e2e (kind, test/e2e/kind-config.yaml)
  -> go test -tags=e2e ./test/e2e
       BeforeSuite: build + kind load -> CRDs + llm-operator -> helm install swiss -> port-forward
       specs: health -> login -> profile -> catalog -> plan -> install -> ready -> proxy -> uninstall
                                                                 (helm, then llmsvc)
  -> cleanup-test-e2e
```

A failing `go test` stops the Make recipe before cleanup, so the cluster stays up for inspection. `KEEP_CLUSTER=1` leaves it up after a passing run too. The suite still exits non-zero when a spec fails. On failure the test output includes pods, recent events, LLMServices, swissd and llm-operator logs, and the model pods' descriptions and logs.

## Run

Prerequisites: Go, docker, kind, kubectl, helm, and outbound HTTPS (the catalog, the chart repo, ghcr.io, Docker Hub and raw.githubusercontent.com are fetched at run time). No GPU. The image build needs the `web/console` submodule.

```sh
git submodule update --init
make test-e2e
KEEP_CLUSTER=1 make test-e2e
SKIP_BUILD=true make test-e2e          # kind-load an image that is already built
make test-e2e E2E_ARGS='-ginkgo.label-filter=helm'
make test-e2e E2E_ARGS='-ginkgo.label-filter=autoscale'
make cleanup-test-e2e
```

`-ginkgo.label-filter=helm` runs the setup specs and the helm deploy. `-ginkgo.label-filter=llmsvc` runs setup and the llmsvc deploy. `-ginkgo.label-filter=autoscale` runs only the PD autoscaling spec, which logs in itself. An empty filter runs the deploys and the autoscaling spec. Setup specs carry both deploy labels; each deploy spec carries its own.

The node image and kube-proxy mode live in `test/e2e/kind-config.yaml`. `make setup-test-e2e KIND_NODE_IMAGE=...` passes `--image` and overrides the pin. A cluster that already exists is left as it is: delete it before a config change takes effect.

`SKIP_BUILD=true` (or `1`) skips `docker build` and still runs `kind load` on the local image.

## What is real and what is not

| component | in the e2e cluster |
| --- | --- |
| swissd | real: built from the checkout, loaded with `kind load`, installed from `helm/swiss` with `test/e2e/testdata/values.yaml` (SQLite on a local-path PVC, fixed login). Image and `rbac.namespaces` are set from `IMG` and `MODEL_NS` |
| site profile | real: `test/e2e/testdata/profile.yaml`, with catalog URL, chart repo and namespace filled from the same env as the helm install, saved through `PUT /api/profile` |
| model catalog | real: the public v2 index, `$CATALOG` |
| engine charts | real: `vllm` from the helm repo, fetched by swissd's helmfile |
| model server | mocked: the catalog's `mock-llm` entry, [mock-llmd](https://github.com/modelsphere/mock-llmd) on the vllm chart, CPU only |
| llm-operator | real: `dist/install.yaml` at `$LLM_OPERATOR_VERSION`. Serves LLMService and runs the LLMScaler controller |
| LLMScaler | CRD and controller real; no Prometheus, so it never has a metric to scale on |
| ServiceMonitor | CRD only, from prometheus-operator. No Prometheus |
| ModelRoute | CRD only, from the autoconfig chart. Routing is off in the profile |
| LLMSLORequirement | CRD only, from the llm-slo-decision-gen chart. No decision-gen |
| openresty | absent. swissd's `/probe` and `/chat` need it, so the suite asks the engine directly through the API server's service proxy |
| CART | off in the profile |
| registry mirror, Harbor | none: images come straight from ghcr.io and Docker Hub |
| GPUs | none: `mock-llm` requests no `nvidia.com/gpu` |
| DisaggregatedSet | CRD only (`test/e2e/testdata/disaggregatedset-crd.yaml`), applied by the autoscale spec. No LWS controller, so nothing reconciles a DisaggregatedSet |
| PD helm install | the autoscale spec installs mock-llm through helm with pd edits, which renders a DisaggregatedSet. No LWS controller |
| decision-gen | an in-cluster `nginx:1.27-alpine` stub returning one static `/decisions` document. Not the real decision server |

## The flow

| step | checks |
| --- | --- |
| `GET /healthz`, `GET /readyz` | swissd is up and reaches the API server |
| `POST /api/login` | the chart's auth Secret works; the token is used as a bearer from here on |
| `PUT /api/profile`, `GET /api/cluster` | the profile is accepted and written; LLMService discovery |
| `GET /api/catalog`, `GET /api/catalog/{model}` | the v2 index is read and `$MODEL` is in it |
| `POST /api/plans`, `POST /api/install` | compose and install with `applyWith` set, once per backend |
| `GET /api/releases/{ns}/{release}/status` | polled until every pod is ready |
| service proxy `/v1/models`, `/v1/chat/completions` | the mock is the thing answering |
| `GET /api/releases/{ns}/{release}/objects`, `GET /api/deployments` | the rendered objects are readable; the release is listed under the backend that installed it |
| `DELETE /api/releases/{ns}/{release}` | pods, the helm release and the LLMService are gone |

The autoscale spec applies the DisaggregatedSet CRD, runs an nginx stub for decision-gen, and points `scaler.serverAddress` and `scaler.autoscalePeriod` (2s) at that Service. It plans a PD release with `sloRequirement.enabled` in the form layer and installs it through helm; autoscaling follows from that plan. The check is the DisaggregatedSet going from 2 slices to 1 and the plan's autoscale layer showing `replicaCount` 1. The first loop wait can still be the process's initial 60s.

## Settings

| variable | default |
| --- | --- |
| `KIND_CLUSTER` | `swiss-test-e2e`. Kube context is `kind-$KIND_CLUSTER` |
| `KIND_NODE_IMAGE` | unset. The image is pinned in `test/e2e/kind-config.yaml` (`kindest/node:v1.36.4@sha256:099e…`, kind v0.33.0). Set to pass `kind create --image` |
| kube-proxy | `iptables`, in `test/e2e/kind-config.yaml`. Use `nftables` on a kernel without `xt_statistic` |
| `KEEP_CLUSTER` | unset. `1` leaves the cluster up after a passing run. A failing run leaves it up either way |
| `SKIP_BUILD` | unset. `true` or `1` skips the image build and still `kind load`s it |
| `IMG` | `swissd:e2e` |
| `IMAGE_REPO`, `IMAGE_TAG` | unset. When `IMAGE_REPO` is set it wins over `IMG`; `IMAGE_TAG` defaults to `e2e` |
| `CATALOG` | `https://modelsphere.github.io/model-catalog/` |
| `CHART_REPO` | `https://modelsphere.github.io/helm-charts`, for the engine chart and the CRD charts |
| `LLM_OPERATOR_VERSION` | `v0.5.0` |
| `AUTOCONFIG_CHART_VERSION` | `0.4.1` |
| `SLO_CHART_VERSION` | `0.3.5` |
| `PROMETHEUS_OPERATOR_VERSION` | `v0.94.1` |
| `MODEL` | `mock-llm` |
| `MODEL_NS` | `models`. Profile namespace and `rbac.namespaces` |
| `SWISS_NS` | `swiss` |
| `READY_TIMEOUT` | `300` seconds |
| `LOCAL_PORT` | `18080`, for the port-forward to swissd |
| `E2E_TIMEOUT` | `45m`, the `go test -timeout` |
| `E2E_ARGS` | empty. Extra `go test` arguments, such as `-ginkgo.label-filter=helm` |
| `KIND`, `KUBECTL`, `HELM`, `DOCKER` | the binaries on `PATH` |

## CI

`.github/workflows/e2e.yml` runs `make test-e2e` on every pull request and on push to `master`, and on `workflow_dispatch`. The `label_filter` input selects one backend on a manual run; on push and pull_request it is empty, so both backends run. It is not a required check.
