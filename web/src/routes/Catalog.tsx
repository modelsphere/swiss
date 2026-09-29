import { Fragment, createContext, useContext, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, ChartColumn, ExternalLink, LayoutGrid, Search, Table2 } from "lucide-react";
import { api, type IndexVariant } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Empty, ErrorState, Loading } from "@/components/States";
import { WorkloadList } from "@/components/Uplift";
import { CatalogGate, CatalogSwitch, useCatalogChoice, withCatalog } from "@/components/CatalogChoice";
import {
  type Comparison,
  type Facets,
  type Hue,
  type Kind,
  type SortKey,
  type Summary,
  facetsOf,
  UPLIFT_HELP,
  formatUplift,
  headlineWorkload,
  httpLink,
  otherWorkloads,
  reportLink,
  sorters,
  summarize,
  tagHue,
  workloadSummary,
} from "@/lib/catalog";
import { gpuCount, gpuShort, gpuSupport, vendorLabel } from "@/lib/gpu";
import { cn } from "@/lib/utils";

// Laid out like the catalog's own GitHub Pages site (swiss-catalog's
// hack/build-site.js): a summary, filters kept in the URL, and a table or
// cards. This page adds what only a deploy controller has: Deploy.

type View = "table" | "cards";
type Update = (patch: Record<string, string | boolean>) => void;

interface Filters {
  q: string;
  family: string;
  engine: string;
  hardware: string;
  tag: string;
  tuned: boolean;
  hidedep: boolean;
  sort: SortKey;
  view: View;
}

// The view is remembered per browser; a view named in the URL wins, so a
// shared link opens the way it was sent.
const VIEW_KEY = "swiss.catalog.view";

function storedView(): View {
  try {
    return localStorage.getItem(VIEW_KEY) === "cards" ? "cards" : "table";
  } catch {
    return "table";
  }
}

function useFilters() {
  const [params, setParams] = useSearchParams();
  const get = (k: string) => params.get(k) ?? "";
  const sort = get("sort");
  const view = get("view");
  const filters: Filters = {
    q: get("q"),
    family: get("family"),
    engine: get("engine"),
    hardware: get("hardware"),
    tag: get("tag"),
    tuned: get("tuned") === "1",
    hidedep: get("hidedep") === "1",
    sort: sort === "uplift" || sort === "family" ? sort : "name",
    view: view === "cards" || view === "table" ? view : storedView(),
  };

  // Replace rather than push: a keystroke in the search box is not a page to
  // go back to.
  const set: Update = (patch) => {
    if (patch.view === "cards" || patch.view === "table") {
      try {
        localStorage.setItem(VIEW_KEY, patch.view);
      } catch {
        // Storage unavailable: the view still applies to this visit.
      }
    }
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        for (const [k, v] of Object.entries(patch)) {
          if (v === true) next.set(k, "1");
          else if (!v || (k === "sort" && v === "name") || (k === "view" && v === "table")) next.delete(k);
          else next.set(k, v);
        }
        return next;
      },
      { replace: true },
    );
  };

  // Clears the filters; the view and the catalog stay.
  const reset = () =>
    setParams(
      (prev) => {
        const next = new URLSearchParams();
        for (const k of ["view", "catalog"]) {
          const v = prev.get(k);
          if (v) next.set(k, v);
        }
        return next;
      },
      { replace: true },
    );

  return { filters, set, reset };
}

function matches(x: Facets, f: Filters): boolean {
  const terms = f.q.toLowerCase().split(/\s+/).filter(Boolean);
  return (
    terms.every((t) => x.text.includes(t)) &&
    (!f.family || x.model.family === f.family) &&
    (!f.engine || x.engines.includes(f.engine)) &&
    (!f.hardware || x.hardware.includes(f.hardware)) &&
    (!f.tag || (x.model.tags ?? []).includes(f.tag)) &&
    (!f.tuned || !!x.cmp) &&
    (!f.hidedep || !x.deprecated)
  );
}

// The catalog being browsed, for the links below it: a model page and a deploy
// have to open in the same catalog.
const CatalogName = createContext("");
// The site it publishes, where its perf reports are served.
const CatalogSite = createContext<string | undefined>(undefined);

