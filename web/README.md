# swiss web

React 19 + TypeScript 7 + Vite 8, shadcn-style components (Radix idiom,
Tailwind v4), TanStack Query, react-router 8. Embedded into `swissd` via
`embed.FS`.

**Read-only.** It shows exactly what the API serves today: deployments, the
catalog, and a model's variants. There is no deploy form, no diff view and no
plan screen, because those endpoints do not exist yet.

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

## Notes

**Bundle size is binary size.** The SPA ships inside `swissd`, to every cluster,
pulled on every rollout. Current build is ~336 KB JS (~105 KB gzipped). The
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
