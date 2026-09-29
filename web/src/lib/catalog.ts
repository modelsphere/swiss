import type { IndexModel, IndexVariant, Tuning } from "@/lib/api";
import { gpuModels } from "@/lib/gpu";

export interface Kind {
  optimized: boolean;
  baseline: boolean;
}

// Which variant is tuned, and by how much, comes from the model's `tuning`
// (metadata.yaml, by way of the index). A model without it falls back to
// reading variant ids and descriptions, by the rules swiss-catalog's
// hack/build-site.js used before the field existed.
// TODO: delete the fallback (guessKind, upliftOf) once every tuned model in the
// catalog records `tuning`.
function guessKind(v: Pick<IndexVariant, "id" | "description">): Kind {
  const id = v.id.toLowerCase();
  const desc = (v.description ?? "").toLowerCase();
  const optimized = /optimi[sz]ed/.test(id) || /\b(optimi[sz]ed|tuned by)\b/.test(desc);
  // A tuned description says "vs the baseline variant", so only the id or a
  // leading "Baseline" marks a baseline, and optimized wins.
  const baseline = !optimized && (id.includes("baseline") || /^\s*baseline\b/.test(desc));
  return { optimized, baseline };
}

// "+44% on the tuning benchmark vs the baseline variant" -> 44.
function upliftOf(description?: string): number | null {
  const m = description?.match(/\+(\d+(?:\.\d+)?)\s*%/);
  return m ? Number(m[1]) : null;
}

// A model that records any tuning is read from it alone: mixing in the guess
// would label variants the catalog deliberately left unlabelled.
export function variantKind(v: Pick<IndexVariant, "id" | "description">, tuning?: Tuning[]): Kind {
  if (!tuning?.length) return guessKind(v);
  const optimized = tuning.some((t) => t.optimized === v.id);
  return { optimized, baseline: !optimized && tuning.some((t) => t.baseline === v.id) };
}

export function formatUplift(pct: number): string {
  return `${pct < 0 ? "" : "+"}${Math.round(pct * 10) / 10}%`;
}

export interface Comparison {
  baseline: string;
  optimized: string;
  uplift: number | null;
  // The version the number was measured on. It can be older than the one
  // shown, when a later version kept the variants without re-measuring.
  version: string;
  report?: string;
  workloads: { name: string; uplift: number }[];
}

// The pair shown for one version's variants. A recorded pair must name
// variants this version has; among those, one measured on this version wins,
// then one whose tuned variant is the default.
export function comparison(variants: IndexVariant[], version: string, tuning?: Tuning[]): Comparison | null {
  if (tuning?.length) {
    const ids = new Set(variants.map((v) => v.id));
    const fits = tuning.filter((t) => ids.has(t.baseline) && ids.has(t.optimized));
    const def = variants.find((v) => v.default)?.id;
    const t = fits.find((t) => t.version === version) ?? fits.find((t) => t.optimized === def) ?? fits[0];
    return t
      ? {
          baseline: t.baseline,
          optimized: t.optimized,
          uplift: t.uplift ?? null,
          version: t.version,
          report: t.report,
          workloads: t.workloads ?? [],
        }
      : null;
  }
  const base = variants.find((v) => guessKind(v).baseline);
  const tuned = variants.filter((v) => guessKind(v).optimized);
  const opt = tuned.find((v) => v.default) ?? tuned[0];
  if (!base || !opt) return null;
  return { baseline: base.id, optimized: opt.id, uplift: upliftOf(opt.description), version, workloads: [] };
}

// What an uplift number means, for tooltips. Workload names are the report's:
// input + output tokens per request.
export const UPLIFT_HELP =
  "Throughput of the tuned variant over its baseline, from the tuning benchmark. " +
  "Workloads are input + output tokens per request, e.g. 50k + 1.5k.";

// The workload the headline number was measured on: the first whose number is
// the headline. Absent when the benchmark recorded no workloads.
export function headlineWorkload(c: Comparison) {
  return c.workloads.find((w) => w.uplift === c.uplift);
}

export function otherWorkloads(c: Comparison) {
  const head = headlineWorkload(c);
  return c.workloads.filter((w) => w !== head);
}

// One result with its workload: "+23.4% at 8k + 1k tokens".
export function workloadLabel(w: { name: string; uplift: number }): string {
  return `${formatUplift(w.uplift)} at ${w.name} tokens`;
}

// Every result in one line: "+58% at 50k + 1.5k tokens, +23.4% at 8k + 1k tokens".
export function workloadSummary(c: Comparison): string {
  return c.workloads.map(workloadLabel).join(", ");
}

// Tag colors. The tags in use get a fixed hue; any other tag gets one picked
// from its name, so a new tag is colored and stays the same color. The same
// table as swiss-catalog's hack/build-site.js.
export const HUES = ["blue", "violet", "teal", "orange", "pink", "amber", "indigo", "green", "cyan", "slate"] as const;
export type Hue = (typeof HUES)[number];