export function Catalog() {
  const choice = useCatalogChoice();
  const { data, isPending, error } = useQuery({
    queryKey: ["catalog", choice.selected],
    queryFn: () => api.catalog(choice.selected),
    enabled: !!choice.selected,
  });
  const { filters: f, set, reset } = useFilters();
  const all = useMemo(() => data?.index.models.map(facetsOf) ?? [], [data]);

  if (choice.isPending) return <Loading what="catalogs" />;
  if (choice.error) return <ErrorState what="catalogs" error={choice.error} />;
  if (!choice.selected) {
    return <CatalogGate catalogs={choice.catalogs} named={choice.named} choose={choice.choose} what="browse" />;
  }
  if (isPending) return <Loading what={`catalog ${choice.selected}`} />;
  if (error) return <ErrorState what={`catalog ${choice.selected}`} error={error} />;

  const shown = all.filter((x) => matches(x, f)).sort(sorters[f.sort]);

  return (
    <CatalogName.Provider value={choice.selected}>
    <CatalogSite.Provider value={data.index.site}>
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h1 className="text-lg font-semibold">Catalog</h1>
        <CatalogSwitch catalogs={choice.catalogs} selected={choice.selected} choose={choice.choose} />
        <span className="text-sm text-muted-foreground">{data.source}</span>
        <code className="ml-auto text-xs text-muted-foreground">{data.ref.slice(0, 19)}</code>
      </div>

      {all.length === 0 ? (
        <Empty>This catalog lists no models.</Empty>
      ) : (
        <>
          <SummaryRow s={summarize(shown)} />
          <Toolbar all={all} f={f} set={set} reset={reset} count={shown.length} />
          {shown.length === 0 ? (
            <Empty>No models match these filters.</Empty>
          ) : f.view === "table" ? (
            <CatalogTable rows={shown} sort={f.sort} set={set} />
          ) : (
            <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
              {shown.map((x) => (
                <ModelCard key={x.model.name} f={x} set={set} />
              ))}
            </div>
          )}
        </>
      )}
    </div>
    </CatalogSite.Provider>
    </CatalogName.Provider>
  );
}

// Summarizes what the filters left, not the whole catalog.
function SummaryRow({ s }: { s: Summary }) {
  const counts = (o: Record<string, number>) =>
    Object.entries(o)
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .map(([k, n]) => `${k} ${n}`)
      .join(" · ");
  const stats: { label: string; value: React.ReactNode; sub: string; good?: boolean }[] = [
    { label: "Models", value: s.models, sub: s.deprecated ? `${s.deprecated} deprecated` : "" },
    { label: "Variants", value: s.variants, sub: counts(s.engines) },
    {
      label: "Tuned",
      value: s.pairs,
      sub: s.models ? `${Math.round((s.pairs / s.models) * 100)}% of models` : "",
    },
    {
      label: "Median uplift",
      value: s.median == null ? "—" : formatUplift(s.median),
      sub: s.worst != null && s.best ? `${formatUplift(s.worst)} to ${formatUplift(s.best.pct)}` : "",
      good: s.median != null,
    },
    {
      label: "Best uplift",
      value: s.best ? formatUplift(s.best.pct) : "—",
      sub: s.best?.name ?? "",
      good: !!s.best,
    },
    { label: "Hardware", value: Object.keys(s.hardware).length, sub: counts(s.hardware) },
  ];
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
      {stats.map((x) => (
        <Card key={x.label} className="min-w-0 px-4 py-3">
          <div className="text-xs text-muted-foreground">{x.label}</div>
          <div className={cn("text-xl font-semibold tabular-nums", x.good && "text-success")}>
            {x.value}
          </div>
          <div className="min-h-4 text-xs break-words text-muted-foreground">{x.sub}</div>
        </Card>
      ))}
    </div>
  );
}

const selectClass = "h-9 rounded-md border bg-background px-2 text-sm";

