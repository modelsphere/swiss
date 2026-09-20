import { Badge } from "@/components/ui/badge";
import type { Plan } from "@/lib/api";
import { subtree, toYaml } from "@/lib/yaml";

const LAYERS = ["catalog", "site", "derived", "form"] as const;

const TONE = {
  catalog: "muted",
  site: "outline",
  derived: "warning",
  form: "success",
} as const;

const WHAT = {
  catalog: "from the model entry; not editable here",
  site: "from this cluster's profile",
  derived: "computed from the variant and the site",
  form: "what this deploy set",
} as const;

export function Provenance({ plan }: { plan: Plan }) {
  const byLayer = groupByLayer(plan);

  return (
    <div className="space-y-4">
      {LAYERS.filter((l) => byLayer.has(l)).map((layer) => {
        const paths = byLayer.get(layer)!;
        return (
          <section key={layer}>
            <div className="mb-1.5 flex flex-wrap items-center gap-2">
              <Badge variant={TONE[layer]}>{layer}</Badge>
              <span className="text-xs text-muted-foreground">{WHAT[layer]}</span>
              <span className="ml-auto text-xs text-muted-foreground">{paths.length} values</span>
            </div>
            <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 text-xs leading-relaxed">
              {toYaml(subtree(plan.values, paths))}
            </pre>
          </section>
        );
      })}
    </div>
  );
}

export function groupByLayer(plan: Plan): Map<string, string[]> {
  const byLayer = new Map<string, string[]>();
  for (const [path, layer] of Object.entries(plan.provenance ?? {})) {
    byLayer.set(layer, [...(byLayer.get(layer) ?? []), path]);
  }
  for (const paths of byLayer.values()) paths.sort();
  return byLayer;
}