const TAG_HUES: Record<string, Hue> = {
  chat: "blue",
  reasoning: "violet",
  "tool-use": "teal",
  moe: "orange",
  "speculative-decoding": "pink",
  "long-context": "amber",
  "multi-node": "indigo",
  vision: "green",
  fp4: "cyan",
  fallback: "slate",
};

export function tagHue(tag: string): Hue {
  if (TAG_HUES[tag]) return TAG_HUES[tag];
  let h = 0;
  for (const ch of tag) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return HUES[h % HUES.length];
}

// variants[].link is only schema-checked as a URI, which admits javascript:.
export function httpLink(link?: string): string | undefined {
  return link && /^https?:\/\//i.test(link) ? link : undefined;
}

// A tuned variant's perf report, where the catalog's site serves it:
// <site>models/<name>/<report>. The link is built here rather than carried by
// the variant, whose version file is immutable and could not name a page
// published after it. Undefined when the catalog publishes no site.
export function reportLink(site: string | undefined, model: string, report?: string): string | undefined {
  if (!site || !report) return undefined;
  try {
    return httpLink(new URL(`models/${encodeURIComponent(model)}/${encodeURIComponent(report)}`, site).href);
  } catch {
    return undefined;
  }
}

export function latestVersion(m: IndexModel) {
  return m.versions.find((v) => v.version === m.latest) ?? m.versions[0];
}

// What the page filters, sorts and summarizes on, one per model.
export interface Facets {
  model: IndexModel;
  // The latest version's, default first.
  variants: IndexVariant[];
  // One per variant, so a summary can count variants per engine.
  engines: string[];
  hardware: string[];
  // Each variant's role, by id.
  kinds: Record<string, Kind>;
  cmp: Comparison | null;
  deprecated: boolean;
  text: string;
}

export function facetsOf(m: IndexModel): Facets {
  const variants = latestVersion(m).variants
    .slice()
    .sort((a, b) => Number(!!b.default) - Number(!!a.default));
  const hardware = Array.from(new Set(variants.flatMap((v) => gpuModels(v.requires)))).sort();
  const text = [
    m.name,
    m.displayName,
    m.description,
    m.family,
    m.source.hf,
    ...(m.tags ?? []),
    ...variants.flatMap((v) => [v.id, v.engine, v.description]),
    ...hardware,
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  return {
    model: m,
    variants,
    engines: variants.map((v) => v.engine),
    hardware,
    kinds: Object.fromEntries(variants.map((v) => [v.id, variantKind(v, m.tuning)])),
    cmp: comparison(variants, m.latest, m.tuning),
    deprecated: !!m.deprecated,
    text,
  };
}

export interface Summary {
  models: number;
  deprecated: number;
  variants: number;
  engines: Record<string, number>;
  hardware: Record<string, number>;
  pairs: number;
  median: number | null;
  worst: number | null;
  best: { name: string; pct: number } | null;
}

export function summarize(list: Facets[]): Summary {
  const engines: Record<string, number> = {};
  const hardware: Record<string, number> = {};
  const uplifts: { name: string; pct: number }[] = [];
  let variants = 0;
  let pairs = 0;
  let deprecated = 0;
  for (const f of list) {
    variants += f.variants.length;
    if (f.deprecated) deprecated++;
    if (f.cmp) pairs++;
    if (f.cmp?.uplift != null) uplifts.push({ name: f.model.name, pct: f.cmp.uplift });
    for (const e of f.engines) engines[e] = (engines[e] ?? 0) + 1;
    for (const h of f.hardware) hardware[h] = (hardware[h] ?? 0) + 1;
  }
  uplifts.sort((a, b) => a.pct - b.pct);
  const n = uplifts.length;
  const median =
    n === 0 ? null : n % 2 ? uplifts[(n - 1) / 2].pct : (uplifts[n / 2 - 1].pct + uplifts[n / 2].pct) / 2;
  return {
    models: list.length,
    deprecated,
    variants,
    engines,
    hardware,
    pairs,
    median,
    worst: n ? uplifts[0].pct : null,
    best: n ? uplifts[n - 1] : null,
  };
}

export type SortKey = "name" | "uplift" | "family";

const byName = (a: Facets, b: Facets) => a.model.name.localeCompare(b.model.name);

export const sorters: Record<SortKey, (a: Facets, b: Facets) => number> = {
  name: byName,
  // Highest first; models without a number go last.
  uplift: (a, b) =>
    (b.cmp?.uplift ?? -Infinity) - (a.cmp?.uplift ?? -Infinity) || byName(a, b),
  // Models without a family go last.
  family: (a, b) =>
    Number(!a.model.family) - Number(!b.model.family) ||
    (a.model.family ?? "").localeCompare(b.model.family ?? "") ||
    byName(a, b),
};
