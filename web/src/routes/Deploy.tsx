import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronLeft, CircleCheck, TriangleAlert } from "lucide-react";
import {
  api,
  deployApi,
  type ApplyResult,
  type ClusterInfo,
  type DiffResult,
  type Plan,
  type PlanRequest,
} from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { Toggle } from "@/components/ui/toggle";
import { DiffView } from "@/components/DiffView";
import { Provenance } from "@/components/Provenance";
import { ErrorState, Loading } from "@/components/States";
import { Tabs } from "@/components/ui/tabs";
import { ReleaseStatus } from "@/components/ReleaseStatus";

interface Form {
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

const EMPTY: Form = {
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

export function Deploy() {
  const { name = "" } = useParams();
  const [params] = useSearchParams();
  const variantId = params.get("variant") ?? "";
  const version = params.get("version") ?? "";

  const model = useQuery({
    queryKey: ["model", name, version],
    queryFn: () => api.model(name, version || undefined),
  });
  const cluster = useQuery({ queryKey: ["cluster"], queryFn: api.cluster });

  const [form, setForm] = useState<Form>({ ...EMPTY, serviceId: name });
  // serviceId is the identity everything else is named after: the release, the
  // openresty route, the LLMScaler and the LLMSLORequirement. The release name
  // follows it until someone types their own.
  const [releaseEdited, setReleaseEdited] = useState(false);
  const release = releaseEdited ? form.release : form.serviceId;
  const [plan, setPlan] = useState<Plan | null>(null);
  const [diff, setDiff] = useState<DiffResult | null>(null);
  const [applied, setApplied] = useState<ApplyResult | null>(null);
  const [tab, setTab] = useState("plan");

  // A diff is bound to the plan it was computed from. Touching the form
  // invalidates it, so apply is never reachable from a diff nobody saw.
  const reset = () => {
    setPlan(null);
    setDiff(null);
    setApplied(null);
  };
  const setArea = (k: keyof Form) => (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    setForm({ ...form, [k]: e.target.value });
    reset();
  };
  const set = (k: keyof Form) => (e: React.ChangeEvent<HTMLInputElement>) => {
    if (k === "release") setReleaseEdited(true);
    setForm({ ...form, [k]: e.target.value });
    reset();
  };
  const toggle = (k: keyof Form) => (v: boolean) => {
    setForm({ ...form, [k]: v });
    reset();
  };

  const planM = useMutation({
    mutationFn: () => deployApi.plan(request(name, version, variantId, { ...form, release })),
    onSuccess: (p) => {
      setPlan(p);
      setDiff(null);
    },
  });
  const diffM = useMutation({
    mutationFn: () => deployApi.diff({ planHash: plan!.hash } as never),
    onSuccess: setDiff,
  });
  const install = diff ? !diff.exists : false;

  const applyM = useMutation({
    mutationFn: () =>
      diff!.exists
        ? deployApi.apply(plan!.hash, diff!.revision)
        : deployApi.install(plan!.hash),
    onSuccess: (r) => {
      setApplied(r);
      setTab("status");
    },
  });

  if (model.isPending || cluster.isPending) return <Loading what="the model" />;
  if (model.error) return <ErrorState what={name} error={model.error} />;

  const entry = model.data.entry;
  const variant = entry.variants.find((v) => v.id === variantId) ?? entry.variants[0];

  if (!cluster.data?.allowDeploy) {
    return (
      <div className="space-y-4">
        <Back name={name} />
        <Card>
          <CardContent className="flex items-start gap-3 p-4 text-sm">
            <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
            <div>
              <div className="font-medium">This swissd is read-only</div>
              <p className="mt-1 text-muted-foreground">
                Set <code>server.allowDeploy</code> and grant <code>rbac.allowDeploy</code>, then
                restart. Deploying from here does nothing until both are on.
              </p>
            </div>
          </CardContent>
        </Card>
      </div>
    );
  }

  const error = planM.error ?? diffM.error ?? applyM.error;

  return (
    <div className="space-y-5">
      <Back name={name} />

      <div>
        <h1 className="text-xl font-semibold">Deploy {entry.displayName ?? entry.name}</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          <Badge variant="outline">{variant.id}</Badge>{" "}
          <Badge variant="muted">v{entry.version}</Badge>{" "}
          {variant.engine} · {variant.requires.gpus} GPU
          {variant.requires.nodes && variant.requires.nodes > 1
            ? ` × ${variant.requires.nodes} nodes`
            : ""}{" "}
          · chart {variant.chart.name}-{variant.chart.version}
        </p>
      </div>

      <Tabs
        tabs={[
          { id: "plan", label: "Plan" },
          { id: "diff", label: "Diff", disabled: !plan, hint: "compose a plan first" },
          { id: "install", label: install ? "Install" : "Apply", disabled: !diff, hint: "diff first" },
          { id: "status", label: "Status", disabled: !applied && !diff?.exists, hint: "nothing deployed yet" },
        ]}
        active={tab}
        onSelect={setTab}
      />

      {error && <ErrorState what="the request" error={error} />}

      {/* The settings follow the operator through diff, apply and status. The
          Plan tab shows the form itself, so it needs no second copy. */}
      {tab !== "plan" && <FormSummary form={form} release={release} cluster={cluster.data} />}

      {tab === "plan" && (
        <div className="space-y-5">
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
            <Input value={form.serviceId} onChange={set("serviceId")} placeholder={name} />
          </Field>
          <Field
            label="Release"
            hint={releaseEdited ? "helm release name" : "follows the service ID"}
          >
            <Input value={release} onChange={set("release")} placeholder={form.serviceId} />
          </Field>
          <Field label="Namespace" hint={`defaults to ${cluster.data.namespace ?? "the profile's"}`}>
            <Input value={form.namespace} onChange={set("namespace")} />
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
                Any values key, applied after every layer — scheduling and resources the
                fields above do not cover, and catalog or site keys the form refuses. It is
                the one input exempt from layer ownership, so nothing here is checked against
                the layer that owns it. Everything it sets is shown as its own{" "}
                <span className="font-medium">edit</span> layer in the composed plan below, so
                it is never invisible.
              </p>
            </CardHeader>
            <CardContent>
              <textarea
                value={form.edits}
                onChange={setArea("edits")}
                spellCheck={false}
                rows={10}
                placeholder={EDITS_PLACEHOLDER}
                className="w-full rounded-md border bg-background px-3 py-2 font-mono text-xs"
              />
            </CardContent>
          </Card>

          <div className="flex flex-wrap items-center gap-2">
            <Button onClick={() => planM.mutate()} disabled={!form.serviceId || planM.isPending}>
              {planM.isPending ? "Composing…" : "Compose plan"}
            </Button>
            {plan && <span className="font-mono text-xs text-muted-foreground">{plan.hash}</span>}
          </div>

          {plan && (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Composed plan</CardTitle>
              </CardHeader>
              <CardContent>
                <Provenance plan={plan} />
              </CardContent>
            </Card>
          )}
        </div>
      )}

      {tab === "diff" && plan && (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-2">
            <Button onClick={() => diffM.mutate()} disabled={diffM.isPending}>
              {diffM.isPending ? "Diffing…" : diff ? "Diff again" : "Diff"}
            </Button>
            {diff && (
              <span className="text-xs text-muted-foreground">
                {diff.exists ? `against live revision ${diff.revision}` : "release does not exist yet"}
              </span>
            )}
          </div>

          {diff && (
            <Card>
              <CardHeader>
                <CardTitle className="flex flex-wrap items-center gap-2 text-base">
                  Diff
                  {diff.changed ? (
                    <Badge variant="warning">changes</Badge>
                  ) : (
                    <Badge variant="success">no changes</Badge>
                  )}
                </CardTitle>
              </CardHeader>
              <CardContent>
                {diff.output.trim() ? (
                  <DiffView output={diff.output} />
                ) : (
                  <p className="text-sm text-muted-foreground">Nothing would change.</p>
                )}
              </CardContent>
            </Card>
          )}
        </div>
      )}

      {tab === "install" && plan && diff && (
        <div className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">{install ? "Install" : "Apply"}</CardTitle>
              <p className="text-sm text-muted-foreground">
                {install
                  ? "The release does not exist; this creates it."
                  : `Upgrading the live release, asserting it is still at revision ${diff.revision}.`}
              </p>
            </CardHeader>
            <CardContent className="space-y-3">
              <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[10rem_1fr]">
                <dt className="text-muted-foreground">Release</dt>
                <dd>{plan.release.namespace}/{plan.release.name}</dd>
                <dt className="text-muted-foreground">Chart</dt>
                <dd>{plan.chart.name}-{plan.chart.version}</dd>
                <dt className="text-muted-foreground">Model</dt>
                <dd>{plan.source.model} v{plan.source.version}</dd>
                <dt className="text-muted-foreground">Plan</dt>
                <dd className="font-mono text-xs break-all">{plan.hash}</dd>
              </dl>
              <Button onClick={() => applyM.mutate()} disabled={applyM.isPending || !!applied}>
                {applyM.isPending ? "Submitting…" : install ? "Install" : "Approve and apply"}
              </Button>
            </CardContent>
          </Card>

          {applied && <Applied result={applied} />}
        </div>
      )}

      {tab === "status" && (
        <ReleaseStatus
          namespace={plan?.release.namespace ?? cluster.data.namespace ?? ""}
          release={plan?.release.name ?? release}
        />
      )}
    </div>
  );
}

// FormSummary keeps the deploy settings in front of the operator on every tab.
// Read-only on purpose: a diff is bound to the plan it was computed from, so an
// editable copy here would either invalidate the diff the apply button is gated
// on, or -- worse -- not invalidate it and approve something else.
function FormSummary({
  form,
  release,
  cluster,
}: {
  form: Form;
  release: string;
  cluster?: ClusterInfo;
}) {
  const on = effective(form);
  const features: [string, boolean][] = [
    ["CART", form.cart],
    ["ModelRoute", on.modelRoute],
    ["SLO", form.slo],
    ["ServiceMonitor", form.serviceMonitor],
    ["Scaler", on.scaler],
  ];

  return (
    <Card>
      <CardContent className="space-y-3 p-4">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
            Deploy settings
          </span>
          <span className="text-xs text-muted-foreground">edit them on the Plan tab</span>
        </div>

        <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[9rem_1fr]">
          <Summary label="Service ID" value={form.serviceId} />
          <Summary label="Release" value={release} />
          <Summary label="Namespace" value={form.namespace || cluster?.namespace} />
          <Summary label="Route" value={on.modelRoute ? form.route || form.serviceId : ""} />
          <Summary label="Replicas" value={form.replicaCount} />
          <Summary
            label="Scaler"
            value={on.scaler ? `${form.minReplicas || "?"} – ${form.maxReplicas || "?"}` : ""}
          />
          <Summary label="Model path" value={form.localPath} />
          <Summary label="Priority class" value={form.priorityClassName} />
          <Summary label="Scheduler" value={form.schedulerName} />
        </dl>

        <div className="flex flex-wrap gap-1">
          {features.map(([label, enabled]) => (
            <Badge key={label} variant={enabled ? "success" : "muted"}>
              {label} {enabled ? "on" : "off"}
            </Badge>
          ))}
          {form.edits.trim() && <Badge variant="warning">plan edits</Badge>}
        </div>
      </CardContent>
    </Card>
  );
}

// An unset field is left out rather than rendered as a dash: the list is read at
// a glance, and nine "—" rows bury the three settings that were actually made.
function Summary({ label, value }: { label: string; value?: string }) {
  if (!value?.trim()) return null;
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="break-all">{value}</dd>
    </>
  );
}

function Applied({ result }: { result: ApplyResult }) {
  return (
    <Card>
      <CardContent className="flex items-start gap-3 p-4 text-sm">
        <CircleCheck className="mt-0.5 size-4 shrink-0 text-success" />
        <div className="space-y-1">
          <div className="font-medium">
            {result.release} submitted at revision {result.revision}
          </div>
          <p className="text-muted-foreground">
            Not waiting: a cold load takes 20–40 minutes, and helm cannot tell loading from
            broken. Watch the pods.
          </p>
          {result.statusError && (
            <p className="text-warning">
              The release is live but its status was not updated: {result.statusError}
            </p>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function Back({ name }: { name: string }) {
  return (
    <Link
      to={`/catalog/${encodeURIComponent(name)}`}
      className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
    >
      <ChevronLeft className="size-4" /> {name}
    </Link>
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
// on. Derived in one place so the toggle, the summary and the request cannot
// disagree about what this deploy is asking for.
function effective(f: Form) {
  return {
    modelRoute: f.modelRoute || f.route.trim() !== "",
    scaler: num(f.minReplicas) !== undefined || num(f.maxReplicas) !== undefined,
  };
}

function request(model: string, version: string, variant: string, f: Form): PlanRequest {
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
    model,
    version: version || undefined,
    editsYAML: f.edits.trim() || undefined,
    variant: variant || undefined,
    release: f.release.trim() || undefined,
    namespace: f.namespace.trim() || undefined,
    serviceId: f.serviceId.trim() || undefined,
    localPath: f.localPath.trim() || undefined,
    overrides,
  };
}
