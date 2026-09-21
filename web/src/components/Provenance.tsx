import { Badge } from "@/components/ui/badge";
import type { Plan } from "@/lib/api";
import { subtree, toYaml } from "@/lib/yaml";

// In merge order, and `edit` last because that is when it is applied. Leaving it
// out of this list is how the one input exempt from ownership becomes the one
// input nobody can see -- the opposite of why it is labelled at all.
const LAYERS = ["catalog", "site", "derived", "form", "edit"] as const;

const TONE = {
  catalog: "muted",
  site: "outline",
  derived: "warning",
  form: "success",
  edit: "destructive",
} as const;

const WHAT = {
  catalog: "from the model entry; not editable here",
  site: "from this cluster's profile",
  derived: "computed from the variant and the site",
  form: "what this deploy set",
  edit: "typed into the plan editor; exempt from layer ownership",
} as const;

export function Provenance({ plan }: { plan: Plan }) {
  const byLayer = groupByLayer(plan);

  return (
    <div className="space-y-4">
      {plan.helmfile && (
        <section>
          <div className="mb-1.5 flex flex-wrap items-center gap-2">
            <Badge variant="default">helmfile.yaml</Badge>
            <span className="text-xs text-muted-foreground">
              the release declaration these values are applied through
            </span>
          </div>
          <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 text-xs leading-relaxed">
            {plan.helmfile}
          </pre>
        </section>
      )}

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
