# Notes for agents

## Comments

- Minimal. Only where the *why* cannot be read off the code.
- No narrative paragraphs above functions, no package essays, no restating the next line.
- Rationale belongs in `docs/swiss-design.md`, not in source. Code goes stale silently; the doc can be edited.

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
