import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Search, TriangleAlert } from "lucide-react";
import { api, type GPUPod, type Node, type NodeCondition } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs } from "@/components/ui/tabs";
import { Empty, ErrorState, Loading } from "@/components/States";
import { VENDOR_RESOURCES } from "@/lib/gpu";
import { cn } from "@/lib/utils";

// The GPU inventory: what each node has, what is free, and what is holding it.
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
  const [focus, setFocus] = useState<Focus>("all");
  const [product, setProduct] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  if (isPending) return <Loading what="nodes" />;
  if (error) return <ErrorState what="nodes" error={error} />;

  const known = !data.usageError;
  const nodes = data.nodes;
  const gpuNodes = nodes.filter((n) => n.GPUs > 0);
  const products = productsOf(nodes, known);
  const counts = countByFocus(nodes, known);
  // Room and full need the pod list; without it those filters would call every
  // schedulable GPU node full.
  const shownFocus: Focus = !known && (focus === "room" || focus === "full") ? "all" : focus;
  const q = query.trim().toLowerCase();
  const visible = nodes
    .filter((n) => matchesFocus(n, shownFocus, known))
    .filter((n) => product === null || (n.GPUs > 0 && productOf(n).key === product))
    .filter((n) => matchesQuery(n, q))
    .sort(byInventory(shownFocus));
  const dirty = q !== "" || product !== null || shownFocus !== "all";

  const gpus = data.summary.gpus;
  const used = data.summary.gpusUsed;
  const free = nodes.reduce((s, n) => s + (n.gpusFree ?? 0), 0);
  const placeableFree = nodes.reduce((s, n) => s + (placeable(n) ? (n.gpusFree ?? 0) : 0), 0);
  const largest = largestFree(nodes, known);
  const freeNote = freeHint(known, gpus, free, placeableFree);
  const over = known && used + free > gpus;

  function clear() {
    setQuery("");
    setProduct(null);
    setFocus("all");
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-baseline gap-2">
        <h1 className="text-lg font-semibold">Nodes</h1>
        {data.cluster && <span className="text-sm text-muted-foreground">{data.cluster}</span>}
      </div>

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Stat
          label="Free GPUs"
          value={gpus === 0 ? "—" : known ? String(free) : "unknown"}
          hint={gpus === 0 ? undefined : freeNote.text}
          tone={known && gpus > 0 && free === 0 ? "warn" : undefined}
          hintTone={freeNote.tone}
        />
        <Stat
          label="Largest free"
          value={gpuNodes.length === 0 ? "—" : known ? String(largest.free) : "unknown"}
          hint={largestHint(known, gpuNodes.length, largest)}
          tone={known && gpuNodes.length > 0 && largest.free === 0 ? "warn" : undefined}
        />
        <Stat
          label="GPU nodes"
          value={String(gpuNodes.length)}
          hint={
            counts.gpuUnavailable > 0
              ? `${counts.gpuUnavailable} unavailable`
              : counts.cpu > 0
                ? `${counts.cpu} CPU`
                : undefined
          }
          hintTone={counts.gpuUnavailable > 0 ? "warn" : undefined}
        />
        <Stat
          label="GPUs in use"
          value={gpus === 0 ? "—" : known ? `${used} / ${gpus}` : "unknown"}
          hint={gpus === 0 ? undefined : known ? (over ? "a node is overcommitted" : undefined) : `${gpus} allocatable`}
          tone={known && gpus > 0 && used >= gpus ? "warn" : undefined}
          hintTone={over ? "warn" : undefined}
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

      {products.length > 0 && (
        <div className="grid gap-2 [grid-template-columns:repeat(auto-fit,minmax(16rem,1fr))]">
          {products.map(([key, agg]) => (
            <button
              key={key}
              type="button"
              aria-pressed={product === key}
              onClick={() => {
                setProduct((cur) => (cur === key ? null : key));
                setFocus((f) => (f === "cpu" ? "all" : f));
              }}
              className={cn(
                "rounded-lg border bg-card p-3 text-left shadow-sm transition-colors",
                product === key ? "border-foreground" : "hover:bg-muted/40",
              )}
            >
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 font-medium" title={agg.title}>
                  {agg.unlabelled ? (
                    <Badge variant="warning" className="whitespace-normal">
                      {agg.title}
                    </Badge>
                  ) : (
                    agg.title
                  )}
                </div>
                <div className={cn("shrink-0 tabular-nums", known && agg.largest === 0 && "text-warning")}>
                  {known ? `${agg.free} free` : `${agg.gpus} GPUs`}
                </div>
              </div>
              {known && (
                <div className="mt-2">
                  <Meter used={agg.used} total={agg.gpus} />
                </div>
              )}
              <div className="mt-1.5 text-xs text-muted-foreground">{productSubtitle(agg, known)}</div>
            </button>
          ))}
        </div>
      )}

      {nodes.length === 0 ? (
        <Empty>No nodes visible to this swissd.</Empty>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Node, pod, taint, or IP"
                aria-label="Filter by node, pod, taint, or IP"
                className="h-8 w-56 pl-8"
              />
            </div>
            <div className="flex flex-wrap gap-1">
              <FilterButton pressed={shownFocus === "all"} onClick={() => setFocus("all")}>
                All
                <Count n={nodes.length} />
              </FilterButton>
              {gpuNodes.length > 0 && (
                <FilterButton pressed={shownFocus === "gpu"} onClick={() => setFocus("gpu")}>
                  GPUs
                  <Count n={counts.gpu} />
                </FilterButton>
              )}
              {known && gpuNodes.length > 0 && (
                <>
                  <FilterButton pressed={shownFocus === "room"} onClick={() => setFocus("room")}>
                    Has room
                    <Count n={counts.room} />
                  </FilterButton>
                  <FilterButton pressed={shownFocus === "full"} onClick={() => setFocus("full")}>
                    Full
                    <Count n={counts.full} />
                  </FilterButton>
                </>
              )}
              {(counts.unavailable > 0 || shownFocus === "unavailable") && (
                <FilterButton pressed={shownFocus === "unavailable"} onClick={() => setFocus("unavailable")}>
                  Unavailable
                  <Count n={counts.unavailable} />
                </FilterButton>
              )}
              {(counts.cpu > 0 || shownFocus === "cpu") && (
                <FilterButton
                  pressed={shownFocus === "cpu"}
                  onClick={() => {
                    setFocus("cpu");
                    setProduct(null);
                  }}
                >
                  CPU
                  <Count n={counts.cpu} />
                </FilterButton>
              )}
            </div>
            <div className="ml-auto flex items-center gap-2">
              <span className="text-sm text-muted-foreground tabular-nums">{visible.length} nodes</span>
              {dirty && (
                <Button variant="ghost" size="sm" onClick={clear}>
                  Clear
                </Button>
              )}
            </div>
          </div>

          {visible.length === 0 ? (
            <Empty>
              {emptyText(shownFocus, q !== "" || product !== null)}
              {dirty && (
                <>
                  {" "}
                  <button type="button" className="underline" onClick={clear}>
                    Clear filters
                  </button>
                </>
              )}
            </Empty>
          ) : (
            <>
              <MobileList nodes={visible} known={known} />
              <Card className="hidden lg:block">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-10" />
                      <TableHead>Node</TableHead>
                      <TableHead>GPU type</TableHead>
                      <TableHead>GPUs</TableHead>
                      <TableHead>Free</TableHead>
                      <TableHead>In use</TableHead>
                      <TableHead>State</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {visible.map((n) => (
                      <Row key={n.Name} node={n} known={known} />
                    ))}
                  </TableBody>
                </Table>
              </Card>
            </>
          )}
        </>
      )}
    </div>
  );
}

