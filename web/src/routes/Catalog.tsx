import { useState } from "react";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, type IndexModel } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Empty, ErrorState, Loading } from "@/components/States";
import { gpuCount, gpuSupport, vendorLabel } from "@/lib/gpu";

export function Catalog() {
  const { data, isPending, error } = useQuery({ queryKey: ["catalog"], queryFn: api.catalog });

  if (isPending) return <Loading what="catalog" />;
  if (error) return <ErrorState what="catalog" error={error} />;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-baseline gap-2">
        <h1 className="text-lg font-semibold">Catalog</h1>
        <span className="text-sm text-muted-foreground">{data.source}</span>
        <code className="ml-auto text-xs text-muted-foreground">{data.ref.slice(0, 19)}</code>
      </div>

      {data.index.models.length === 0 ? (
        <Empty>This catalog lists no models.</Empty>
      ) : (
        <div className="grid gap-3 md:grid-cols-2">
          {data.index.models.map((m) => (
            <ModelCard key={m.name} m={m} />
          ))}
        </div>
      )}
    </div>
  );
}

// A model may publish one variant per accelerator, so the card names every
// kind of card its variants accept rather than only the default's.
function runsOn(variants: IndexModel["versions"][number]["variants"]): string {
  return Array.from(new Set(variants.map((v) => gpuSupport(v.requires)))).join(" · ");
}

function ModelCard({ m }: { m: IndexModel }) {
  const latest = m.versions.find((v) => v.version === m.latest) ?? m.versions[0];
  // A variant is a hardware and parallelism decision, so it is the operator's
  // to make: deploying the default because it sorted first is how a model ends
  // up on the wrong accelerator. Only a model with one variant has nothing to
  // ask about.
  const [choosing, setChoosing] = useState(false);
  const only = latest.variants.length === 1 ? latest.variants[0] : undefined;

  return (
    <Card className="flex h-full flex-col">
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          <Link to={`/catalog/${encodeURIComponent(m.name)}`} className="hover:underline">
            {m.displayName ?? m.name}
          </Link>
          <Badge variant="muted">v{m.latest}</Badge>
          {m.versions.length > 1 && (
            <span className="text-xs font-normal text-muted-foreground">
              {m.versions.length} versions
            </span>
          )}
        </CardTitle>
        <CardDescription className="line-clamp-2">{m.description}</CardDescription>
      </CardHeader>
      <CardContent className="mt-auto space-y-3">
        <div className="text-xs text-muted-foreground">
          {m.source.hf}
          {m.source.sizeGiB ? ` · ${m.source.sizeGiB} GiB` : ""}
        </div>
        <div className="flex flex-wrap items-center gap-1">
          {latest.variants.map((v) => (
            <Badge key={v.id} variant="outline">
              {v.engine} · {gpuCount(v.requires)} · {vendorLabel(v.requires.vendor)}
            </Badge>
          ))}
        </div>
        <div className="text-xs text-muted-foreground">Runs on {runsOn(latest.variants)}</div>
        <div className="flex items-center gap-2">
          {only ? (
            <Link to={deployTo(m.name, only.id)}>
              <Button size="sm">Deploy</Button>
            </Link>
          ) : (
            <Button size="sm" onClick={() => setChoosing(true)}>
              Deploy
            </Button>
          )}
          <Link to={`/catalog/${encodeURIComponent(m.name)}`}>
            <Button size="sm" variant="ghost">
              {latest.variants.length > 1 ? `${latest.variants.length} variants` : "Details"}
            </Button>
          </Link>
        </div>

        <Dialog
          open={choosing}
          onClose={() => setChoosing(false)}
          title={`Deploy ${m.displayName ?? m.name}`}
          subtitle={`v${m.latest} — ${latest.variants.length} variants. Each is a different accelerator and parallelism shape; pick the one this cluster runs.`}
        >
          <div className="space-y-2">
            {latest.variants.map((v) => (
              <Link
                key={v.id}
                to={deployTo(m.name, v.id)}
                className="block rounded-md border p-3 hover:bg-muted/50"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium">{v.id}</span>
                  {v.default && <Badge variant="muted">default</Badge>}
                  <Badge variant="outline">{v.engine}</Badge>
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {gpuCount(v.requires)} · {vendorLabel(v.requires.vendor)} ·{" "}
                  {v.requires.topology ?? "single-node"}
                  {v.requires.rdma && " · RDMA"} · chart {v.chart.name}-{v.chart.version}
                </div>
                {v.description && (
                  <p className="mt-1 text-xs text-muted-foreground">{v.description}</p>
                )}
              </Link>
            ))}
          </div>
        </Dialog>
      </CardContent>
    </Card>
  );
}

function deployTo(model: string, variant: string): string {
  return `/deploy/${encodeURIComponent(model)}?variant=${encodeURIComponent(variant)}`;
}
