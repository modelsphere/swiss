import { Badge } from "@/components/ui/badge";
import type { Plan } from "@/lib/api";

const LAYERS = ["catalog", "site", "derived", "form"] as const;

const TONE = {
  catalog: "muted",
  site: "outline",
  derived: "warning",
  form: "success",
} as const;

export function Provenance({ plan }: { plan: Plan }) {
  const prov = plan.provenance ?? {};
  const byLayer = new Map<string, string[]>();
  for (const [path, layer] of Object.entries(prov)) {
    byLayer.set(layer, [...(byLayer.get(layer) ?? []), path]);
  }

  return (
    <div className="space-y-3">
      {LAYERS.filter((l) => byLayer.has(l)).map((layer) => (
        <div key={layer}>
          <div className="mb-1 flex items-center gap-2">
            <Badge variant={TONE[layer]}>{layer}</Badge>
            <span className="text-xs text-muted-foreground">
              {byLayer.get(layer)!.length} values
            </span>
          </div>
          <dl className="grid gap-x-4 text-xs sm:grid-cols-[minmax(0,18rem)_1fr]">
            {byLayer
              .get(layer)!
              .sort()
              .map((path) => (
                <div key={path} className="contents">
                  <dt className="truncate text-muted-foreground">{path}</dt>
                  <dd className="truncate font-mono">{render(get(plan.values, path))}</dd>
                </div>
              ))}
          </dl>
        </div>
      ))}
    </div>
  );
}

function get(tree: Record<string, unknown>, path: string): unknown {
  let cur: unknown = tree;
  for (const seg of path.split(".")) {
    if (typeof cur !== "object" || cur === null) return undefined;
    cur = (cur as Record<string, unknown>)[seg];
  }
  return cur;
}

function render(v: unknown): string {
  if (v === undefined) return "";
  if (Array.isArray(v)) return v.map(render).join(" ");
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}