type Focus = "all" | "gpu" | "room" | "full" | "unavailable" | "cpu";

function placeable(n: Node): boolean {
  return n.GPUs > 0 && n.Ready && n.Schedulable;
}

function matchesFocus(n: Node, focus: Focus, known: boolean): boolean {
  switch (focus) {
    case "all":
      return true;
    case "gpu":
      return n.GPUs > 0;
    case "room":
      return known && placeable(n) && (n.gpusFree ?? 0) > 0;
    case "full":
      return known && placeable(n) && (n.gpusFree ?? 0) === 0;
    case "unavailable":
      return !n.Ready || !n.Schedulable;
    case "cpu":
      return n.GPUs === 0;
  }
}

function matchesQuery(n: Node, q: string): boolean {
  if (!q) return true;
  const hay = [
    n.Name,
    n.Kubelet ?? "",
    n.InternalIP ?? "",
    n.ExternalIP ?? "",
    n.GPUProduct,
    n.GPUResource ?? "",
    ...(n.Taints ?? []),
    ...(n.gpuPods ?? []).flatMap((p) => [p.name, p.namespace]),
  ];
  return hay.join("\n").toLowerCase().includes(q);
}

// Has-room is a placement question, so the biggest hole sorts first. Everywhere
// else the busiest GPU node leads: that is the row accounting starts from.
function byInventory(focus: Focus) {
  return (a: Node, b: Node) => {
    if (focus === "room") return (b.gpusFree ?? 0) - (a.gpusFree ?? 0) || a.Name.localeCompare(b.Name);
    return b.GPUs - a.GPUs || (b.gpusUsed ?? 0) - (a.gpusUsed ?? 0) || a.Name.localeCompare(b.Name);
  };
}

