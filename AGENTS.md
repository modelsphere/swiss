# Notes for agents

## Role

swiss is the backend server and cli client, changelog are made from that perspective, UI things left to console, not here.

## Before calling a change done

- **New feature:** what real problem does it solve that the current code can't? If you can't name one, don't add it.
- **Scope:** is it over-designed, or guarding edge cases nobody asked about? Prefer the simplest change, and let an existing layer or check handle the rest.
- **API:** is every API-level change (endpoint, request or response field, site profile key) synced in `docs/openapi.yaml`?
- **Compatibility:** could it break an older console or an older catalog? swissd runs against both across versions.

## Comments

- Minimal. Only where the *why* cannot be read off the code.
- No narrative paragraphs above functions, no package essays, no restating the next line.
- Rationale belongs in `docs/architecture.md`, not in source. Code goes stale silently; the doc can be edited.

## Formatting

- No 80-column limit. Wrap where it reads well; `gofmt` and `prettier` decide the rest.
- Go: `gofmt -w`. Web: `npm run typecheck` before claiming done.

## Docs and long comments

When something genuinely needs explaining, structure it:

- Lists over paragraphs.
- Tables for "this maps to that".
- A workflow graph for anything sequential:

```
compose -> diff -> apply -> status
             ^                 |
             +--- re-diff <----+   (live revision moved)
```

- One sentence of context, then the structure. Not three paragraphs building to it.

## Exceptions

- `../charts/*/values.yaml` keeps its heavy prose commentary — existing convention in that repo, do not strip it.
- `helm/swiss/values.yaml` follows the same convention: values files are read by people choosing values.
