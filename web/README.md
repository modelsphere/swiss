# swiss web

React 19 + TypeScript 7 + Vite 8, shadcn-style components (Radix idiom,
Tailwind v4), TanStack Query, react-router 8. Embedded into `swissd` via
`embed.FS`.

Shows deployments, the catalog and a model's variants, and deploys: a variant
card leads to `/deploy/:name?variant=`, which composes a plan, diffs it and
applies or installs. Apply is only reachable from a diff, and editing the form
discards the diff. When `server.allowDeploy` is off the page says so instead.

```sh
npm install
npm run dev      # vite on :5173, proxying /api to swissd on :8080
npm run build    # -> dist/, which the Go build embeds
```

To iterate without rebuilding the binary:

```sh
swissd --config swissd.yaml -web-dir web/dist
```

## Routes

| route | shows |
| --- | --- |
| `/` | deployments, untracked first |
| `/catalog` | the catalog index |
| `/catalog/:name` | one model's entry, with a per-variant fit check |
| `/deploy/:name` | form -> plan -> diff -> apply/install |
| `/upgrade/:ns/:release` | pick a model version -> what moves -> diff -> approve |

## Notes

**Bundle size is binary size.** The SPA ships inside `swissd`, to every cluster,
pulled on every rollout. Current build is ~357 KB JS (~110 KB gzipped). The
Vite chunk warning is set to 400 KB so growth is noticed rather than discovered.
This is also why the YAML escape hatch, when it lands, should use CodeMirror
(~300 KB) and not Monaco (~2 MB).

**Components are copied in, not depended on.** `src/components/ui/` is ours to
edit. There is no component library version to upgrade and no theme to fight.

**TypeScript 7 removed `baseUrl`.** Path aliases now resolve relative to
`tsconfig.json`, so `paths` stands alone. `src/vite-env.d.ts` is also required:
TS 7 rejects a side-effect import of `./index.css` without `vite/client` types.

**No client-side compose.** Layering happens server-side only. If the UI ever
merges catalog, site and form values itself, it will drift from the CLI, and the
shared core stops being shared.

**Routing is decided by location, not by file extension.** Model names carry
dots (`glm-5.3`, `kimi-k2.5`), so `path.Ext("/catalog/glm-5.3")` is `".3"` --
an extension-based static-file test 404s every model page. Only `assets/` is
treated as static; see `internal/server/static.go`.