function countByFocus(nodes: Node[], known: boolean) {
  const counts = { gpu: 0, room: 0, full: 0, unavailable: 0, gpuUnavailable: 0, cpu: 0 };
  for (const n of nodes) {
    if (n.GPUs > 0) counts.gpu += 1;
    else counts.cpu += 1;
    if (!n.Ready || !n.Schedulable) {
      counts.unavailable += 1;
      if (n.GPUs > 0) counts.gpuUnavailable += 1;
    }
    if (known && placeable(n)) {
      if ((n.gpusFree ?? 0) > 0) counts.room += 1;
      else counts.full += 1;
    }
  }
  return counts;
}

function productOf(n: Node): { key: string; title: string; unlabelled: boolean } {
  if (n.GPUProduct) return { key: n.GPUProduct, title: n.GPUProduct, unlabelled: false };
  const title = n.GPUResource ? `${n.GPUResource} (unlabelled)` : "unlabelled";
  return { key: title, title, unlabelled: true };
}

type Agg = {
  title: string;
  unlabelled: boolean;
  gpus: number;
  used: number;
  free: number;
  nodes: number;
  largest: number;
  unavailable: number;
};

function productsOf(nodes: Node[], known: boolean): [string, Agg][] {
  const map = new Map<string, Agg>();
  for (const n of nodes) {
    if (n.GPUs === 0) continue;
    const p = productOf(n);
    const agg = map.get(p.key) ?? {
      title: p.title,
      unlabelled: p.unlabelled,
      gpus: 0,
      used: 0,
      free: 0,
      nodes: 0,
      largest: 0,
      unavailable: 0,
    };
    agg.gpus += n.GPUs;
    agg.used += n.gpusUsed ?? 0;
    agg.free += n.gpusFree ?? 0;
    agg.nodes += 1;
    if (!n.Ready || !n.Schedulable) agg.unavailable += 1;
    if (known && placeable(n) && (n.gpusFree ?? 0) > agg.largest) agg.largest = n.gpusFree ?? 0;
    map.set(p.key, agg);
  }
  return [...map.entries()].sort((a, b) => b[1].gpus - a[1].gpus || a[0].localeCompare(b[0]));
}

