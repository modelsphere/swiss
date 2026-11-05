# Adaptive concurrency plan composition

`POST /api/plans` accepts `adaptiveOnly: true` with `fromRelease`, `namespace` and `overrides.modelRoute.nginx.values`.

- Composition starts from the live LLMService plan, replaces only `adaptive_cc` / `adaptive_cc_*` form values, and pins source, chart, other layers and values without resolving catalogs again.
- This optional mode works without a Canary assignment or controller-owned baseline. Normal compose and apply operations have no Canary-specific restrictions.
- List/status supplies ownership and assignment metadata for Console, which controls experiment-service actions.
- An independently enabled operator admission webhook can enforce additional rules on Kubernetes writes.
