import type { ClusterInfo, Plan, PlanRequest } from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { Toggle } from "@/components/ui/toggle";

// One form, two routes. Deploy composes it against a catalog model and Upgrade
// against a release's stored plan, but a setting that means one thing on one
// page and another on the other is how the two drift into disagreeing about
// what a deploy is.
export interface Form {
  edits: string;
  priorityClassName: string;
  schedulerName: string;
  cart: boolean;
  modelRoute: boolean;
  slo: boolean;
  serviceMonitor: boolean;
  release: string;
  namespace: string;
  serviceId: string;
  localPath: string;
  replicaCount: string;
  minReplicas: string;
  maxReplicas: string;
  route: string;
}

export const EMPTY: Form = {
  edits: "",
  priorityClassName: "",
  schedulerName: "",
  cart: true,
  modelRoute: false,
  slo: false,
  serviceMonitor: false,
  release: "",
  namespace: "",
  serviceId: "",
  localPath: "",
  replicaCount: "",
  minReplicas: "",
  maxReplicas: "",
  route: "",
};

export function DeploySettings({
  form,
  onChange,
  cluster,
  serviceIdPlaceholder,
  releaseHint,
  // Upgrade locks these two: they name the helm release being upgraded, and
  // changing them does not rename it, it installs a second one.
  lockIdentity = false,
}: {
  form: Form;
  onChange: (patch: Partial<Form>) => void;
  cluster?: ClusterInfo;
  serviceIdPlaceholder?: string;
  releaseHint?: string;
  lockIdentity?: boolean;
}) {
  const set =
    (k: keyof Form) =>
    (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
      onChange({ [k]: e.target.value } as Partial<Form>);
  const toggle = (k: keyof Form) => (v: boolean) => onChange({ [k]: v } as Partial<Form>);

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Deploy settings</CardTitle>
          <p className="text-sm text-muted-foreground">
            Engine flags, probes and the image come from the catalog and are not editable here.
            To change those, pick another variant or move the catalog reference.
          </p>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <Field
            label="Service ID"
            hint="the identity everything is named after: release, route, scaler, SLO"
          >
            <Input
              value={form.serviceId}
              onChange={set("serviceId")}
              placeholder={serviceIdPlaceholder}
            />
          </Field>
          <Field
            label="Release"
            hint={lockIdentity ? "the release being upgraded" : (releaseHint ?? "helm release name")}
          >
            <Input
              value={form.release}
              onChange={set("release")}
              placeholder={form.serviceId}
              disabled={lockIdentity}
            />
          </Field>
          <Field
            label="Namespace"
            hint={
              lockIdentity
                ? "moving a release between namespaces installs a second one"
                : `defaults to ${cluster?.namespace ?? "the profile's"}`
            }
          >
            <Input value={form.namespace} onChange={set("namespace")} disabled={lockIdentity} />
          </Field>
          <Field label="Route" hint="openresty path; empty uses the service ID">
            <Input value={form.route} onChange={set("route")} placeholder={form.serviceId} />
          </Field>
          <Field label="Replicas" hint="fixed count; leave empty when the scaler owns it">
            <Input value={form.replicaCount} onChange={set("replicaCount")} inputMode="numeric" />
          </Field>
          <Field label="Model path" hint="overrides the site's path template">
            <Input value={form.localPath} onChange={set("localPath")} />
          </Field>
          <Field label="Scaler min" hint="filling either turns the scaler on">
            <Input value={form.minReplicas} onChange={set("minReplicas")} inputMode="numeric" />
          </Field>
          <Field label="Scaler max">
            <Input value={form.maxReplicas} onChange={set("maxReplicas")} inputMode="numeric" />
          </Field>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Features</CardTitle>
          <p className="text-sm text-muted-foreground">
            Written into the plan either way — nothing is left to a chart default.
          </p>
        </CardHeader>
        <CardContent className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Toggle
            label="CART"
            hint="cache-aware router in front of the backends"
            checked={form.cart}
            onChange={toggle("cart")}
          />
          <Toggle
            label="ModelRoute"
            hint="publish to openresty and the monitor"
            checked={effective(form).modelRoute}
            onChange={toggle("modelRoute")}
          />
          <Toggle
            label="SLO requirement"
            hint="LLMSLORequirement for this service"
            checked={form.slo}
            onChange={toggle("slo")}
          />
          <Toggle
            label="ServiceMonitor"
            hint="scrape metrics into Prometheus"
            checked={form.serviceMonitor}
            onChange={toggle("serviceMonitor")}
          />
        </CardContent>
      </Card>

      <details className="rounded-lg border">
        <summary className="cursor-pointer px-4 py-3 text-sm font-medium">
          Advanced — scheduling
        </summary>
        <div className="grid gap-4 border-t p-4 sm:grid-cols-2">
          <Field label="Priority class" hint="priorityClassName">
            <Input value={form.priorityClassName} onChange={set("priorityClassName")} />
          </Field>
          <Field label="Scheduler" hint="schedulerName, e.g. volcano">
            <Input value={form.schedulerName} onChange={set("schedulerName")} />
          </Field>
        </div>
      </details>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Plan editor</CardTitle>
          <p className="text-sm text-muted-foreground">
            Any values key, applied after every layer — scheduling and resources the fields
            above do not cover, and catalog or site keys the form refuses. It is the one input
            exempt from layer ownership, so nothing here is checked against the layer that owns
            it. Everything it sets is shown as its own <span className="font-medium">edit</span>{" "}
            layer in the composed plan, so it is never invisible.
          </p>
        </CardHeader>
        <CardContent>
          <textarea
            value={form.edits}
            onChange={set("edits")}
            spellCheck={false}
            rows={10}
            placeholder={EDITS_PLACEHOLDER}
            className="w-full rounded-md border bg-background px-3 py-2 font-mono text-xs"
          />
        </CardContent>
      </Card>
    </>
  );
}