function productSubtitle(agg: Agg, known: boolean): string {
  const nodes = `${agg.nodes} node${agg.nodes === 1 ? "" : "s"}`;
  if (!known) return `${nodes} · ${agg.gpus} GPUs`;
  const parts = [nodes, agg.largest > 0 ? `largest ${agg.largest}` : "no schedulable room"];
  if (agg.unavailable > 0) parts.push(`${agg.unavailable} unavailable`);
  return parts.join(" · ");
}

// Free counts GPUs no pod holds, including on a cordoned node. Largest free is
// the biggest hole a pod can actually land in.
function largestFree(nodes: Node[], known: boolean): { free: number; names: string[] } {
  if (!known) return { free: 0, names: [] };
  let free = 0;
  let names: string[] = [];
  for (const n of nodes) {
    if (!placeable(n)) continue;
    const f = n.gpusFree ?? 0;
    if (f > free) {
      free = f;
      names = [n.Name];
    } else if (f === free && f > 0) names.push(n.Name);
  }
  return { free, names };
}

function largestHint(known: boolean, gpuNodes: number, largest: { free: number; names: string[] }): string | undefined {
  if (!known || gpuNodes === 0) return undefined;
  if (largest.free === 0) return "no schedulable room";
  if (largest.names.length === 1) return largest.names[0];
  return `on ${largest.names.length} nodes`;
}

function freeHint(
  known: boolean,
  gpus: number,
  free: number,
  placeableFree: number,
): { text?: string; tone?: "warn" } {
  if (!known) return { text: "not measured" };
  if (gpus === 0) return {};
  // Overcommit is reported on the in-use stat. Here the useful split is how
  // much of the free count can actually take a pod.
  if (placeableFree !== free) {
    return placeableFree === 0
      ? { text: "none schedulable", tone: "warn" }
      : { text: `${placeableFree} schedulable` };
  }
  return { text: `of ${gpus} allocatable` };
}

function emptyText(focus: Focus, narrowed: boolean): string {
  if (narrowed) return "No nodes match this filter.";
  switch (focus) {
    case "room":
      return "No schedulable GPU has room.";
    case "full":
      return "No schedulable GPU is full.";
    case "unavailable":
      return "Every node is ready and schedulable.";
    case "cpu":
      return "No CPU nodes.";
    case "gpu":
      return "No GPU nodes.";
    case "all":
      return "No nodes match this filter.";
  }
}

function MobileList({ nodes, known }: { nodes: Node[]; known: boolean }) {
  return (
    <div className="space-y-2 lg:hidden">
      {nodes.map((n) => (
        <MobileRow key={n.Name} node={n} known={known} />
      ))}
    </div>
  );
}

function MobileRow({ node, known }: { node: Node; known: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <Card className="overflow-hidden">
      <button
        type="button"
        className="block w-full p-3 text-left"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <div className="flex items-start justify-between gap-3">
          <div>
            <NodeName node={node} />
            <div className="mt-1">
              <StateCell node={node} known={known} />
            </div>
          </div>
          <div className="flex items-start gap-2">
            {node.GPUs > 0 && (
              <div className="text-right">
                <div className="text-lg leading-none">
                  <FreeCell node={node} known={known} />
                </div>
                {known && <div className="text-xs text-muted-foreground">free</div>}
              </div>
            )}
            {open ? (
              <ChevronDown className="mt-0.5 size-4 text-muted-foreground" />
            ) : (
              <ChevronRight className="mt-0.5 size-4 text-muted-foreground" />
            )}
          </div>
        </div>
        <div className="mt-1 text-sm">
          <GpuLabel node={node} />
        </div>
        {node.GPUs > 0 && (
          <div className="mt-2">
            {known ? (
              <Usage used={node.gpusUsed ?? 0} total={node.GPUs} />
            ) : (
              <span className="text-sm text-muted-foreground">usage unknown</span>
            )}
          </div>
        )}
      </button>
      {open && (
        <div className="border-t px-3 py-3">
          <Detail node={node} known={known} />
        </div>
      )}
    </Card>
  );
}

