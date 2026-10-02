# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The version is the
chart's `appVersion`; the swissd image and the chart are released together
under it.

## [Unreleased]

### Added
- CI on every pull request: `gofmt`, `go vet` and `go test` against a pinned
  model catalog, plus a gate for the license text and committed credentials.
- Dependabot for Go modules, GitHub Actions, base images and the `web/console`
  submodule.
- `NOTICE` listing third-party components.

### Changed
- GitHub Actions pinned to commit SHAs.
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
