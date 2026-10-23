# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The version is the
chart's `appVersion`; the swissd image and the chart are released together
under it.

## [Unreleased]

### Added
- PD slice autoscaling in swissd. An `autoscale` plan layer, applied last, holds `replicaCount` for a disaggregated release. `GET/PUT/DELETE /api/releases/{namespace}/{release}/autoscaling` stores the bounds in ConfigMap `swiss-autoscale-<release>`. A loop asks decision-gen and upgrades the release when the slice count should change. Deleting that ConfigMap leaves the live layer in place until the next user upgrade; a rollback while autoscaling is enabled keeps the current slice count.
- CI on every pull request: `gofmt`, `go vet` and `go test` against a pinned
  model catalog, plus a gate for the license text and committed credentials.
- Dependabot for Go modules, GitHub Actions, base images and the `web/console`
  submodule.
- `NOTICE` listing third-party components.
- `forceGpuProducts` on `POST /api/plans`: deploy on a GPU product the
  variant's catalog entry does not list, as long as the cluster has it under
  the variant's GPU resource. The plan carries a `warnings` entry per forced
  product.

### Changed
- The LLMService backend requires the plan's `source.schema` to be v2. Empty means v1 (a v1 catalog, including one whose `schemaVersion` is unset, and every plan stored before v2) and is rejected with 400 before any cluster write. Rollback uses that stored schema, so a v2 plan still rolls back after its catalog is renamed, and a stored v1 plan stays refused when that name now points at a v2 repo. `swiss.modelsphere.dev/catalog-schema` records `v1` or `v2` on the LLMService. Helm still installs both.
- The backend for a new install is `applyWith` on the site profile (`helm` when omitted, or `llmsvc`), edited without a helm upgrade. `server.applyWith` and the chart's `config.applyWith` are removed. `rbac.applyWith` stays on the chart: the grant is install-time.
- GitHub Actions pinned to commit SHAs.
- `gpuProducts` is checked against the variant's `requires.gpuProduct`;
  an unlisted product is refused unless `forceGpuProducts` is set. Before, only
  the web form restricted it.
- deploy upgrading, relax upgrade restriction:
  - allow catalog repo switch, only to the same model: same HF repo (`source.hf`, now recorded in the plan) and same engine.
  - allow chart version upgrade in same catalog model version, model-catalog schema is upgraded with a more powerful semVer string

### Fixed
- The web UI is built from a console commit without the proprietary
  `@riseaicloud/ui` kit; images up to and including 0.6.1 embedded it.
- Server tests updated for the catalog's renamed `qwen3.6-35b-a3b` variant.
- `examples/site-prod.yaml` parses again (it still set `createNamespace`,
  which moved to the deploy request).

## [0.6.1] - 2026-10-02

First tagged release. Earlier development is in the git history.

[Unreleased]: https://github.com/modelsphere/swiss/compare/0.6.1...HEAD
[0.6.1]: https://github.com/modelsphere/swiss/releases/tag/0.6.1
