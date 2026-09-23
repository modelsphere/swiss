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
import { Tabs } from "@/components/ui/tabs";
import { Empty, ErrorState, Loading } from "@/components/States";

// No "diff": a diff is not audited. Rows from before that carry the action
// still render, they are just not something to filter for.
const ACTIONS = ["", "apply", "install", "uninstall", "rollback"];

// The operation log is the half of the system the cluster cannot rebuild. What
// is deployed is read from the plan ConfigMap beside each release; what was
// *attempted* -- an apply that failed, a rollback, an uninstall -- exists only
// here, and each entry carries the diff helmfile computed before it ran.
//
// Filtered to one release it is also where that release's revisions live, as a
// second tab rather than a second page: they answer one question between them
// -- what has happened to this release -- and used to be two places to look.
// Two tabs and not one table, because they are not the same rows: the log is
// attempts, including the ones that changed nothing, and revisions are what the
// cluster will let you go back to. Nothing in the cluster can rebuild the first
// and nothing in the database can be trusted for the second.
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

  // Revisions are per release: the tab exists only when the page is looking at
  // one, which is also the only view that knows which revision is live.
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

  // In the URL, so a link can open the revisions of a release directly and a
  // reload does not drop back to the log.
  const tab = oneRelease && params.get("tab") === "revisions" ? "revisions" : "log";

  if (isPending) return <Loading what="the operation log" />;
  if (error) return <ErrorState what="the operation log" error={error} />;

  const filtered = release || namespace || action;
  const revisions = revs.data?.revisions ?? [];
  const live = revisions.find((r) => r.current)?.revision;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-lg font-semibold">{oneRelease ? release : "Operation log"}</h1>
        <div className="flex flex-wrap items-center gap-2">
          {/* The action filter is the log's; revisions have no actions to
              filter by. */}
          {tab === "log" && (
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
          )}
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

      {oneRelease && (
        <Tabs
          tabs={[
            { id: "log", label: "Log" },
            { id: "revisions", label: "Revisions" },
          ]}
          active={tab}
          onSelect={(t) => setFilter("tab", t === "log" ? "" : t)}
        />
      )}

      {tab === "log" ? (
        <>
          {/* A note rather than a branch: a swissd with no database keeps no
              log, and its revisions are still in the cluster next door. */}
          {!data.hasStore && (
            <p className="text-sm text-muted-foreground">
              This swissd has no database, so it keeps no operation log. What is deployed, and
              what can be rolled back to, is still read from the cluster.
            </p>
          )}

          {data.runs.length === 0 ? (
            <Empty>
              {filtered ? "Nothing matches that filter." : "Nothing has been attempted yet."}
            </Empty>
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
                    {/* Last, because it is the only column whose width is
                        somebody's prose. */}
                    <TableHead>Note</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.runs.map((r) => (
                    <Row key={r.id} run={r} live={live} />
                  ))}
                </TableBody>
              </Table>
            </Card>
          )}
        </>
      ) : (
        <RevisionsTab
          namespace={namespace}
          release={release}
          revisions={revisions}
          pending={revs.isPending}
          error={revs.error}
        />
      )}
    </div>
  );
}

// RevisionsTab is what this release can be rolled back to: one archived plan
// per applied revision, read from the cluster rather than the log, so losing
// the database does not cost the ability to roll back.
function RevisionsTab({
  namespace,
  release,
  revisions,
  pending,
  error,
}: {
  namespace: string;
  release: string;
  revisions: Revision[];
  pending: boolean;
  error: unknown;
}) {
  if (error) return <ErrorState what="the revisions" error={error} />;
  if (pending) return <Loading what="the revisions" />;
  if (revisions.length === 0) {
    return (
      <Empty>Nothing yet — a plan is archived each time this release is applied.</Empty>
    );
  }

  return (
    <div className="space-y-2">
      <p className="text-sm text-muted-foreground">
        Rolling back re-applies that revision's plan as a new one, with the image, chart and
        engine flags it had — not whatever the catalog says that version is today.
      </p>
      <Card>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-8" />
              <TableHead>Rev</TableHead>
              <TableHead>Model</TableHead>
              <TableHead>Variant</TableHead>
              <TableHead>Chart</TableHead>
              <TableHead>Plan</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {revisions.map((r) => (
              <RevisionRow
                key={r.revision}
                revision={r}
                namespace={namespace}
                release={release}
              />
            ))}
          </TableBody>
        </Table>
      </Card>
    </div>
  );
}

function Row({ run, live }: { run: Run; live?: number }) {
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
        {/* Truncated with the whole of it in the title and in the row below:
            a column that grows with the longest note would push every column
            left of it about. */}
        <TableCell className="max-w-56 truncate text-sm" title={run.note}>
          {run.note || <span className="text-muted-foreground">—</span>}
        </TableCell>
      </TableRow>

      {open && (
        <TableRow>
          <TableCell colSpan={9} className="bg-muted/30">
            {run.note && <p className="mb-2 text-sm italic">{run.note}</p>}
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

// RevisionRow is one archived plan: what ran, and the way back to it. Opening
// it reads what helm holds for that revision, which is not the same document as
// the plan swiss composed -- and the two are only the same until something is
// wrong.
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
        <TableCell className="font-medium tabular-nums whitespace-nowrap">
          {revision.revision}
        </TableCell>
        <TableCell className="whitespace-nowrap">
          {revision.model || <span className="text-muted-foreground">—</span>}
          {revision.version && (
            <span className="text-muted-foreground"> v{revision.version}</span>
          )}
        </TableCell>
        <TableCell className="text-muted-foreground">{revision.variant || "—"}</TableCell>
        <TableCell className="text-muted-foreground">{revision.chart || "—"}</TableCell>
        <TableCell className="font-mono text-xs text-muted-foreground">
          {revision.planHash ? revision.planHash.slice(0, 12) : "—"}
        </TableCell>
        <TableCell className="pr-0 text-right whitespace-nowrap">
          {revision.current ? (
            <Badge variant="success">live</Badge>
          ) : (
            <RollBack namespace={namespace} release={release} to={revision.revision} />
          )}
        </TableCell>
      </TableRow>

      {open && (
        <TableRow>
          <TableCell colSpan={7} className="bg-muted/30">
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
