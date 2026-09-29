import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronLeft, TriangleAlert } from "lucide-react";
import { api, deployApi, type ApplyResult, type DiffResult, type Plan } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { DeploySettings, EMPTY, imageOf, planRequest, type Form } from "@/components/DeploySettings";
import { Pipeline } from "@/components/Pipeline";
import { ErrorState, Loading } from "@/components/States";
import { CatalogBadge, CatalogGate, useCatalogChoice, withCatalog } from "@/components/CatalogChoice";

export function Deploy() {
  const { name = "" } = useParams();
  const [params] = useSearchParams();
  const variantId = params.get("variant") ?? "";
  const version = params.get("version") ?? "";
  // The catalog is chosen before anything is composed: the same model name in
  // two catalogs is two different models.
  const choice = useCatalogChoice();
  const catalog = choice.selected;

  const model = useQuery({
    queryKey: ["model", catalog, name, version],
    queryFn: () => api.model(name, version || undefined, catalog),
    enabled: !!catalog,
  });
  const cluster = useQuery({ queryKey: ["cluster"], queryFn: api.cluster });
  const nodes = useQuery({ queryKey: ["nodes"], queryFn: api.nodes });

  const [form, setForm] = useState<Form>({ ...EMPTY, serviceId: name });
  // serviceId is the identity everything else is named after: the helm release,
  // the openresty route, the LLMScaler and the LLMSLORequirement.
  const release = form.serviceId;

  const [plan, setPlan] = useState<Plan | null>(null);
  const [diff, setDiff] = useState<DiffResult | null>(null);
  const [applied, setApplied] = useState<ApplyResult | null>(null);
  const [tab, setTab] = useState("plan");

  // A diff is bound to the plan it was computed from, so touching the form
  // invalidates both, and the pipeline walks back to Plan rather than leaving a
  // pane open over a plan that no longer exists.
  const update = (patch: Partial<Form>) => {
    setForm({ ...form, ...patch });
    setPlan(null);
    setDiff(null);
    setApplied(null);
    setTab((t) => (t === "status" ? t : "plan"));
  };

  const planM = useMutation({
    mutationFn: () =>
      deployApi.plan(planRequest(form, { model: name, version, variant: variantId, catalog })),
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

  if (choice.isPending) return <Loading what="catalogs" />;
  if (!catalog) {
    return <CatalogGate catalogs={choice.catalogs} named={choice.named} choose={choice.choose} what={`deploy ${name} from`} />;
  }
  if (model.isPending || cluster.isPending) return <Loading what="the model" />;
  if (model.error) return <ErrorState what={name} error={model.error} />;

  const entry = model.data.entry;
  const variant = entry.variants.find((v) => v.id === variantId) ?? entry.variants[0];

  if (!cluster.data?.allowDeploy) {
    return (
      <div className="space-y-4">
        <Back name={name} catalog={catalog} />
        <ReadOnly />
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <Back name={name} catalog={catalog} />

      <div>
        <h1 className="flex flex-wrap items-center gap-2 text-xl font-semibold">
          Deploy {entry.displayName ?? entry.name}
          <CatalogBadge name={catalog} show={choice.several} />
        </h1>
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
        form={form}
        onChange={update}
        cluster={cluster.data}
        serviceIdPlaceholder={name}
        localPathDefault={model.data.localPath}
        localPathPlaceholder={model.data.pathTemplate}
        image={imageOf(variant, model.data.imageRepository)}
        supportedGPUs={variant?.requires.gpuProduct}
        clusterGPUs={nodes.data?.nodes.map((n) => n.GPUProduct)}
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

function Back({ name, catalog }: { name: string; catalog: string }) {
  return (
    <Link
      to={withCatalog(`/catalog/${encodeURIComponent(name)}`, catalog)}
      className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
    >
      <ChevronLeft className="size-4" /> {name}
    </Link>
  );
}
