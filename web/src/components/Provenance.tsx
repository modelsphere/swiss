import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { Plan } from "@/lib/api";
import { Code } from "@/components/ui/code";
import { toYaml } from "@/lib/yaml";

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

interface File {
  key: string;
  name: string;
  tone: (typeof TONE)[keyof typeof TONE] | "default";
  what: string;
  count?: number;
  body: string;
}

// One collapsible per file, closed by default. Expanded, a composed plan is
// several hundred lines of YAML and the tab is a scroll; the headers carry the
// value counts, which is what says where to look.
export function Provenance({ plan }: { plan: Plan }) {
  const files = filesOf(plan);
  const [open, setOpen] = useState<Set<string>>(new Set());
  const allOpen = open.size === files.length;

  return (
    <div className="space-y-2">
      <div className="flex justify-end">
        <button
          type="button"
          onClick={() => setOpen(allOpen ? new Set() : new Set(files.map((f) => f.key)))}
          className="text-xs text-muted-foreground underline hover:text-foreground"
        >
          {allOpen ? "close all" : "open all"}
        </button>
      </div>

      {files.map((f) => {
        const isOpen = open.has(f.key);
        return (
          <section key={f.key} className="rounded-md border">
            <button
              type="button"
              aria-expanded={isOpen}
              onClick={() =>
                setOpen((prev) => {
                  const next = new Set(prev);
                  if (!next.delete(f.key)) next.add(f.key);
                  return next;
                })
              }
              className="flex w-full flex-wrap items-center gap-2 px-3 py-2 text-left hover:bg-muted/40"
            >
              {isOpen ? (
                <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
              ) : (
                <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
              )}
              <Badge variant={f.tone}>{f.name}</Badge>
              <span className="text-xs text-muted-foreground">{f.what}</span>
              {f.count !== undefined && (
                <span className="ml-auto text-xs text-muted-foreground">{f.count} values</span>
              )}
            </button>
            {isOpen && <Code lang="yaml" className="rounded-none border-x-0 border-b-0">{f.body}</Code>}
          </section>
        );
      })}
    </div>
  );
}

// The files a plan materialises into: the release declaration, and one values
// document per layer in merge order.
function filesOf(plan: Plan): File[] {
  const out: File[] = [];
  if (plan.helmfile) {
    out.push({
      key: "helmfile",
      name: "helmfile.yaml",
      tone: "default",
      what: "the release declaration these values are applied through",
      body: plan.helmfile,
    });
  }
  for (const layer of LAYERS) {
    const tree = plan.layers?.[layer];
    if (!tree) continue;
    out.push({
      key: layer,
      name: `${layer}.yaml`,
      tone: TONE[layer],
      what: WHAT[layer],
      count: leafCount(tree),
      body: toYaml(tree),
    });
  }
  return out;
}

function leafCount(tree: Record<string, unknown>): number {
  let n = 0;
  for (const v of Object.values(tree)) {
    n += v && typeof v === "object" && !Array.isArray(v)
      ? leafCount(v as Record<string, unknown>)
      : 1;
  }
  return n;
}
