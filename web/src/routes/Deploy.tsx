import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronLeft, CircleCheck, TriangleAlert } from "lucide-react";
import { api, deployApi, type ApplyResult, type DiffResult, type Plan, type PlanRequest } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { DiffView } from "@/components/DiffView";
import { Provenance } from "@/components/Provenance";
import { ErrorState, Loading } from "@/components/States";

interface Form {
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

  const [form, setForm] = useState<Form>({ ...EMPTY, release: name, serviceId: name });
  const [plan, setPlan] = useState<Plan | null>(null);
  const [diff, setDiff] = useState<DiffResult | null>(null);
  const [applied, setApplied] = useState<ApplyResult | null>(null);

  // A diff is bound to the plan it was computed from. Touching the form
  // invalidates it, so apply is never reachable from a diff nobody saw.
  const set = (k: keyof Form) => (e: React.ChangeEvent<HTMLInputElement>) => {
    setForm({ ...form, [k]: e.target.value });
    setPlan(null);
    setDiff(null);
    setApplied(null);
  };

  const planM = useMutation({
    mutationFn: () => deployApi.plan(request(name, version, variantId, form)),
    onSuccess: (p) => {
      setPlan(p);
      setDiff(null);
    },
  });
  const diffM = useMutation({
    mutationFn: () => deployApi.diff({ planHash: plan!.hash } as never),
    onSuccess: setDiff,
  });
  const applyM = useMutation({
    mutationFn: () =>
      diff!.exists
        ? deployApi.apply(plan!.hash, diff!.revision)
        : deployApi.install(plan!.hash),
    onSuccess: setApplied,
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

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Deploy settings</CardTitle>
          <p className="text-sm text-muted-foreground">
            Engine flags, probes and the image come from the catalog and are not editable here.
            To change those, pick another variant or move the catalog reference.
          </p>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <Field label="Release" hint="helm release name">
            <Input value={form.release} onChange={set("release")} placeholder={name} />
          </Field>
          <Field label="Namespace" hint={`defaults to ${cluster.data.namespace ?? "the profile's"}`}>
            <Input value={form.namespace} onChange={set("namespace")} />
          </Field>
          <Field label="Service ID" hint="modelRoute, sloRequirement and the scaler key off this">
            <Input value={form.serviceId} onChange={set("serviceId")} />
          </Field>
          <Field label="Route" hint="openresty route name; empty leaves modelRoute alone">
            <Input value={form.route} onChange={set("route")} />
          </Field>
          <Field label="Replicas" hint="fixed count; leave empty when the scaler owns it">
            <Input value={form.replicaCount} onChange={set("replicaCount")} inputMode="numeric" />
          </Field>
          <Field label="Model path" hint="overrides the site's path template">
            <Input value={form.localPath} onChange={set("localPath")} />
          </Field>
          <Field label="Scaler min">
            <Input value={form.minReplicas} onChange={set("minReplicas")} inputMode="numeric" />
          </Field>
          <Field label="Scaler max">
            <Input value={form.maxReplicas} onChange={set("maxReplicas")} inputMode="numeric" />
          </Field>
        </CardContent>
      </Card>

      {error && <ErrorState what="the request" error={error} />}

      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={() => planM.mutate()} disabled={!form.release || planM.isPending}>
          {planM.isPending ? "Composing…" : "Compose plan"}
        </Button>
        <Button variant="outline" onClick={() => diffM.mutate()} disabled={!plan || diffM.isPending}>
          {diffM.isPending ? "Diffing…" : "Diff"}
        </Button>
        <Button onClick={() => applyM.mutate()} disabled={!diff || applyM.isPending || !!applied}>
          {applyM.isPending ? "Submitting…" : diff?.exists ? "Apply" : "Install"}
        </Button>
        {plan && !diff && (
          <span className="text-xs text-muted-foreground">Diff before applying.</span>
        )}
      </div>

      {applied && <Applied result={applied} />}

      {diff && !applied && (
        <Card>
          <CardHeader>
            <CardTitle className="flex flex-wrap items-center gap-2 text-base">
              Diff
              {diff.changed ? (
                <Badge variant="warning">changes</Badge>
              ) : (
                <Badge variant="success">no changes</Badge>
              )}
              {diff.exists && (
                <span className="text-xs font-normal text-muted-foreground">
                  live revision {diff.revision}
                </span>
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

      {plan && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Composed values</CardTitle>
            <p className="font-mono text-xs text-muted-foreground">{plan.hash}</p>
          </CardHeader>
          <CardContent>
            <Provenance plan={plan} />
          </CardContent>
        </Card>
      )}
    </div>
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

function request(model: string, version: string, variant: string, f: Form): PlanRequest {
  const overrides: Record<string, unknown> = {};
  const num = (v: string) => (v.trim() === "" ? undefined : Number(v));

  if (num(f.replicaCount) !== undefined) overrides.replicaCount = num(f.replicaCount);
  const scaler: Record<string, unknown> = {};
  if (num(f.minReplicas) !== undefined) scaler.minReplicas = num(f.minReplicas);
  if (num(f.maxReplicas) !== undefined) scaler.maxReplicas = num(f.maxReplicas);
  if (Object.keys(scaler).length) overrides.scaler = scaler;
  if (f.route.trim()) overrides.modelRoute = { enabled: true, nginx: { route: f.route.trim() } };

  return {
    model,
    version: version || undefined,
    variant: variant || undefined,
    release: f.release.trim() || undefined,
    namespace: f.namespace.trim() || undefined,
    serviceId: f.serviceId.trim() || undefined,
    localPath: f.localPath.trim() || undefined,
    overrides: Object.keys(overrides).length ? overrides : undefined,
  };
}