function Toolbar({
  all,
  f,
  set,
  reset,
  count,
}: {
  all: Facets[];
  f: Filters;
  set: Update;
  reset: () => void;
  count: number;
}) {
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-2">
        <label className="relative min-w-60 flex-1">
          <span className="sr-only">Search models</span>
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            type="search"
            value={f.q}
            onChange={(e) => set({ q: e.target.value })}
            placeholder="Search models, variants, hardware…"
            className="pl-8"
          />
        </label>
        <FacetSelect
          label="Family"
          all="All families"
          value={f.family}
          values={all.flatMap((x) => (x.model.family ? [x.model.family] : []))}
          onChange={(v) => set({ family: v })}
        />
        <FacetSelect
          label="Engine"
          all="All engines"
          value={f.engine}
          values={all.flatMap((x) => Array.from(new Set(x.engines)))}
          onChange={(v) => set({ engine: v })}
        />
        <FacetSelect
          label="Hardware"
          all="All hardware"
          value={f.hardware}
          values={all.flatMap((x) => x.hardware)}
          onChange={(v) => set({ hardware: v })}
        />
        <FacetSelect
          label="Tag"
          all="All tags"
          value={f.tag}
          values={all.flatMap((x) => x.model.tags ?? [])}
          onChange={(v) => set({ tag: v })}
        />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Toggle on={f.tuned} onClick={() => set({ tuned: !f.tuned })}>
          Tuned vs baseline
        </Toggle>
        {all.some((x) => x.deprecated) && (
          <Toggle on={f.hidedep} onClick={() => set({ hidedep: !f.hidedep })}>
            Hide deprecated
          </Toggle>
        )}
        <span className="ml-auto text-sm text-muted-foreground tabular-nums">
          {count === all.length ? `${all.length} models` : `${count} of ${all.length} models`}
        </span>
        <select
          aria-label="Sort by"
          className={selectClass}
          value={f.sort}
          onChange={(e) => set({ sort: e.target.value })}
        >
          <option value="name">Sort by name</option>
          <option value="uplift">Sort by uplift</option>
          <option value="family">Sort by family</option>
        </select>
        <ViewSwitch view={f.view} onChange={(view) => set({ view })} />
        <Button variant="ghost" size="sm" onClick={reset}>
          Reset
        </Button>
      </div>
    </div>
  );
}

// Options carry their counts across the whole catalog. A value from a stale
// link that no model has any more is still listed, at 0, so the filter that is
// hiding everything can be seen and cleared.
function FacetSelect({
  label,
  all,
  value,
  values,
  onChange,
}: {
  label: string;
  all: string;
  value: string;
  values: string[];
  onChange: (v: string) => void;
}) {
  const counts = new Map<string, number>();
  for (const v of values) counts.set(v, (counts.get(v) ?? 0) + 1);
  if (value && !counts.has(value)) counts.set(value, 0);
  return (
    <select
      aria-label={label}
      className={selectClass}
      value={value}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">{all}</option>
      {Array.from(counts)
        .sort((a, b) => a[0].localeCompare(b[0]))
        .map(([v, n]) => (
          <option key={v} value={v}>
            {v} ({n})
          </option>
        ))}
    </select>
  );
}

function Toggle({
  on,
  onClick,
  children,
}: {
  on: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={onClick}
      className={cn(
        "h-8 rounded-full border px-3 text-xs font-medium transition-colors",
        on ? "border-primary bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-muted",
      )}
    >
      {children}
    </button>
  );
}

function ViewSwitch({ view, onChange }: { view: View; onChange: (v: View) => void }) {
  const option = (v: View, Icon: typeof Table2, label: string) => (
    <button
      type="button"
      aria-pressed={view === v}
      onClick={() => onChange(v)}
      className={cn(
        "inline-flex h-7 items-center gap-1.5 rounded px-2.5 text-xs font-medium transition-colors",
        view === v ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
      )}
    >
      <Icon className="size-3.5" />
      {label}
    </button>
  );
  return (
    <div role="group" aria-label="View" className="inline-flex rounded-md bg-muted p-0.5">
      {option("table", Table2, "Table")}
      {option("cards", LayoutGrid, "Cards")}
    </div>
  );
}

