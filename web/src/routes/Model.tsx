import { Link, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft } from "lucide-react";
import { api, type Node, type Variant } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorState, Loading } from "@/components/States";

export function Model() {
  const { name = "" } = useParams();
  const model = useQuery({ queryKey: ["model", name], queryFn: () => api.model(name) });
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
        <Field label="Catalog ref" value={model.data.ref.slice(0, 19)} />
      </dl>

      <div>
        <h2 className="mb-2 text-sm font-medium">Variants</h2>
        <div className="grid gap-3 md:grid-cols-2">
          {e.variants.map((v) => (
            <VariantCard key={v.id} v={v} nodes={nodes.data?.nodes} />
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
function VariantCard({ v, nodes }: { v: Variant; nodes?: Node[] }) {
  const fit = fitness(v, nodes);
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          {v.id}
          {v.default && <Badge variant="muted">default</Badge>}
          <Badge variant="outline">{v.engine}</Badge>
        </CardTitle>
        {v.description && <p className="text-sm text-muted-foreground">{v.description}</p>}
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        <div className="text-muted-foreground">
          {v.requires.nodes && v.requires.nodes > 1
            ? `${v.requires.gpus} GPU × ${v.requires.nodes} nodes`
            : `${v.requires.gpus} GPU`}
          {" · "}
          {v.requires.topology ?? "single-node"}
          {v.requires.rdma && " · RDMA"}
          {" · "}
          chart {v.chart.name}-{v.chart.version}
        </div>
        {v.requires.gpuProduct && (
          <div className="flex flex-wrap gap-1">
            {v.requires.gpuProduct.map((p) => (
              <Badge key={p} variant="outline">{p}</Badge>
            ))}
          </div>
        )}
        {fit && <Badge variant={fit.ok ? "success" : "warning"}>{fit.text}</Badge>}
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
