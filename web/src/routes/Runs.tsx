import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight } from "lucide-react";
import { api, type Run } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Empty, ErrorState, Loading } from "@/components/States";

// No "diff": a diff is not audited. Rows from before that carry the action
// still render, they are just not something to filter for.
const ACTIONS = ["", "apply", "install", "uninstall", "rollback"];

// The operation log is the half of the system the cluster cannot rebuild. What
// is deployed is read from the plan ConfigMap beside each release; what was
// *attempted* -- an apply that failed, a rollback, an uninstall -- exists only
// here, and each entry carries the diff helmfile computed before it ran.
export function Runs() {
  const [params, setParams] = useSearchParams();
  const release = params.get("release") ?? "";
  const namespace = params.get("namespace") ?? "";
  const action = params.get("action") ?? "";

  const { data, isPending, error } = useQuery({
    queryKey: ["runs", namespace, release, action],
    queryFn: () => api.runs({ namespace, release, action, limit: 200 }),
    refetchInterval: 20_000,
  });

  const setFilter = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    setParams(next, { replace: true });
  };

  if (isPending) return <Loading what="the operation log" />;
  if (error) return <ErrorState what="the operation log" error={error} />;

  const filtered = release || namespace || action;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-lg font-semibold">Operation log</h1>
        <div className="flex flex-wrap items-center gap-2">
          <select
            className="rounded-md border bg-background px-2 py-1.5 text-sm"
            value={action}
            onChange={(e) => setFilter("action", e.target.value)}
            aria-label="action"
          >
            {ACTIONS.map((a) => (
              <option key={a} value={a}>
                {a || "all actions"}
              </option>
            ))}
          </select>
          {filtered && (
            <button
              onClick={() => setParams(new URLSearchParams(), { replace: true })}
              className="text-sm text-muted-foreground underline hover:text-foreground"
            >
              clear filters
            </button>
          )}
        </div>
      </div>

      {release && (
        <p className="text-sm text-muted-foreground">
          Showing <span className="font-medium">{release}</span>
          {namespace && ` in ${namespace}`} only.
        </p>
      )}

      {!data.hasStore ? (
        <Empty>
          This swissd has no database, so it keeps no operation log. What is deployed is
          still read from the cluster.
        </Empty>
      ) : data.runs.length === 0 ? (
        <Empty>{filtered ? "Nothing matches that filter." : "Nothing has been attempted yet."}</Empty>
      ) : (
        <Card>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-8" />
                <TableHead>When</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Release</TableHead>
                <TableHead>Result</TableHead>
                <TableHead>Took</TableHead>
                <TableHead>Plan</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.runs.map((r) => (
                <Row key={r.id} run={r} />
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

function Row({ run }: { run: Run }) {
  const [open, setOpen] = useState(false);

  // Output is fetched per row rather than with the list: a log view that drags
  // every diff it ever rendered into one response is one nobody opens.
  const detail = useQuery({
    queryKey: ["run", run.id],
    queryFn: () => api.run(run.id),
    enabled: open,
  });

  return (
    <>
      <TableRow className="cursor-pointer" onClick={() => setOpen(!open)}>
        <TableCell className="text-muted-foreground">
          {open ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
        </TableCell>
        <TableCell className="whitespace-nowrap text-muted-foreground">
          {when(run.startedAt)}
        </TableCell>
        <TableCell>
          <Badge variant="outline">{run.action}</Badge>
        </TableCell>
        <TableCell className="font-medium">
          <Link
            to={`/deployments/${encodeURIComponent(run.namespace)}/${encodeURIComponent(run.release)}`}
            onClick={(e) => e.stopPropagation()}
            className="hover:underline"
          >
            {run.release}
          </Link>
          <div className="text-xs text-muted-foreground">{run.namespace}</div>
        </TableCell>
        <TableCell>
          <Outcome run={run} />
        </TableCell>
        <TableCell className="tabular-nums text-muted-foreground">
          {took(run.startedAt, run.endedAt)}
        </TableCell>
        <TableCell className="font-mono text-xs text-muted-foreground">
          {run.planHash ? run.planHash.slice(0, 12) : "—"}
        </TableCell>
      </TableRow>

      {open && (
        <TableRow>
          <TableCell colSpan={7} className="bg-muted/30">
            {run.error && (
              <p className="mb-2 text-sm text-destructive break-all">{run.error}</p>
            )}
            {detail.isPending ? (
              <p className="text-sm text-muted-foreground">Loading output…</p>
            ) : detail.error ? (
              <ErrorState what="the output" error={detail.error} />
            ) : detail.data?.output?.trim() ? (
              <pre className="max-h-96 overflow-auto rounded-md border bg-background p-3 text-xs leading-relaxed">
                {detail.data.output}
              </pre>
            ) : (
              <p className="text-sm text-muted-foreground">No output recorded.</p>
            )}
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

// The log is the only place a failed apply is recorded. The diff branch is for
// rows written before diffs stopped being audited.
function Outcome({ run }: { run: Run }) {
  if (run.error) return <Badge variant="destructive">failed</Badge>;
  if (run.action === "diff") {
    return run.changed ? (
      <Badge variant="warning">changes</Badge>
    ) : (
      <Badge variant="muted">no changes</Badge>
    );
  }
  return <Badge variant="success">ok</Badge>;
}

function when(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  return new Date(t).toLocaleString();
}

function took(start: string, end: string): string {
  const a = Date.parse(start);
  const b = Date.parse(end);
  if (Number.isNaN(a) || Number.isNaN(b)) return "—";
  const ms = b - a;
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.round(ms / 60_000)} min`;
}