function CatalogTable({ rows, sort, set }: { rows: Facets[]; sort: SortKey; set: Update }) {
  // Clicking a header sorts by it, and the Sort menu follows.
  const head = (key: SortKey, label: string, className?: string, title?: string, sub?: string) => (
    <TableHead
      className={className}
      title={title}
      aria-sort={sort === key ? (key === "uplift" ? "descending" : "ascending") : undefined}
    >
      <button
        type="button"
        onClick={() => set({ sort: key })}
        className={cn(
          "inline-flex items-center gap-1 tracking-wide uppercase",
          sort === key ? "text-foreground" : "hover:text-foreground",
        )}
      >
        {label}
        {sort === key &&
          (key === "uplift" ? <ArrowDown className="size-3" /> : <ArrowUp className="size-3" />)}
      </button>
      {sub && <div className="text-[10px] font-normal tracking-normal normal-case">{sub}</div>}
    </TableHead>
  );

  return (
    <Card className="overflow-hidden">
      <Table className="min-w-[760px]">
        <TableHeader className="bg-muted/40">
          <TableRow className="hover:bg-transparent">
            {head("name", "Model")}
            {head("family", "Family")}
            <TableHead>Variants (latest)</TableHead>
            {head("uplift", "Uplift vs baseline", "h-auto py-2 text-right", UPLIFT_HELP, "per workload · in + out tokens")}
            <TableHead>
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((x) => (
            <TableRow key={x.model.name}>
              <TableCell className="min-w-48 align-top">
                <ModelName f={x} />
                <div className="mt-0.5 text-xs text-muted-foreground">
                  <code>{x.model.name}</code> · v{x.model.latest}
                  {x.model.versions.length > 1 && ` · ${x.model.versions.length} versions`}
                </div>
              </TableCell>
              <TableCell className="align-top">
                {x.model.family ? (
                  <Chip onClick={() => set({ family: x.model.family ?? "" })}>{x.model.family}</Chip>
                ) : (
                  <span className="text-muted-foreground">—</span>
                )}
              </TableCell>
              <TableCell className="align-top">
                <div className="space-y-1.5">
                  {x.variants.map((v) => (
                    <VariantLine key={v.id} v={v} kind={x.kinds[v.id]} />
                  ))}
                </div>
              </TableCell>
              <TableCell className="align-top text-right">
                {x.cmp ? (
                  <UpliftCell cmp={x.cmp} latest={x.model.latest} model={x.model.name} />
                ) : (
                  <span className="text-muted-foreground">—</span>
                )}
              </TableCell>
              <TableCell className="align-top text-right">
                <DeployButton f={x} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

function ModelCard({ f, set }: { f: Facets; set: Update }) {
  const catalog = useContext(CatalogName);
  const m = f.model;
  const report = reportLink(useContext(CatalogSite), m.name, f.cmp?.report);
  return (
    <Card className="flex h-full flex-col gap-3 p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 space-y-1.5">
          <ModelName f={f} className="text-base" />
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            <Badge variant="muted">v{m.latest}</Badge>
            {m.versions.length > 1 && <span>{m.versions.length} versions</span>}
            {m.family && <Chip onClick={() => set({ family: m.family ?? "" })}>{m.family}</Chip>}
          </div>
        </div>
        {f.cmp && (
          <div className="shrink-0 text-right">
            <UpliftBadge cmp={f.cmp} />
            <div className="mt-1 text-xs text-muted-foreground">
              vs baseline{f.cmp.version !== m.latest && ` · v${f.cmp.version}`}
            </div>
          </div>
        )}
      </div>

      {!!f.cmp?.workloads.length && <WorkloadsLine cmp={f.cmp} />}
      {m.description && (
        <p className="line-clamp-2 text-sm text-muted-foreground" title={m.description}>
          {m.description}
        </p>
      )}
      {!!m.tags?.length && (
        <div className="flex flex-wrap gap-1">
          {m.tags.map((t) => (
            <Chip key={t} hue={tagHue(t)} onClick={() => set({ tag: t })}>
              {t}
            </Chip>
          ))}
        </div>
      )}
      <div className="text-xs text-muted-foreground">
        {m.source.hf}
        {m.source.sizeGiB ? ` · ${m.source.sizeGiB} GiB` : ""}
      </div>

      <ul className="divide-y rounded-md border">
        {f.variants.map((v) => (
          <li key={v.id} className="px-3 py-2">
            <VariantLine v={v} kind={f.kinds[v.id]} wrap />
            {/* Cards are narrow, so the hardware leads the description line
                rather than wrapping onto a line of its own. */}
            <p className="mt-1 line-clamp-2 text-xs text-muted-foreground" title={v.description}>
              <span className="font-medium text-foreground tabular-nums" title={gpuSupport(v.requires)}>
                {gpuShort(v.requires)}
              </span>
              {v.description && ` · ${v.description}`}
            </p>
          </li>
        ))}
      </ul>

      <div className="mt-auto flex items-center gap-2">
        <DeployButton f={f} />
        <Link to={withCatalog(`/catalog/${encodeURIComponent(m.name)}`, catalog)}>
          <Button size="sm" variant="ghost">
            Details
          </Button>
        </Link>
        {report && (
          <a
            href={report}
            target="_blank"
            rel="noreferrer"
            className={cn(buttonVariants({ variant: "outline", size: "sm" }), "ml-auto gap-1.5")}
          >
            <ChartColumn className="size-3.5 text-muted-foreground" />
            Perf report
          </a>
        )}
      </div>
    </Card>
  );
}

function ModelName({ f, className }: { f: Facets; className?: string }) {
  const catalog = useContext(CatalogName);
  const m = f.model;
  return (
    <div className={cn("flex flex-wrap items-center gap-2", className)}>
      <Link
        to={withCatalog(`/catalog/${encodeURIComponent(m.name)}`, catalog)}
        className="font-semibold hover:underline"
        title={m.description}
      >
        {m.displayName ?? m.name}
      </Link>
      {f.deprecated && (
        <Badge
          variant="destructive"
          title={typeof m.deprecated === "string" ? m.deprecated : undefined}
        >
          deprecated
        </Badge>
      )}
    </div>
  );
}

// id, badges, and (unless the caller places it) the hardware on one line.
function VariantLine({ v, kind, wrap = false }: { v: IndexVariant; kind: Kind; wrap?: boolean }) {
  const link = httpLink(v.link);
  return (
    <div className={cn("flex items-center gap-1.5", wrap ? "flex-wrap" : "whitespace-nowrap")}>
      <code className="text-xs">{v.id}</code>
      {v.default && <Badge variant="muted">default</Badge>}
      {kind.optimized && <Badge variant="success">optimized</Badge>}
      {kind.baseline && <Badge variant="outline">baseline</Badge>}
      {link && (
        <a
          href={link}
          target="_blank"
          rel="noreferrer"
          title={`Docs for ${v.id}`}
          className="text-muted-foreground hover:text-foreground"
        >
          <ExternalLink className="size-3.5" />
        </a>
      )}
      {!wrap && (
        <span
          className="ml-auto pl-4 text-xs tabular-nums"
          title={`${gpuCount(v.requires)} · ${gpuSupport(v.requires)}`}
        >
          {gpuShort(v.requires)}
        </span>
      )}
    </div>
  );
}

function UpliftBadge({ cmp, className }: { cmp: Comparison; className?: string }) {
  return (
    <Badge
      variant="success"
      className={cn("text-sm font-semibold tabular-nums", className)}
      title={
        `${cmp.optimized} vs ${cmp.baseline}, measured on v${cmp.version}` +
        (cmp.workloads.length ? `: ${workloadSummary(cmp)}` : "") +
        `. ${UPLIFT_HELP}`
      }
    >
      {cmp.uplift != null ? formatUplift(cmp.uplift) : "tuned"}
    </Badge>
  );
}

// The table's uplift cell: one row per workload, workload left and uplift
// right, so the numbers line up. The headline keeps the badge; the others use
// the same box unfilled, so their digits align with it. "Tokens" is said once,
// in the column header.
//   50k + 1.5k  [+64.2%]
//      8k + 1k    +7.6%
function UpliftCell({ cmp, latest, model }: { cmp: Comparison; latest: string; model: string }) {
  const report = reportLink(useContext(CatalogSite), model, cmp.report);
  const head = headlineWorkload(cmp);
  const label = "text-left font-mono text-xs whitespace-nowrap text-muted-foreground";
  return (
    <div className="inline-grid grid-cols-[auto_auto] items-center gap-x-2.5 gap-y-1">
      <span className={label}>{head?.name}</span>
      <UpliftBadge cmp={cmp} className="justify-self-end" />
      {otherWorkloads(cmp).map((w) => (
        <Fragment key={w.name}>
          <span className={label}>{w.name}</span>
          <Badge variant="success" className="justify-self-end bg-transparent text-sm font-semibold tabular-nums">
            {formatUplift(w.uplift)}
          </Badge>
        </Fragment>
      ))}
      {cmp.version !== latest && (
        <span className="col-span-2 text-right text-xs text-muted-foreground">measured on v{cmp.version}</span>
      )}
      {report && (
        <a
          href={report}
          target="_blank"
          rel="noreferrer"
          className="col-span-2 inline-flex items-center justify-end gap-1 text-xs text-muted-foreground hover:text-foreground hover:underline"
        >
          <ChartColumn className="size-3.5" />
          perf report
        </a>
      )}
    </div>
  );
}

// A card's full-width breakdown: "+58% at 50k + 1.5k tokens · +23.4% at 8k + 1k tokens".
function WorkloadsLine({ cmp }: { cmp: Comparison }) {
  return (
    <p className="text-xs leading-6 text-muted-foreground" title={UPLIFT_HELP}>
      <WorkloadList workloads={cmp.workloads} />
    </p>
  );
}

// Written out in full so Tailwind generates them.
const HUE_CLASSES: Record<Hue, string> = {
  blue: "bg-blue-500/10 text-blue-700 hover:bg-blue-500/15",
  violet: "bg-violet-500/10 text-violet-700 hover:bg-violet-500/15",
  teal: "bg-teal-500/10 text-teal-700 hover:bg-teal-500/15",
  orange: "bg-orange-500/10 text-orange-700 hover:bg-orange-500/15",
  pink: "bg-pink-500/10 text-pink-700 hover:bg-pink-500/15",
  amber: "bg-amber-500/10 text-amber-700 hover:bg-amber-500/15",
  indigo: "bg-indigo-500/10 text-indigo-700 hover:bg-indigo-500/15",
  green: "bg-green-500/10 text-green-700 hover:bg-green-500/15",
  cyan: "bg-cyan-500/10 text-cyan-700 hover:bg-cyan-500/15",
  slate: "bg-slate-500/10 text-slate-700 hover:bg-slate-500/15",
};

// Family chips stay neutral, so a family never reads as a tag.
function Chip({ onClick, hue, children }: { onClick: () => void; hue?: Hue; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "rounded-full bg-muted px-2 py-0.5 text-xs whitespace-nowrap text-muted-foreground hover:text-foreground",
        hue && HUE_CLASSES[hue],
      )}
    >
      {children}
    </button>
  );
}

function DeployButton({ f }: { f: Facets }) {
  const catalog = useContext(CatalogName);
  const m = f.model;
  const report = reportLink(useContext(CatalogSite), m.name, f.cmp?.report);
  // A variant is a hardware and parallelism decision, so it is the operator's
  // to make: deploying the default because it sorted first is how a model ends
  // up on the wrong accelerator. Only a model with one variant has nothing to
  // ask about.
  const [choosing, setChoosing] = useState(false);
  const only = f.variants.length === 1 ? f.variants[0] : undefined;

  if (only) {
    return (
      <Link to={deployTo(m.name, only.id, catalog)}>
        <Button size="sm">Deploy</Button>
      </Link>
    );
  }
  return (
    <>
      <Button size="sm" onClick={() => setChoosing(true)}>
        Deploy
      </Button>
      <Dialog
        open={choosing}
        onClose={() => setChoosing(false)}
        title={`Deploy ${m.displayName ?? m.name}`}
        subtitle={`v${m.latest} — ${f.variants.length} variants. Each is a different accelerator and parallelism shape; pick the one this cluster runs.`}
      >
        <div className="space-y-2.5 text-left">
          {f.variants.map((v) => {
            const kind = f.kinds[v.id];
            const link = httpLink(v.link);
            return (
              <div key={v.id} className="rounded-lg border p-3 transition-colors hover:bg-muted/30">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-semibold">{v.id}</span>
                    {v.default && <Badge variant="muted">default</Badge>}
                    {kind.optimized && <Badge variant="success">optimized</Badge>}
                    {kind.baseline && <Badge variant="outline">baseline</Badge>}
                    <Badge variant="outline">{v.engine}</Badge>
                  </div>
                  <div className="flex items-center gap-2">
                    {report && f.cmp?.optimized === v.id && (
                      <a
                        href={report}
                        target="_blank"
                        rel="noreferrer"
                        className={buttonVariants({ variant: "outline", size: "sm" })}
                      >
                        <ChartColumn className="size-3.5 text-muted-foreground" />
                        <span>Report</span>
                      </a>
                    )}
                    {link && (
                      <a
                        href={link}
                        target="_blank"
                        rel="noreferrer"
                        className={buttonVariants({ variant: "outline", size: "sm" })}
                      >
                        <ExternalLink className="size-3.5 text-muted-foreground" />
                        <span>Docs</span>
                      </a>
                    )}
                    <Link to={deployTo(m.name, v.id, catalog)}>
                      <Button size="sm">Deploy</Button>
                    </Link>
                  </div>
                </div>
                <div className="mt-1.5 text-xs text-muted-foreground">
                  {gpuShort(v.requires)} · {vendorLabel(v.requires.vendor)} ·{" "}
                  {v.requires.topology ?? "single-node"}
                  {v.requires.rdma && " · RDMA"} · chart {v.chart.name}-{v.chart.version}
                </div>
                {v.description && <p className="mt-1.5 text-xs text-muted-foreground">{v.description}</p>}
              </div>
            );
          })}
        </div>
      </Dialog>
    </>
  );
}

function deployTo(model: string, variant: string, catalog: string): string {
  return withCatalog(`/deploy/${encodeURIComponent(model)}?variant=${encodeURIComponent(variant)}`, catalog);
}
