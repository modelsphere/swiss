import { Link, useParams, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { ChartColumn, ChevronLeft, ExternalLink } from "lucide-react";
import { api, type Node, type Variant } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import {
  type Kind,
  comparison,
  formatUplift,
  httpLink,
  reportLink,
  UPLIFT_HELP,
  variantKind,
  workloadSummary,
} from "@/lib/catalog";
import { gpuCount, matchesVendor, vendorLabel } from "@/lib/gpu";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorState, Loading } from "@/components/States";
import { WorkloadList } from "@/components/Uplift";
import { CatalogBadge, CatalogGate, useCatalogChoice, withCatalog } from "@/components/CatalogChoice";
import { cn } from "@/lib/utils";

export function Model() {
  const { name = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const version = params.get("version") ?? "";
  // A model belongs to one catalog: the same name in another is another model.
  const choice = useCatalogChoice();
  const selected = choice.selected;
  const model = useQuery({
    queryKey: ["model", selected, name, version],
    queryFn: () => api.model(name, version || undefined, selected),
    enabled: !!selected,
  });
  const catalog = useQuery({
    queryKey: ["catalog", selected],
    queryFn: () => api.catalog(selected),
    enabled: !!selected,
  });
  // Node facts are a separate, non-blocking query: a variant list is still
  // worth showing when the fit check cannot be computed.
  const nodes = useQuery({ queryKey: ["nodes"], queryFn: api.nodes });

  if (choice.isPending) return <Loading what="catalogs" />;
  if (!selected) {
    return <CatalogGate catalogs={choice.catalogs} named={choice.named} choose={choice.choose} what={`look up ${name} in`} />;
  }
  if (model.isPending) return <Loading what={name} />;
  if (model.error) return <ErrorState what={name} error={model.error} />;

  const e = model.data.entry;
  const indexed = catalog.data?.index.models.find((m) => m.name === name);
  // Roles and the uplift come from the model's recorded tuning, via the index.
  // The entry itself carries none: it is metadata, not part of the version.
  const tuning = indexed?.tuning;
  const cmp = comparison(e.variants, e.version, tuning);
  const report = cmp ? reportLink(catalog.data?.index.site, e.name, cmp.report) : undefined;
  return (
    <div className="space-y-5">
      <Link
        to={withCatalog("/catalog", selected)}
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ChevronLeft className="size-4" /> Catalog
      </Link>

      <div>
        <h1 className="flex flex-wrap items-center gap-2 text-xl font-semibold">
          {e.displayName ?? e.name}
          <CatalogBadge name={selected} show={choice.several} />
        </h1>
        {e.description && <p className="mt-1 max-w-3xl text-sm text-muted-foreground">{e.description}</p>}
      </div>

      <dl className="grid gap-x-8 gap-y-2 text-sm sm:grid-cols-2">
        <Field label="Weights" value={`${e.source.hf}${e.source.sizeGiB ? ` · ${e.source.sizeGiB} GiB` : ""}`} />
        <Field label="Served as" value={e.servedName ?? e.name} />
        {e.license && <Field label="License" value={e.license} />}
        <Field label="Version" value={e.version} />
        {e.digest && <Field label="Digest" value={e.digest.slice(0, 19)} />}
      </dl>

      <VersionPicker
        name={name}
        current={e.version}
        versions={
          indexed?.versions.map((v) => v.version) ?? []
        }
        onPick={(v) =>
          setParams((prev) => {
            const next = new URLSearchParams(prev);
            if (v) next.set("version", v);
            else next.delete("version");
            return next;
          })
        }
      />

      <div>
        <h2 className="mb-2 text-sm font-medium">Variants</h2>
        <div className="grid gap-3 md:grid-cols-2">
          {e.variants.map((v) => (
            <VariantCard
              key={v.id}
              v={v}
              kind={variantKind(v, tuning)}
              uplift={
                cmp?.optimized === v.id && cmp.uplift != null
                  ? formatUplift(cmp.uplift) + (cmp.version !== e.version ? ` on v${cmp.version}` : "")
                  : undefined
              }
              upliftTitle={cmp?.optimized === v.id && cmp.workloads.length ? workloadSummary(cmp) : undefined}
              report={cmp?.optimized === v.id ? report : undefined}
              workloads={cmp?.optimized === v.id && cmp.workloads.length ? cmp.workloads : undefined}
              nodes={nodes.data?.nodes}
              model={e.name}
              version={version}
              catalog={selected}
            />
          ))}
        </div>
      </div>
    </div>
  );
}

// A variant is a hardware and parallelism decision, so the card shows whether
// this cluster can actually run it. Surfacing the fit check at selection time
// beats discovering it at apply time, and far beats discovering it as an OOM
// forty minutes into a load.
function VersionPicker({
  name,
  current,
  versions,
  onPick,
}: {
  name: string;
  current: string;
  versions: string[];
  onPick: (v: string) => void;
}) {
  if (versions.length < 2) return null;
  return (
    <div className="flex items-center gap-2 text-sm">
      <span className="text-muted-foreground">Version</span>
      <select
        className="rounded-md border bg-background px-2 py-1 text-sm"
        value={current}
        onChange={(e) => onPick(e.target.value)}
        aria-label={`version of ${name}`}
      >
        {versions.map((v) => (
          <option key={v} value={v}>
            {v}
          </option>
        ))}
      </select>
      <span className="text-xs text-muted-foreground">
        a deploy records the version and its digest
      </span>
    </div>
  );
}

function VariantCard({
  v,
  kind,
  uplift,
  upliftTitle,
  report,
  workloads,
  nodes,
  model,
  version,
  catalog,
}: {
  v: Variant;
  kind: Kind;
  // The headline, "+58%", with "on v1.0.0" when measured on another version.
  // Which workload it is for is on the line below, with the others.
  uplift?: string;
  upliftTitle?: string;
  // The tuned variant's perf report, on the catalog's site.
  report?: string;
  workloads?: { name: string; uplift: number }[];
  nodes?: Node[];
  model: string;
  version: string;
  catalog: string;
}) {
  const fit = fitness(v, nodes);
  const link = httpLink(v.link);
  return (
    <Card className="flex h-full flex-col">
      <CardHeader className="flex-row items-start justify-between gap-3">
        <div className="space-y-1">
          <CardTitle className="flex flex-wrap items-center gap-2 text-base">
            {v.id}
            {v.default && <Badge variant="muted">default</Badge>}
            {kind.optimized && (
              <Badge variant="success" title={upliftTitle ? `${upliftTitle}. ${UPLIFT_HELP}` : UPLIFT_HELP}>
                optimized{uplift && ` ${uplift}`}
              </Badge>
            )}
            {kind.baseline && <Badge variant="outline">baseline</Badge>}
            <Badge variant="outline">{v.engine}</Badge>
          </CardTitle>
          {workloads && (
            <p className="text-xs leading-6 text-muted-foreground" title={UPLIFT_HELP}>
              vs baseline: <WorkloadList workloads={workloads} />
            </p>
          )}
          {v.description && <p className="text-sm text-muted-foreground">{v.description}</p>}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {report && (
            <a
              href={report}
              target="_blank"
              rel="noreferrer"
              className={cn(buttonVariants({ variant: "outline", size: "sm" }), "gap-1.5")}
            >
              <ChartColumn className="size-3.5 text-muted-foreground" />
              <span>Report</span>
            </a>
          )}
          {link && (
            <a
              href={link}
              target="_blank"
              rel="noreferrer"
              className={cn(buttonVariants({ variant: "outline", size: "sm" }), "gap-1.5")}
            >
              <ExternalLink className="size-3.5 text-muted-foreground" />
              <span>Docs</span>
            </a>
          )}
        </div>
      </CardHeader>
      <CardContent className="flex flex-1 flex-col gap-2 text-sm">
        <div className="text-muted-foreground">
          {gpuCount(v.requires)}
          {" · "}
          {vendorLabel(v.requires.vendor)}
          {" · "}
          {v.requires.topology ?? "single-node"}
          {v.requires.rdma && " · RDMA"}
          {" · "}
          chart {v.chart.name}-{v.chart.version}
        </div>
        <div className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
          Runs on
          {v.requires.gpuProduct?.length ? (
            v.requires.gpuProduct.map((p) => (
              <Badge key={p} variant="outline">{p}</Badge>
            ))
          ) : (
            <Badge variant="outline">any {vendorLabel(v.requires.vendor)}</Badge>
          )}
        </div>
        <div className="mt-auto flex flex-wrap items-center justify-between gap-2 border-t pt-3">
          {fit && <Badge variant={fit.ok ? "success" : "warning"}>{fit.text}</Badge>}
          <Link
            to={withCatalog(
              `/deploy/${encodeURIComponent(model)}?variant=${encodeURIComponent(v.id)}` +
                (version ? `&version=${encodeURIComponent(version)}` : ""),
              catalog,
            )}
            className="ml-auto"
          >
            <Button size="sm">Deploy</Button>
          </Link>
        </div>
      </CardContent>
    </Card>
  );
}

function fitness(v: Variant, nodes?: Node[]): { ok: boolean; text: string } | null {
  if (!nodes) return null;
  const matching = nodes.filter(
    (n) =>
      n.Schedulable &&
      n.GPUs >= v.requires.gpus &&
      matchesVendor(v.requires.vendor, n) &&
      (!v.requires.gpuProduct?.length || v.requires.gpuProduct.includes(n.GPUProduct)),
  );
  const needed = v.requires.nodes ?? 1;
  return matching.length >= needed
    ? { ok: true, text: `${matching.length} matching node${matching.length === 1 ? "" : "s"}` }
    : { ok: false, text: `needs ${needed}, ${matching.length} matching` };
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="font-medium">{value}</dd>
    </div>
  );
}