function Row({ node, known }: { node: Node; known: boolean }) {
  const [open, setOpen] = useState(false);

  return (
    <>
      <TableRow className="cursor-pointer" onClick={() => setOpen((v) => !v)}>
        <TableCell className="w-10">
          <button
            type="button"
            aria-expanded={open}
            aria-label={`${open ? "Collapse" : "Expand"} ${node.Name}`}
            className="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-muted"
            onClick={(e) => {
              e.stopPropagation();
              setOpen((v) => !v);
            }}
          >
            {open ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
          </button>
        </TableCell>
        <TableCell>
          <NodeName node={node} />
        </TableCell>
        <TableCell>
          <GpuLabel node={node} />
        </TableCell>
        <TableCell className="tabular-nums">{node.GPUs || "—"}</TableCell>
        <TableCell>
          <FreeCell node={node} known={known} />
        </TableCell>
        <TableCell>
          {node.GPUs === 0 ? (
            <span className="text-muted-foreground">—</span>
          ) : known ? (
            <Usage used={node.gpusUsed ?? 0} total={node.GPUs} />
          ) : (
            <span className="text-sm text-muted-foreground">unknown</span>
          )}
        </TableCell>
        <TableCell>
          <StateCell node={node} known={known} />
        </TableCell>
      </TableRow>
      {open && (
        <TableRow>
          <TableCell colSpan={7} className="bg-muted/30 p-3">
            <div className="rounded-lg border bg-card px-3 py-1">
              <Detail node={node} known={known} />
            </div>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function NodeName({ node }: { node: Node }) {
  const taints = node.Taints ?? [];
  return (
    <>
      <div className="font-medium">{node.Name}</div>
      {taints.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-1">
          {taints.map((t) => (
            <Badge key={t} variant={taintVariant(t)} className="whitespace-normal">
              {t}
            </Badge>
          ))}
        </div>
      )}
      {node.Kubelet && <div className="mt-0.5 text-xs text-muted-foreground">{node.Kubelet}</div>}
    </>
  );
}

function StateCell({ node, known }: { node: Node; known: boolean }) {
  const over = known && node.GPUs > 0 && (node.gpusUsed ?? 0) > node.GPUs;
  // Ready is already the ready / not-ready badge. A pressure condition that
  // is False is the normal reading, so the column only adds one that is on
  // or Unknown. The Node tab still lists every condition.
  const conditions = (node.Conditions ?? []).filter(
    (c) => c.Type !== "Ready" && (c.Status === "True" || c.Status === "Unknown"),
  );
  return (
    <div className="flex flex-wrap gap-1">
      {!node.Ready && <Badge variant="destructive">not ready</Badge>}
      {node.Ready && !node.Schedulable && <Badge variant="warning">cordoned</Badge>}
      {node.Ready && node.Schedulable && <Badge variant="success">ready</Badge>}
      {over && <Badge variant="warning">overcommitted</Badge>}
      {conditions.map((c) => (
        <Badge key={c.Type} variant={conditionVariant(c)} title={conditionTitle(c)}>
          {c.Type}
        </Badge>
      ))}
    </div>
  );
}

function GpuLabel({ node }: { node: Node }) {
  if (node.GPUs === 0) return <span className="text-muted-foreground">—</span>;
  if (!node.GPUProduct) {
    return (
      <Badge variant="warning">{node.GPUResource ? `${node.GPUResource} (unlabelled)` : "unlabelled"}</Badge>
    );
  }
  const unusual = node.GPUResource && node.GPUResource !== VENDOR_RESOURCES.nvidia;
  return (
    <div>
      <div>{node.GPUProduct}</div>
      {unusual && <div className="text-xs text-muted-foreground">{node.GPUResource}</div>}
    </div>
  );
}

function FreeCell({ node, known }: { node: Node; known: boolean }) {
  if (node.GPUs === 0) return <span className="text-muted-foreground">—</span>;
  if (!known) return <span className="text-sm text-muted-foreground">unknown</span>;
  const free = node.gpusFree ?? 0;
  // A cordoned node can report GPUs free. They are not a hole a pod can take,
  // so only schedulable room is drawn as the number to scan for.
  const room = placeable(node) && free > 0;
  return <span className={cn("tabular-nums", room ? "font-semibold" : "text-muted-foreground")}>{free}</span>;
}

function Detail({ node, known }: { node: Node; known: boolean }) {
  const pods = node.gpuPods ?? [];
  const [tab, setTab] = useState(node.GPUs > 0 ? "holding" : "node");
  return (
    <div onClick={(e) => e.stopPropagation()}>
      <Tabs
        tabs={[
          { id: "holding", label: pods.length > 0 ? `Holding ${pods.length}` : "Holding" },
          { id: "node", label: "Node" },
        ]}
        active={tab}
        onSelect={setTab}
      />
      <div className="pt-3">
        {tab === "holding" ? <HoldingTable node={node} pods={pods} known={known} /> : <NodeFacts node={node} />}
      </div>
    </div>
  );
}

function HoldingTable({ node, pods, known }: { node: Node; pods: GPUPod[]; known: boolean }) {
  if (node.GPUs === 0) return <p className="text-sm text-muted-foreground">This node has no GPUs.</p>;
  if (!known) return <p className="text-sm text-muted-foreground">GPU usage was not measured.</p>;
  if (pods.length === 0) return <p className="text-sm text-muted-foreground">No pod is holding a GPU here.</p>;
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="border-b text-left text-xs tracking-wide text-muted-foreground uppercase">
          <th className="py-1.5 pr-4 font-medium">Pod</th>
          <th className="py-1.5 pr-4 font-medium">Namespace</th>
          <th className="py-1.5 text-right font-medium">GPUs</th>
        </tr>
      </thead>
      <tbody>
        {pods.map((p) => (
          <tr key={`${p.namespace}/${p.name}`} className="border-b last:border-0">
            <td className="py-1.5 pr-4 font-medium">{p.name}</td>
            <td className="py-1.5 pr-4 text-muted-foreground">{p.namespace}</td>
            <td className="py-1.5 text-right tabular-nums">{p.gpus}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function NodeFacts({ node }: { node: Node }) {
  const taints = node.Taints ?? [];
  const conditions = node.Conditions ?? [];
  return (
    <div className="space-y-4">
      <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-2">
        <Fact label="Kubelet" value={node.Kubelet || "—"} />
        <Fact label="IP" value={node.InternalIP || node.ExternalIP || "—"} />
        {node.InternalIP && node.ExternalIP && <Fact label="External IP" value={node.ExternalIP} />}
        <Fact label="Schedulable" value={node.Schedulable ? "yes" : "cordoned"} />
        {node.GPUResource && <Fact label="Resource" value={node.GPUResource} />}
      </dl>
      <div>
        <div className="text-xs tracking-wide text-muted-foreground uppercase">Taints</div>
        {taints.length === 0 ? (
          <p className="mt-1 text-sm text-muted-foreground">None.</p>
        ) : (
          <div className="mt-1 flex flex-wrap gap-1">
            {taints.map((t) => (
              <Badge key={t} variant={taintVariant(t)} className="whitespace-normal">
                {t}
              </Badge>
            ))}
          </div>
        )}
      </div>
      <div>
        <div className="text-xs tracking-wide text-muted-foreground uppercase">Conditions</div>
        {conditions.length === 0 ? (
          <p className="mt-1 text-sm text-muted-foreground">None reported.</p>
        ) : (
          <table className="mt-1 w-full text-sm">
            <thead>
              <tr className="border-b text-left text-xs tracking-wide text-muted-foreground uppercase">
                <th className="py-1.5 pr-4 font-medium">Condition</th>
                <th className="py-1.5 pr-4 font-medium">Status</th>
                <th className="py-1.5 font-medium">Reason</th>
              </tr>
            </thead>
            <tbody>
              {conditions.map((c) => (
                <tr key={c.Type} className="border-b last:border-0">
                  <td className="py-1.5 pr-4 font-medium">{c.Type}</td>
                  <td className="py-1.5 pr-4">
                    <Badge variant={conditionVariant(c)}>{c.Status}</Badge>
                  </td>
                  <td className="py-1.5 text-muted-foreground" title={c.Message || undefined}>
                    {c.Reason || "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

// Pressure and Unavailable are healthy when False. Ready is healthy when True.
function conditionTitle(c: NodeCondition): string | undefined {
  const text = [c.Reason, c.Message].filter(Boolean).join(" — ");
  return text || undefined;
}

function conditionVariant(c: NodeCondition): "success" | "warning" | "destructive" | "muted" {
  const problem = c.Type !== "Ready";
  if (c.Status === "True") return problem ? "warning" : "success";
  if (c.Status === "False") return problem ? "muted" : "destructive";
  return "muted";
}

function taintVariant(t: string): "destructive" | "warning" | "muted" {
  if (t.endsWith(":NoExecute")) return "destructive";
  if (t.endsWith(":NoSchedule")) return "warning";
  return "muted";
}

function Usage({ used, total }: { used: number; total: number }) {
  const full = total > 0 && used >= total;
  const pct = total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 0;
  const occupied = full ? "bg-warning" : "bg-success";
  return (
    <div className="flex items-center gap-2" role="img" aria-label={`${used} of ${total} GPUs in use`}>
      {total > 0 && total <= 16 ? (
        <span className="flex shrink-0 gap-0.5" aria-hidden>
          {Array.from({ length: total }, (_, i) => (
            <span key={i} className={cn("size-2 rounded-[2px]", i < used ? occupied : "bg-border")} />
          ))}
        </span>
      ) : (
        <span className="h-2 w-24 shrink-0 overflow-hidden rounded-full bg-border" aria-hidden>
          <span className={cn("block h-full", occupied)} style={{ width: `${pct}%` }} />
        </span>
      )}
      <span className="text-sm tabular-nums whitespace-nowrap">
        {used} / {total}
      </span>
    </div>
  );
}

function Meter({ used, total }: { used: number; total: number }) {
  const pct = total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 0;
  const full = total > 0 && used >= total;
  return (
    <div
      className="h-1.5 overflow-hidden rounded-full bg-border"
      role="img"
      aria-label={`${used} of ${total} GPUs in use`}
    >
      <div className={cn("h-full", full ? "bg-warning" : "bg-success")} style={{ width: `${pct}%` }} />
    </div>
  );
}

function Stat({
  label,
  value,
  hint,
  tone,
  hintTone,
}: {
  label: string;
  value: string;
  hint?: string;
  tone?: "warn";
  hintTone?: "warn";
}) {
  return (
    <Card className="min-w-0">
      <CardContent className="p-4">
        <div className="text-xs tracking-wide text-muted-foreground uppercase">{label}</div>
        <div className={cn("text-2xl font-semibold", tone === "warn" && "text-warning")}>{value}</div>
        {hint && (
          <div
            className={cn("mt-1 truncate text-xs text-muted-foreground", hintTone === "warn" && "text-warning")}
            title={hint}
          >
            {hint}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function FilterButton({
  pressed,
  onClick,
  children,
}: {
  pressed: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onClick}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm transition-colors",
        pressed ? "bg-muted font-medium" : "text-muted-foreground hover:bg-muted/60",
      )}
    >
      {children}
    </button>
  );
}

function Count({ n }: { n: number }) {
  return <span className="tabular-nums text-muted-foreground">{n}</span>;
}