const EDITS_PLACEHOLDER = `# last word, any key
nodeSelector:
  nvidia.com/gpu.product: NVIDIA-B300-SXM6-AC
tolerations:
  - key: gpu
    operator: Exists
    effect: NoSchedule
resources:
  limits:
    rdma/hca_shared: "1"
extraArgs:
  - --tp-size=4`;

const num = (v: string) => (v.trim() === "" ? undefined : Number(v));

// Two features are switched on by being filled in rather than by their toggle:
// naming a route turns routing on, and typing a scaling bound turns the scaler
// on. Derived in one place so the toggle and the request cannot disagree about
// what this deploy is asking for.
export function effective(f: Form) {
  return {
    modelRoute: f.modelRoute || f.route.trim() !== "",
    scaler: num(f.minReplicas) !== undefined || num(f.maxReplicas) !== undefined,
  };
}

export function planRequest(
  f: Form,
  opts: { model: string; version?: string; variant?: string; fromRelease?: string },
): PlanRequest {
  const overrides: Record<string, unknown> = {};
  const on = effective(f);

  if (num(f.replicaCount) !== undefined) overrides.replicaCount = num(f.replicaCount);

  // Every flag is sent explicitly. Leaving one out would hand the decision to a
  // chart default, which is the thing this is here to avoid.
  overrides.cart = { enabled: f.cart };
  overrides.sloRequirement = { enabled: f.slo };
  overrides.serviceMonitor = { enabled: f.serviceMonitor };

  const route = f.route.trim();
  overrides.modelRoute = route
    ? { enabled: on.modelRoute, nginx: { route } }
    : { enabled: on.modelRoute };

  const scaler: Record<string, unknown> = { enabled: on.scaler };
  if (num(f.minReplicas) !== undefined) scaler.minReplicas = num(f.minReplicas);
  if (num(f.maxReplicas) !== undefined) scaler.maxReplicas = num(f.maxReplicas);
  overrides.scaler = scaler;

  if (f.priorityClassName.trim()) overrides.priorityClassName = f.priorityClassName.trim();
  if (f.schedulerName.trim()) overrides.schedulerName = f.schedulerName.trim();

  return {
    model: opts.model,
    fromRelease: opts.fromRelease,
    version: opts.version || undefined,
    editsYAML: f.edits.trim() || undefined,
    variant: opts.variant || undefined,
    release: f.release.trim() || undefined,
    namespace: f.namespace.trim() || undefined,
    serviceId: f.serviceId.trim() || undefined,
    localPath: f.localPath.trim() || undefined,
    overrides,
  };
}

// formFromPlan seeds the upgrade form from what a release was deployed with.
// The plan stores the composed overrides tree, not the form fields, so this is
// the inverse of planRequest -- and the pair is why an upgrade can show the
// settings rather than only promise it carried them forward.
export function formFromPlan(plan: Plan): Form {
  const o = (plan.overrides ?? {}) as Record<string, unknown>;
  const at = (path: string): unknown =>
    path.split(".").reduce<unknown>((acc, k) => {
      if (acc && typeof acc === "object" && k in (acc as Record<string, unknown>)) {
        return (acc as Record<string, unknown>)[k];
      }
      return undefined;
    }, o);

  const str = (path: string) => {
    const v = at(path);
    return v === undefined || v === null ? "" : String(v);
  };
  const bool = (path: string, fallback = false) => {
    const v = at(path);
    return typeof v === "boolean" ? v : fallback;
  };

  return {
    ...EMPTY,
    release: plan.release.name,
    namespace: plan.release.namespace,
    serviceId: str("serviceId"),
    localPath: str("model.localPath"),
    replicaCount: str("replicaCount"),
    minReplicas: str("scaler.minReplicas"),
    maxReplicas: str("scaler.maxReplicas"),
    route: str("modelRoute.nginx.route"),
    cart: bool("cart.enabled", true),
    modelRoute: bool("modelRoute.enabled"),
    slo: bool("sloRequirement.enabled"),
    serviceMonitor: bool("serviceMonitor.enabled"),
    priorityClassName: str("priorityClassName"),
    schedulerName: str("schedulerName"),
    edits: plan.edits && Object.keys(plan.edits).length > 0 ? toYamlish(plan.edits) : "",
  };
}

// The plan editor round-trips through text, so edits carried forward have to be
// rendered back to YAML. Display-grade is enough: the server re-parses it.
function toYamlish(tree: Record<string, unknown>): string {
  return yamlLines(tree, 0).join("\n");
}

function yamlLines(value: Record<string, unknown>, indent: number): string[] {
  const pad = " ".repeat(indent);
  const out: string[] = [];
  for (const [k, v] of Object.entries(value)) {
    if (Array.isArray(v)) {
      out.push(`${pad}${k}:`);
      for (const item of v) {
        if (item && typeof item === "object") {
          const nested = yamlLines(item as Record<string, unknown>, indent + 4);
          out.push(`${pad}  - ${nested[0].trim()}`, ...nested.slice(1));
        } else {
          out.push(`${pad}  - ${scalar(item)}`);
        }
      }
    } else if (v && typeof v === "object") {
      out.push(`${pad}${k}:`, ...yamlLines(v as Record<string, unknown>, indent + 2));
    } else {
      out.push(`${pad}${k}: ${scalar(v)}`);
    }
  }
  return out;
}

function scalar(v: unknown): string {
  if (typeof v === "string" && (v === "" || /[:#{}[\],&*?|<>=!%@`"']/.test(v))) {
    return JSON.stringify(v);
  }
  return String(v);
}
