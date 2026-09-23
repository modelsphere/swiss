import { Link } from "react-router";
import { Plus } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { api, type Deployment } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Empty, ErrorState, Loading } from "@/components/States";

export function Deployments() {
  const { data, isPending, error } = useQuery({
    queryKey: ["deployments"],
    queryFn: api.deployments,
    // A release changes on a human timescale; a rollout takes 20-40 minutes.
    refetchInterval: 15_000,
  });

  if (isPending) return <Loading what="deployments" />;
  if (error) return <ErrorState what="deployments" error={error} />;

  // Untracked first. A release nobody's inventory knows about is how two engines
  // end up on one set of GPUs, so it is the row that should be read first.
  const rows = [...data.deployments].sort(
    (a, b) => Number(a.managed) - Number(b.managed) || a.release.localeCompare(b.release),
  );

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-lg font-semibold">LLM deployments</h1>
        <Link to="/catalog">
          <Button size="sm">
            <Plus className="size-4" /> Deploy a model
          </Button>
        </Link>
      </div>

      <div className="grid gap-3 sm:grid-cols-3">
        <Stat label="Releases" value={data.summary.total} />
        <Stat label="Untracked" value={data.summary.untracked} tone={data.summary.untracked > 0 ? "warn" : undefined} />
        <Stat label="Behind catalog" value={data.summary.catalogBehind} />
      </div>

      {rows.length === 0 ? (
        <Empty>
          No helm releases in this cluster.{" "}
          <Link to="/catalog" className="underline">
            Deploy one
          </Link>
          .
        </Empty>
      ) : (
        <Card>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Release</TableHead>
                <TableHead>Namespace</TableHead>
                <TableHead>Model</TableHead>
                <TableHead>Route</TableHead>
                <TableHead>Chart</TableHead>
                <TableHead>Rev</TableHead>
                <TableHead>Status</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((d) => (
                <Row key={`${d.namespace}/${d.release}`} d={d} />
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

function Row({ d }: { d: Deployment }) {
  return (
    <TableRow>
      <TableCell className="font-medium">
        <Link
          to={`/deployments/${encodeURIComponent(d.namespace)}/${encodeURIComponent(d.release)}`}
          className="hover:underline"
        >
          {d.release}
        </Link>
        {d.drift && <div className="mt-0.5 text-xs text-muted-foreground">{d.drift}</div>}
      </TableCell>
      <TableCell className="text-muted-foreground">{d.namespace}</TableCell>
      <TableCell>
        {d.managed ? (
          <span>
            {d.model}
            {d.version && <span className="text-muted-foreground"> v{d.version}</span>}
            {d.variant && <span className="text-muted-foreground"> · {d.variant}</span>}
          </span>
        ) : (
          <Badge variant="warning">untracked</Badge>
        )}
      </TableCell>
      <TableCell className="font-mono text-xs text-muted-foreground">
        {d.route || "—"}
      </TableCell>
      <TableCell className="text-muted-foreground">{d.chart}</TableCell>
      <TableCell className="tabular-nums">{d.revision}</TableCell>
      <TableCell>
        <StatusBadge status={d.status} />
      </TableCell>
      <TableCell className="text-right">
        <Link to={`/deployments/${encodeURIComponent(d.namespace)}/${encodeURIComponent(d.release)}`}>
          <Button size="sm" variant="outline">
            Details
          </Button>
        </Link>
      </TableCell>
    </TableRow>
  );
}

// "pending-upgrade" on this workload usually means a 20-40 minute model load in
// progress, not a failure -- so it reads as in-flight rather than as an error.
function StatusBadge({ status }: { status?: string }) {
  if (!status) return null;
  if (status === "deployed") return <Badge variant="success">deployed</Badge>;
  if (status.startsWith("pending")) return <Badge variant="muted">{status}</Badge>;
  if (status === "failed") return <Badge variant="destructive">failed</Badge>;
  return <Badge variant="outline">{status}</Badge>;
}

function Stat({ label, value, tone }: { label: string; value: number; tone?: "warn" }) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="text-xs tracking-wide text-muted-foreground uppercase">{label}</div>
        <div className={tone === "warn" && value > 0 ? "text-2xl font-semibold text-warning" : "text-2xl font-semibold"}>
          {value}
        </div>
      </CardContent>
    </Card>
  );
}
