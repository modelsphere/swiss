# Development

How the pieces fit: [docs/architecture.md](docs/architecture.md).

## Prerequisites

- Go (version in `go.mod`), helm, node/npm for the UI
- For e2e: docker, kind, kubectl
- The UI source is a submodule: `git submodule update --init`

## Build

```sh
go build -o swiss ./cmd/swiss
go build -o swissd ./cmd/swissd
hack/web.sh                   # build the UI into web/dist, which swissd embeds
make docker-build             # swissd image, IMG=swissd:e2e by default
```

`web/dist` is committed empty, so `go build` works without the UI.

## Run locally

```sh
./swissd --config examples/swiss.yaml
./swiss catalog list
```

Configuration: [README.md](README.md#configure).

## Unit tests

```sh
make test                     # go test ./...
```

CI checks out [model-catalog](https://github.com/modelsphere/model-catalog) to `../swiss-catalog` (pinned by `CATALOG_REF` in `.github/workflows/ci.yml`); the server tests read it from there.

## e2e tests (kind)

```sh
make test-e2e                 # KEEP_CLUSTER=1 leaves the cluster up
make cleanup-test-e2e
```

Details and settings: [docs/e2e.md](docs/e2e.md).

## Lint

What CI runs:

```sh
gofmt -l cmd internal helm test web/embed.go   # must print nothing
make vet                                       # go vet, including -tags=e2e
helm lint --strict helm/swiss --set 'rbac.namespaces={ci}'
```

## Pull requests

1. Fork, branch from `master`.
2. Run the unit tests and lint above.
3. Open a PR against `modelsphere/swiss` `master`. CI (`ci.yml`) must pass; `e2e.yml` runs too but is not required.
