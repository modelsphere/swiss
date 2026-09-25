import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, TriangleAlert } from "lucide-react";
import { api, type GPUPod, type Node } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Empty, ErrorState, Loading } from "@/components/States";

// The GPU inventory: what each node has, and what is holding it.
//
// A node publishes capacity and allocatable but never allocated, so usage is
// summed from the pods bound to it. That needs a cluster-wide pod list, which a
// cluster may withhold — in which case usage reads as unknown rather than as a
// zero nobody measured.
export function Nodes() {
  const { data, isPending, error } = useQuery({
    queryKey: ["nodes"],
    queryFn: api.nodes,
    refetchInterval: 30_000,
  });

  if (isPending) return <Loading what="nodes" />;
  if (error) return <ErrorState what="nodes" error={error} />;

  const known = !data.usageError;
  // GPU nodes first, then the busiest: an idle CPU node is never the row
  // someone opened this page to find.
  const rows = [...data.nodes].sort(
    (a, b) => b.GPUs - a.GPUs || (b.gpusUsed ?? 0) - (a.gpusUsed ?? 0) || a.Name.localeCompare(b.Name),
  );
  const byProduct = new Map<string, { gpus: number; used: number; nodes: number }>();
  for (const n of rows) {
    if (n.GPUs === 0) continue;
    const key = n.GPUProduct || (n.GPUResource ? `${n.GPUResource} (unlabelled)` : "unlabelled");
    const agg = byProduct.get(key) ?? { gpus: 0, used: 0, nodes: 0 };
    agg.gpus += n.GPUs;
    agg.used += n.gpusUsed ?? 0;
    agg.nodes += 1;
    byProduct.set(key, agg);
  }

  return (
    <div className="space-y-4">
      <h1 className="text-lg font-semibold">Nodes</h1>

      <div className="grid gap-3 sm:grid-cols-3">
        <Stat label="Nodes" value={String(data.summary.nodes)} />
        <Stat label="GPUs" value={String(data.summary.gpus)} />
        <Stat
          label="GPUs in use"
          value={known ? `${data.summary.gpusUsed} / ${data.summary.gpus}` : "unknown"}
          tone={known && data.summary.gpus > 0 && data.summary.gpusUsed >= data.summary.gpus ? "warn" : undefined}
        />
      </div>

      {data.usageError && (
        <div className="flex items-start gap-2 rounded-lg border p-3 text-sm text-warning">
          <TriangleAlert className="mt-0.5 size-4 shrink-0" />
          <div>
            GPU usage is unavailable, so only capacity is shown. Summing it needs a
            cluster-wide pod list — set <code>rbac.nodeGPUUsage</code> on the chart.
            <div className="mt-1 text-xs text-muted-foreground">{data.usageError}</div>
          </div>
        </div>
      )}

      {byProduct.size > 0 && (
        <Card>
          <CardContent className="flex flex-wrap gap-2 p-4">
            {[...byProduct.entries()].map(([product, agg]) => (
              <div key={product} className="rounded-md border px-3 py-2 text-sm">
                <div className="font-medium">{product}</div>
                <div className="text-xs text-muted-foreground">
                  {agg.nodes} node{agg.nodes > 1 ? "s" : ""} ·{" "}
                  {known ? `${agg.used} / ${agg.gpus} GPUs used` : `${agg.gpus} GPUs`}
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      {rows.length === 0 ? (
        <Empty>No nodes visible to this swissd.</Empty>
      ) : (
        <Card>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-8" />
                <TableHead>Node</TableHead>
                <TableHead>GPU type</TableHead>
                <TableHead>GPUs</TableHead>
                <TableHead>Usage</TableHead>
                <TableHead>State</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((n) => (
                <Row key={n.Name} node={n} known={known} />
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

function Row({ node, known }: { node: Node; known: boolean }) {
  const [open, setOpen] = useState(false);
  const pods = node.gpuPods ?? [];
  const expandable = pods.length > 0 || (node.Taints?.length ?? 0) > 0;

  return (
    <>
      <TableRow
        className={expandable ? "cursor-pointer" : undefined}
        onClick={() => expandable && setOpen(!open)}
      >
        <TableCell className="text-muted-foreground">
          {expandable ? (
            open ? (
              <ChevronDown className="size-4" />
            ) : (
              <ChevronRight className="size-4" />
            )
          ) : null}
        </TableCell>
        <TableCell className="font-medium">
          {node.Name}
          {node.Kubelet && (
            <div className="text-xs text-muted-foreground">{node.Kubelet}</div>
          )}
        </TableCell>
        <TableCell className="text-muted-foreground">
          {node.GPUs === 0
            ? "—"
            : node.GPUProduct || (
                <Badge variant="warning">
                  {node.GPUResource ? `${node.GPUResource} (unlabelled)` : "unlabelled"}
                </Badge>
              )}
        </TableCell>
        <TableCell className="tabular-nums">{node.GPUs || "—"}</TableCell>
        <TableCell>
          {node.GPUs === 0 ? (
            <span className="text-muted-foreground">—</span>
          ) : known ? (
            <Usage used={node.gpusUsed ?? 0} total={node.GPUs} />
          ) : (
            <span className="text-sm text-muted-foreground">unknown</span>
          )}
        </TableCell>
        <TableCell className="space-x-1">
          {!node.Ready && <Badge variant="destructive">not ready</Badge>}
          {node.Ready && !node.Schedulable && <Badge variant="warning">cordoned</Badge>}
          {node.Ready && node.Schedulable && <Badge variant="success">ready</Badge>}
        </TableCell>
      </TableRow>

      {open && (
        <TableRow>
          <TableCell colSpan={6} className="bg-muted/30">
            {pods.length > 0 ? (
              <div className="space-y-1">
                <div className="text-xs tracking-wide text-muted-foreground uppercase">
                  Holding GPUs
                </div>
                {pods.map((p) => (
                  <PodRow key={`${p.namespace}/${p.name}`} pod={p} />
                ))}
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">No pod is holding a GPU here.</p>
            )}
            {node.Taints?.length ? (
              <div className="mt-3 space-y-1">
                <div className="text-xs tracking-wide text-muted-foreground uppercase">Taints</div>
                <div className="flex flex-wrap gap-1">
                  {node.Taints.map((t) => (
                    <Badge key={t} variant="muted">
                      {t}
                    </Badge>
                  ))}
                </div>
              </div>
            ) : null}
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function PodRow({ pod }: { pod: GPUPod }) {
  return (
    <div className="flex flex-wrap items-baseline gap-2 text-sm">
      <span className="font-medium">{pod.name}</span>
      <span className="text-xs text-muted-foreground">{pod.namespace}</span>
      <Badge variant="muted">
        {pod.gpus} GPU{pod.gpus > 1 ? "s" : ""}
      </Badge>
    </div>
  );
}

function Usage({ used, total }: { used: number; total: number }) {
  const pct = total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 0;
  const full = used >= total && total > 0;
  return (
    <div className="flex items-center gap-2">
      <div className="h-2 w-24 shrink-0 overflow-hidden rounded-full bg-muted">
        <div
          className={full ? "h-full bg-warning" : "h-full bg-success"}
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="text-sm tabular-nums whitespace-nowrap">
        {used} / {total}
      </span>
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: "warn" }) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="text-xs tracking-wide text-muted-foreground uppercase">{label}</div>
        <div className={tone === "warn" ? "text-2xl font-semibold text-warning" : "text-2xl font-semibold"}>
          {value}
        </div>
      </CardContent>
    </Card>
  );
}
