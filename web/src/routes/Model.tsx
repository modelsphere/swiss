import { Link, useParams, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft, ExternalLink } from "lucide-react";
import { api, type Node, type Variant } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { gpuCount, vendorLabel } from "@/lib/gpu";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorState, Loading } from "@/components/States";

export function Model() {
  const { name = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const version = params.get("version") ?? "";
  const model = useQuery({
    queryKey: ["model", name, version],
    queryFn: () => api.model(name, version || undefined),
  });
  const catalog = useQuery({ queryKey: ["catalog"], queryFn: api.catalog });
  // Node facts are a separate, non-blocking query: a variant list is still
  // worth showing when the fit check cannot be computed.
  const nodes = useQuery({ queryKey: ["nodes"], queryFn: api.nodes });

  if (model.isPending) return <Loading what={name} />;
  if (model.error) return <ErrorState what={name} error={model.error} />;

  const e = model.data.entry;
  return (
    <div className="space-y-5">
      <Link to="/catalog" className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
        <ChevronLeft className="size-4" /> Catalog
      </Link>

      <div>
        <h1 className="text-xl font-semibold">{e.displayName ?? e.name}</h1>
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
          catalog.data?.index.models.find((m) => m.name === name)?.versions.map((v) => v.version) ?? []
        }
        onPick={(v) => setParams(v ? { version: v } : {})}
      />

      <div>
        <h2 className="mb-2 text-sm font-medium">Variants</h2>
        <div className="grid gap-3 md:grid-cols-2">
          {e.variants.map((v) => (
            <VariantCard
              key={v.id}
              v={v}
              nodes={nodes.data?.nodes}
              model={e.name}
              version={version}
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
  nodes,
  model,
  version,
}: {
  v: Variant;
  nodes?: Node[];
  model: string;
  version: string;
}) {
  const fit = fitness(v, nodes);
  return (
    <Card className="flex h-full flex-col">
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          {v.id}
          {v.default && <Badge variant="muted">default</Badge>}
          <Badge variant="outline">{v.engine}</Badge>
          {v.link && (
            <a
              href={v.link}
              target="_blank"
              rel="noreferrer"
              className="ml-auto inline-flex items-center gap-1 text-xs font-normal text-muted-foreground hover:text-foreground hover:underline"
            >
              <span>link</span>
              <ExternalLink className="size-3" />
            </a>
          )}
        </CardTitle>
        {v.description && <p className="text-sm text-muted-foreground">{v.description}</p>}
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
        <div className="mt-auto flex flex-wrap items-center gap-2 pt-2">
          {fit && <Badge variant={fit.ok ? "success" : "warning"}>{fit.text}</Badge>}
          <Link
            to={
              `/deploy/${encodeURIComponent(model)}?variant=${encodeURIComponent(v.id)}` +
              (version ? `&version=${encodeURIComponent(version)}` : "")
            }
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
