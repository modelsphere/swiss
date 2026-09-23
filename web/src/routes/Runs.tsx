import { Fragment, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Undo2 } from "lucide-react";
import { api, type Revision, type Run } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Code } from "@/components/ui/code";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Empty, ErrorState, Loading } from "@/components/States";

// No "diff": a diff is not audited. Rows from before that carry the action
// still render, they are just not something to filter for.
const ACTIONS = ["", "apply", "install", "uninstall", "rollback"];

// The operation log is the half of the system the cluster cannot rebuild. What
// is deployed is read from the plan ConfigMap beside each release; what was
// *attempted* -- an apply that failed, a rollback, an uninstall -- exists only
// here, and each entry carries the diff helmfile computed before it ran.
//
// Filtered to one release it is also that release's revision history, which is
// why there is no second Revisions view: an applied revision and the run that
// produced it are one event, and two tables describing it were two places to
// look and two chances to disagree. The cluster is still the authority on what
// can be rolled back to, so revisions it knows about and the log does not are
// listed here too rather than being unreachable.
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

  // One release is the view that can offer a rollback: it is the only one that
  // knows which revision is live, and rolling back to the revision already
  // running is a new revision with nothing in it.
  const oneRelease = !!(namespace && release);
  const revs = useQuery({
    queryKey: ["revisions", namespace, release],
    queryFn: () => api.revisions(namespace, release),
    enabled: oneRelease,
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
  const revisions = revs.data?.revisions ?? [];
  const live = revisions.find((r) => r.current)?.revision;
  // Revisions the log cannot account for: deployed by the CLI, applied before
  // swissd recorded the revision on the row, or written while the database was
  // gone. They are still rollback targets, so they are listed rather than lost.
  const logged = new Set(data.runs.map((r) => r.revision).filter(Boolean));
  const unlogged = revisions.filter((r) => !r.current && !logged.has(r.revision));

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
          {namespace && ` in ${namespace}`} only
          {live !== undefined && ` — live at revision ${live}`}.
        </p>
      )}
      {revs.error && <ErrorState what="the revisions" error={revs.error} />}

      {/* A note rather than a branch: a swissd with no database still has
          revisions in the cluster, and they are still rollback targets. */}
      {!data.hasStore && (
        <p className="text-sm text-muted-foreground">
          This swissd has no database, so it keeps no operation log. What is deployed, and
          what can be rolled back to, is still read from the cluster.
        </p>
      )}

      {data.runs.length === 0 && unlogged.length === 0 ? (
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
                <TableHead>Rev</TableHead>
                <TableHead>Plan</TableHead>
                {oneRelease && <TableHead />}
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.runs.map((r) => (
                <Row key={r.id} run={r} live={live} oneRelease={oneRelease} />
              ))}
              {unlogged.length > 0 && (
                <TableRow>
                  <TableCell colSpan={9} className="text-xs text-muted-foreground">
                    In the cluster with no entry in this log — still what they were when they
                    ran, and still rollback targets.
                  </TableCell>
                </TableRow>
              )}
              {unlogged.map((r) => (
                <RevisionRow
                  key={`rev-${r.revision}`}
                  revision={r}
                  namespace={namespace}
                  release={release}
                />
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

function Row({
  run,
  live,
  oneRelease,
}: {
  run: Run;
  live?: number;
  oneRelease: boolean;
}) {
  const [open, setOpen] = useState(false);
  // Rolling back to the revision already running would be a new revision with
  // nothing in it, and a row that produced none -- a failed apply, an
  // uninstall -- has nothing to go back to.
  const target = run.revision && run.revision !== live ? run.revision : 0;

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
          {/* The one thing in the row nothing else can reconstruct. */}
          {run.note && <div className="mt-0.5 max-w-xs text-xs italic">{run.note}</div>}
        </TableCell>
        <TableCell>
          <Outcome run={run} />
        </TableCell>
        <TableCell className="tabular-nums text-muted-foreground">
          {took(run.startedAt, run.endedAt)}
        </TableCell>
        <TableCell className="tabular-nums whitespace-nowrap">
          {run.revision ? (
            <>
              {run.revision}
              {run.revision === live && (
                <Badge variant="success" className="ml-2">
                  live
                </Badge>
              )}
            </>
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </TableCell>
        <TableCell className="font-mono text-xs text-muted-foreground">
          {run.planHash ? run.planHash.slice(0, 12) : "—"}
        </TableCell>
        {oneRelease && (
          <TableCell className="pr-0 text-right whitespace-nowrap">
            {target > 0 && <RollBack namespace={run.namespace} release={run.release} to={target} />}
          </TableCell>
        )}
      </TableRow>

      {open && (
        <TableRow>
          <TableCell colSpan={9} className="bg-muted/30">
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
            {!!run.revision && (
              <RevisionValues
                namespace={run.namespace}
                release={run.release}
                revision={run.revision}
              />
            )}
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

// RevisionRow is a revision the cluster has and the log does not. It carries
// what the archive says it was and the same rollback action, so a release
// deployed by the CLI -- or one whose database was lost -- is still a release
// that can be put back.
function RevisionRow({
  revision,
  namespace,
  release,
}: {
  revision: Revision;
  namespace: string;
  release: string;
}) {
  const [open, setOpen] = useState(false);

  return (
    <Fragment>
      <TableRow className="cursor-pointer" onClick={() => setOpen(!open)}>
        <TableCell className="text-muted-foreground">
          {open ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
        </TableCell>
        <TableCell className="whitespace-nowrap text-muted-foreground">—</TableCell>
        <TableCell>
          <Badge variant="muted">revision</Badge>
        </TableCell>
        <TableCell className="font-medium">
          {release}
          <div className="text-xs text-muted-foreground">
            {revision.model}
            {revision.version && ` v${revision.version}`}
            {revision.variant && ` · ${revision.variant}`}
            {revision.chart && ` · ${revision.chart}`}
          </div>
        </TableCell>
        <TableCell className="text-sm text-muted-foreground">not in this log</TableCell>
        <TableCell className="text-muted-foreground">—</TableCell>
        <TableCell className="tabular-nums">{revision.revision}</TableCell>
        <TableCell className="font-mono text-xs text-muted-foreground">
          {revision.planHash ? revision.planHash.slice(0, 12) : "—"}
        </TableCell>
        <TableCell className="pr-0 text-right whitespace-nowrap">
          <RollBack namespace={namespace} release={release} to={revision.revision} />
        </TableCell>
      </TableRow>

      {open && (
        <TableRow>
          <TableCell colSpan={9} className="bg-muted/30">
            <RevisionValues
              namespace={namespace}
              release={release}
              revision={revision.revision}
            />
          </TableCell>
        </TableRow>
      )}
    </Fragment>
  );
}

// The rollback itself is the upgrade page: same diff, same apply, same note,
// with the settings shown as they ran and not editable. A button here that
// rolled back in one click would be the one cluster-changing action in the app
// with nothing between the click and the cluster.
function RollBack({
  namespace,
  release,
  to,
}: {
  namespace: string;
  release: string;
  to: number;
}) {
  return (
    <Link
      to={`/upgrade/${encodeURIComponent(namespace)}/${encodeURIComponent(release)}?rollback=${to}`}
      onClick={(e) => e.stopPropagation()}
    >
      <Button size="sm" variant="outline">
        <Undo2 className="size-3.5" /> Roll back
      </Button>
    </Link>
  );
}

// RevisionValues is what helm holds for one revision, rather than what swiss
// composed. A revision's values never change once written, so what is read is
// kept for as long as the page lives.
function RevisionValues({
  namespace,
  release,
  revision,
}: {
  namespace: string;
  release: string;
  revision: number;
}) {
  const values = useQuery({
    queryKey: ["revision-values", namespace, release, revision],
    queryFn: () => api.revisionValues(namespace, release, revision),
    staleTime: Infinity,
  });

  return (
    <div className="mt-2 space-y-1">
      <div className="text-xs tracking-wide text-muted-foreground uppercase">
        helm values · revision {revision}
      </div>
      {values.isPending ? (
        <p className="text-sm text-muted-foreground">Reading values…</p>
      ) : values.error ? (
        <ErrorState what="the values" error={values.error} />
      ) : (
        <Code lang="yaml" className="max-h-96 overflow-auto">
          {values.data?.values.trim() || "# helm recorded no values here"}
        </Code>
      )}
    </div>
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
