import { useEffect, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowRight, ChevronLeft, TriangleAlert } from "lucide-react";
import { api, deployApi, type ApplyResult, type DiffResult, type Plan } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/input";
import {
  DeploySettings,
  EMPTY,
  formFromPlan,
  imageOf,
  planRequest,
  type Form,
} from "@/components/DeploySettings";
import { Pipeline } from "@/components/Pipeline";
import { ErrorState, Loading } from "@/components/States";

export function Upgrade() {
  const { namespace = "", release = "" } = useParams();
  // A rollback is an upgrade whose plan is not composed: it comes back out of
  // the cluster exactly as it ran. One page, because the operator's question is
  // the same one either way -- what does this do to the live release -- and two
  // pages would be two chances to answer it differently.
  const [params] = useSearchParams();
  const rollbackTo = Number(params.get("rollback")) || 0;
  const [version, setVersion] = useState("");
  const [variant, setVariant] = useState("");

  const current = useQuery({
    queryKey: ["release-plan", namespace, release],
    queryFn: () => api.releasePlan(namespace, release),
  });
  // The plan that produced the revision being rolled back to, read from its
  // archive rather than recomposed: going back to what worked must not mean
  // going to whatever the catalog says that version is today. Archives are
  // immutable, so what is read is kept.
  const archived = useQuery({
    queryKey: ["revision-plan", namespace, release, rollbackTo],
    queryFn: () => api.revisionPlan(namespace, release, rollbackTo),
    enabled: rollbackTo > 0,
    staleTime: Infinity,
  });
  const catalog = useQuery({ queryKey: ["catalog"], queryFn: api.catalog });
  const cluster = useQuery({ queryKey: ["cluster"], queryFn: api.cluster });
  const nodes = useQuery({ queryKey: ["nodes"], queryFn: api.nodes });
  // For the model path default, which is the site's template resolved against
  // this model's hf -- the same value the deploy page shows.
  const entry = useQuery({
    queryKey: ["model", current.data?.source.model ?? "", version],
    queryFn: () => api.model(current.data!.source.model, version || undefined),
    enabled: !!current.data,
  });

  const [form, setForm] = useState<Form>(EMPTY);
  const [plan, setPlan] = useState<Plan | null>(null);
  const [diff, setDiff] = useState<DiffResult | null>(null);
  const [applied, setApplied] = useState<ApplyResult | null>(null);
  const [tab, setTab] = useState("plan");

  // Seed the form from what the release was deployed with, once -- or, on a
  // rollback, from the revision being restored, which is what would come back.
  // An upgrade whose form starts empty would silently propose dropping every
  // setting.
  useEffect(() => {
    const from = rollbackTo ? archived.data : current.data;
    if (from) setForm(formFromPlan(from));
  }, [current.data, archived.data, rollbackTo]);

  const reset = () => {
    setPlan(null);
    setDiff(null);
    setApplied(null);
    setTab((t) => (t === "status" ? t : "plan"));
  };
  const update = (patch: Partial<Form>) => {
    setForm({ ...form, ...patch });
    reset();
  };

  const planM = useMutation({
    mutationFn: () =>
      deployApi.plan(
        planRequest(form, {
          model: current.data!.source.model,
          version: version || undefined,
          variant: variant || undefined,
          // The server carries forward anything the form does not cover, so a
          // value set once from a flag survives the upgrade instead of being
          // dropped by a form that never knew about it.
          fromRelease: release,
        }),
      ),
    // The previous plan is superseded the moment a recompose starts. Dropping
    // it here rather than on the way back means a failed compose leaves
    // nothing to act on, instead of a stale plan the error message sits behind.
    onMutate: () => {
      setPlan(null);
      setDiff(null);
    },
    onSuccess: (p) => {
      setPlan(p);
      setDiff(null);
    },
  });

  if (current.isPending) return <Loading what={release} />;
  if (current.error) return <ErrorState what={`the plan for ${release}`} error={current.error} />;
  if (rollbackTo > 0) {
    if (archived.isPending) return <Loading what={`revision ${rollbackTo}`} />;
    if (archived.error) {
      return <ErrorState what={`the plan for revision ${rollbackTo}`} error={archived.error} />;
    }
  }

  const cur = current.data;
  // What the pipeline acts on: a composed plan on an upgrade, the archive on a
  // rollback. Nothing else about the page differs.
  const target = rollbackTo ? (archived.data ?? null) : plan;
  const model = catalog.data?.index.models.find((m) => m.name === cur.source.model);
  const versions = model?.versions.map((v) => v.version) ?? [];
  const variants = model?.versions.find((v) => v.version === (version || model.latest))?.variants ?? [];

  if (!cluster.data?.allowDeploy) {
    return (
      <div className="space-y-4">
        <Back namespace={namespace} />
        <Card>
          <CardContent className="flex items-start gap-3 p-4 text-sm">
            <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
            <div>This swissd is read-only; upgrades and rollbacks are disabled.</div>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <Back namespace={namespace} />

      <div>
        <h1 className="text-xl font-semibold">
          {rollbackTo ? `Roll back ${release} to revision ${rollbackTo}` : `Upgrade ${release}`}
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          <Badge variant="outline">{namespace}</Badge> {cur.source.model}
          {cur.source.version && ` v${cur.source.version}`} · {cur.source.variant} · chart{" "}
          {cur.chart.name}-{cur.chart.version}
        </p>
        {rollbackTo > 0 && (
          <p className="mt-1 text-sm text-muted-foreground">
            That is what is deployed now. Everything below is revision {rollbackTo}, as it ran
            — a rollback re-applies it forward as a new revision.
          </p>
        )}
      </div>

      {planM.error && <ErrorState what="the request" error={planM.error} />}

      {!rollbackTo && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Catalog target</CardTitle>
            <p className="text-sm text-muted-foreground">
              The model version and variant this upgrade composes against. Engine flags, probes
              and the image move with them — that is what an upgrade is for.
            </p>
          </CardHeader>
          <CardContent className="grid gap-4 sm:grid-cols-2">
            <Field label="Model version" hint={`deployed: ${cur.source.version ?? "unpinned"}`}>
              <Select
                value={version}
                onChange={(v) => {
                  setVersion(v);
                  reset();
                }}
                options={versions}
                emptyLabel="latest"
              />
            </Field>
            <Field label="Variant" hint={`deployed: ${cur.source.variant}`}>
              <Select
                value={variant}
                onChange={(v) => {
                  setVariant(v);
                  reset();
                }}
                options={variants.map((v) => v.id)}
                emptyLabel={`keep ${cur.source.variant}`}
              />
            </Field>
          </CardContent>
        </Card>
      )}

      {/* The same form as the deploy page, seeded from the release. Editing it
          here means the upgrade moves the catalog and the settings together,
          which is two changes in one diff -- so the diff is the thing that has
          to be read, and the pipeline below is the same one.

          On a rollback it is the same form with nothing editable: the archived
          plan is re-applied as it ran, so a field that could be typed into
          would promise a change the rollback would not make. */}
      <DeploySettings
        form={form}
        onChange={update}
        cluster={cluster.data}
        supportedGPUs={variants.find((v) => v.id === (variant || cur.source.variant))?.requires.gpuProduct}
        clusterGPUs={nodes.data?.nodes.map((n) => n.GPUProduct)}
        localPathDefault={entry.data?.localPath}
        localPathPlaceholder={entry.data?.pathTemplate}
        image={imageOf(
          entry.data?.entry.variants.find((v) => v.id === (variant || cur.source.variant)),
          entry.data?.imageRepository,
        )}
        lockIdentity
        readOnly={rollbackTo > 0}
      />

      <Pipeline
        namespace={namespace}
        release={release}
        plan={target}
        rollbackTo={rollbackTo || undefined}
        diff={diff}
        applied={applied}
        tab={tab}
        onTab={setTab}
        onCompose={() => planM.mutate()}
        composing={planM.isPending}
        composeLabel={planM.isPending ? "Composing…" : plan ? "Recompose upgrade" : "Compose upgrade"}
        onDiff={setDiff}
        onApplied={setApplied}
      >
        {target && <WhatMoves current={cur} proposed={target} rollback={rollbackTo > 0} />}
      </Pipeline>
    </div>
  );
}

