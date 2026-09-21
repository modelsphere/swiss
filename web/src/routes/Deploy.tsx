import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronLeft, TriangleAlert } from "lucide-react";
import { api, deployApi, type ApplyResult, type DiffResult, type Plan } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { DeploySettings, EMPTY, planRequest, type Form } from "@/components/DeploySettings";
import { Pipeline } from "@/components/Pipeline";
import { ErrorState, Loading } from "@/components/States";

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

  // A diff is bound to the plan it was computed from, so touching the form
  // invalidates both, and the pipeline walks back to Plan rather than leaving a
  // pane open over a plan that no longer exists.
  const update = (patch: Partial<Form>) => {
    if (patch.release !== undefined) setReleaseEdited(true);
    setForm({ ...form, ...patch });
    setPlan(null);
    setDiff(null);
    setApplied(null);
    setTab((t) => (t === "status" ? t : "plan"));
  };

  const planM = useMutation({
    mutationFn: () =>
      deployApi.plan(
        planRequest({ ...form, release }, { model: name, version, variant: variantId }),
      ),
    onSuccess: (p) => {
      setPlan(p);
      setDiff(null);
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
        <ReadOnly />
      </div>
    );
  }

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

      {planM.error && <ErrorState what="the request" error={planM.error} />}

      {/* The settings are the page. Plan, diff and apply are what is done with
          them, so they sit below as tabs rather than holding the form inside
          one of them. */}
      <DeploySettings
        form={{ ...form, release }}
        onChange={update}
        cluster={cluster.data}
        serviceIdPlaceholder={name}
        releaseHint={releaseEdited ? "helm release name" : "follows the service ID"}
      />

      <Pipeline
        namespace={form.namespace || cluster.data.namespace || ""}
        release={release}
        plan={plan}
        diff={diff}
        applied={applied}
        tab={tab}
        onTab={setTab}
        onCompose={() => planM.mutate()}
        composing={planM.isPending}
        composeDisabled={!form.serviceId}
        onDiff={setDiff}
        onApplied={setApplied}
      />
    </div>
  );
}

function ReadOnly() {
  return (
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
