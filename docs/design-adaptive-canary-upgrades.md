# Adaptive concurrency for assigned experiment arms

Existing-service experiment arms permit only adaptive concurrency changes during upgrade.

- `POST /api/plans` accepts `adaptiveOnly: true` with `fromRelease`, `namespace` and `overrides.modelRoute.nginx.values`. The values replace only `adaptive_cc` and `adaptive_cc_*` entries in the form layer. All source, chart, other layers and values remain pinned to the live plan; no catalog is resolved.
- Apply rechecks the live assignment and compares the proposed LLMService spec with adaptive form values removed. Install, rollback, scaling, SLO and uninstall retain their existing guards.
- Canary admission applies the same comparison and requires a controller-owned baseline before permitting changes. The controller captures it only after the original frozen digest matches, preserving existing experiments.
- Normalized evidence ignores adaptive route values and apply generation/hash/revision, retaining workload, identity, bindings, auth and all other configuration evidence. A tuning upgrade temporarily closes measurement windows while unapplied; it resumes the same experiment once ready.
- Console obtains fresh service status on upgrade, exposes only the adaptive controls for verified assigned arms, and blocks all other form changes.