// WhatMoves names the catalog change before anything else on the Plan tab. The
// settings above are editable now, so this is also where an operator sees that
// an upgrade they meant as a version bump is carrying a form change with it.
//
// A rollback reads the same table in the other direction: from what is running
// to what is coming back.
function WhatMoves({
  current,
  proposed,
  rollback,
}: {
  current: Plan;
  proposed: Plan;
  rollback?: boolean;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">
          {rollback ? "What comes back" : "What moves"}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        <Change label="Model version" from={current.source.version} to={proposed.source.version} />
        <Change
          label="Chart"
          from={`${current.chart.name}-${current.chart.version}`}
          to={`${proposed.chart.name}-${proposed.chart.version}`}
        />
        <Change
          label="Entry digest"
          from={current.source.digest?.slice(7, 19)}
          to={proposed.source.digest?.slice(7, 19)}
        />
        <Change label="Variant" from={current.source.variant} to={proposed.source.variant} />
        {current.hash === proposed.hash && (
          <p className="pt-1 text-sm text-muted-foreground">
            Identical to what is deployed — {rollback ? "this revision is what is running." : "nothing to upgrade."}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function Change({ label, from, to }: { label: string; from?: string; to?: string }) {
  const same = from === to;
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <span className="w-32 shrink-0 text-muted-foreground">{label}</span>
      <span className="font-mono">{from || "—"}</span>
      {!same && (
        <>
          <ArrowRight className="size-3.5 text-muted-foreground" />
          <span className="font-mono font-medium text-success">{to || "—"}</span>
        </>
      )}
      {same && <Badge variant="muted">unchanged</Badge>}
    </div>
  );
}

function Select({
  value,
  onChange,
  options,
  emptyLabel,
}: {
  value: string;
  onChange: (v: string) => void;
  options: string[];
  emptyLabel: string;
}) {
  return (
    <select
      className="w-full rounded-md border bg-background px-3 py-2 text-sm"
      value={value}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">{emptyLabel}</option>
      {options.map((o) => (
        <option key={o} value={o}>
          {o}
        </option>
      ))}
    </select>
  );
}

function Back({ namespace }: { namespace: string }) {
  return (
    <Link
      to="/"
      className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
    >
      <ChevronLeft className="size-4" /> Deployments in {namespace}
    </Link>
  );
}
