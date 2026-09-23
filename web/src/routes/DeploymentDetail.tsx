import { Fragment, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, TriangleAlert } from "lucide-react";
import {
  api,
  deployApi,
  type RevisionDiff,
  type PlanStatus,
  type ReleaseStatus as Status,
} from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { Code } from "@/components/ui/code";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import { DiffView } from "@/components/DiffView";
import { Provenance } from "@/components/Provenance";
import { ReleaseStatus } from "@/components/ReleaseStatus";
import { ErrorState, Loading } from "@/components/States";

export function DeploymentDetail() {
  const { namespace = "", release = "" } = useParams();
  const cluster = useQuery({ queryKey: ["cluster"], queryFn: api.cluster });

  const status = useQuery({
    queryKey: ["status", namespace, release],
    queryFn: () => api.status(namespace, release),
    refetchInterval: 15_000,
  });
  // A release swiss did not deploy has no plan beside it. That is the untracked
  // row, and it is not an error here -- the page just shows less.
  const plan = useQuery({
    queryKey: ["releasePlan", namespace, release],
    queryFn: () => api.releasePlan(namespace, release),
    retry: false,
  });

  if (status.isPending) return <Loading what={release} />;
  if (status.error) return <ErrorState what={release} error={status.error} />;

  const s = status.data;

  return (
    <div className="space-y-5">
      <Link
        to="/"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ChevronLeft className="size-4" /> LLM deployments
      </Link>

      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">{release}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            <Badge variant="outline">{namespace}</Badge>{" "}
            {plan.data ? (
              <>
                {plan.data.source.model}
                {plan.data.source.version && ` v${plan.data.source.version}`} ·{" "}
                {plan.data.source.variant} · {plan.data.chart.name}-{plan.data.chart.version}
              </>
            ) : (
              <Badge variant="warning">untracked — no plan beside this release</Badge>
            )}
          </p>
        </div>
        <div className="flex gap-2">
          <Link
            to={`/runs?namespace=${encodeURIComponent(namespace)}&release=${encodeURIComponent(release)}`}
          >
            <Button size="sm" variant="outline">
              History
            </Button>
          </Link>
          {plan.data && (
            <Link
              to={`/upgrade/${encodeURIComponent(namespace)}/${encodeURIComponent(release)}`}
            >
              <Button size="sm" variant="outline">
                Upgrade
              </Button>
            </Link>
          )}
        </div>
      </div>

      <InstallStatus status={s} />

      <ReleaseStatus namespace={namespace} release={release} />

      {plan.data && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Plan</CardTitle>
            <p className="text-sm text-muted-foreground">
              What this release was composed from, read from the ConfigMap beside it.
            </p>
          </CardHeader>
          <CardContent>
            <Provenance plan={plan.data} />
          </CardContent>
        </Card>
      )}

      {cluster.data?.allowDeploy && s.exists && (
        <Revisions namespace={namespace} release={release} live={s.revision} />
      )}

      {cluster.data?.allowDeploy && (
        <Uninstall namespace={namespace} release={release} exists={s.exists} />
      )}
    </div>
  );
}

// InstallStatus reads the status key swiss writes beside the release, which is
// the only thing that can report an apply that started and never finished --
// helm's own status describes the last apply that returned.
function InstallStatus({ status }: { status: Status }) {
  const p: PlanStatus | undefined = status.planStatus;

  if (!status.exists && !p) {
    return (
      <Card>
        <CardContent className="p-4 text-sm text-muted-foreground">
          No helm release here. Nothing has been installed under this name.
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          Install status
          {p && <PhaseBadge phase={p.phase} />}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[11rem_1fr]">
          <Row label="Helm status" value={status.helmStatus} />
          <Row label="Helm revision" value={status.exists ? String(status.revision) : "not installed"} />
          {p && (
            <>
              <Row label="Last action" value={p.action} />
              <Row label="Recorded revision" value={p.revision ? String(p.revision) : undefined} />
              <Row label="Started" value={p.startedAt} />
              <Row label="Updated" value={p.updatedAt} />
            </>
          )}
          <Row label="Route" value={status.route} />
        </dl>

        {p?.error && <p className="text-sm text-warning">{p.error}</p>}

        {p?.phase === "applying" && (
          <p className="flex items-start gap-2 text-sm text-warning">
            <TriangleAlert className="mt-0.5 size-4 shrink-0" />
            An apply was started and never recorded a result. It may still be running, or
            swissd may have been restarted mid-apply.
          </p>
        )}

        {!p && status.exists && (
          <p className="text-sm text-muted-foreground">
            No status recorded beside this release — swiss did not deploy it.
          </p>
        )}
      </CardContent>
    </Card>
  );
}

// Revisions is what this release can be rolled back to: one archived workspace
// per applied revision, read from the cluster. A rollback re-applies one
// forward as a new revision -- helm's own rollback does the same -- so the plan
// beside the release keeps describing what is running.
function Revisions({
  namespace,
  release,
  live,
}: {
  namespace: string;
  release: string;
  live: number;
}) {
  const [diff, setDiff] = useState<RevisionDiff | null>(null);
  // Every revision whose values are open. Several at once, because two
  // revisions' values are read side by side or not at all.
  const [openValues, setOpenValues] = useState<number[]>([]);
  const qc = useQueryClient();

  const toggleValues = (rev: number) =>
    setOpenValues((open) =>
      open.includes(rev) ? open.filter((n) => n !== rev) : [...open, rev],
    );

  // Optional, exactly as on the deploy and upgrade pipelines. Running it also
  // pins the rollback to the revision it saw.
  const diffM = useMutation({
    mutationFn: (to: number) => deployApi.diffRevision(namespace, release, to),
    onSuccess: setDiff,
  });

  const revs = useQuery({
    queryKey: ["revisions", namespace, release],
    queryFn: () => api.revisions(namespace, release),
  });

  const roll = useMutation({
    // The revision the diff saw when there is one, else the one this page
    // rendered. Either way the rollback asserts it has not moved.
    mutationFn: (to: number) =>
      deployApi.rollback(namespace, release, to, diff?.toRevision === to ? diff.revision : live),
    onSettled: () => {
      setDiff(null);
      qc.invalidateQueries({ queryKey: ["status", namespace, release] });
      qc.invalidateQueries({ queryKey: ["revisions", namespace, release] });
      qc.invalidateQueries({ queryKey: ["release-plan", namespace, release] });
    },
  });

  const rows = revs.data?.revisions ?? [];
  const archived = rows.filter((r) => !r.current);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Revisions</CardTitle>
        <p className="text-sm text-muted-foreground">
          Diff first: rolling back re-applies that revision's plan as a new one, with the
          image, chart and engine flags it had — not whatever the catalog says that version
          is today.
        </p>
      </CardHeader>
      <CardContent className="space-y-2">
        {revs.error && <ErrorState what="the revisions" error={revs.error} />}
        {diffM.error && <ErrorState what="the diff" error={diffM.error} />}
        {roll.error && <ErrorState what="the rollback" error={roll.error} />}

        {revs.isPending ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : archived.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            Nothing to roll back to yet — a plan is kept each time this release is applied.
          </p>
        ) : (
          // A table rather than a row of flex boxes: a "current" badge and a
          // rollback button are not the same width, and only shared columns
          // keep the two action cells from stepping about from row to row.
          <Table>
            <TableBody>
              {rows.map((r) => (
                <Fragment key={r.revision}>
                  <TableRow>
                    <TableCell className="pl-0 text-sm tabular-nums whitespace-nowrap">
                      rev {r.revision}
                    </TableCell>
                    <TableCell className="w-full text-sm text-muted-foreground">
                      {r.model}
                      {r.version && ` v${r.version}`}
                      {r.variant && ` · ${r.variant}`}
                      {r.chart && ` · ${r.chart}`}
                    </TableCell>
                    <TableCell className="whitespace-nowrap">
                      <Button size="sm" variant="ghost" onClick={() => toggleValues(r.revision)}>
                        {openValues.includes(r.revision) ? "Hide values" : "Values"}
                      </Button>
                    </TableCell>
                    <TableCell className="pr-0 text-right whitespace-nowrap">
                      {r.current ? (
                        <Badge variant="success">current</Badge>
                      ) : diff?.toRevision === r.revision && diff.changed ? (
                        <Button
                          size="sm"
                          variant="destructive"
                          onClick={() => roll.mutate(r.revision)}
                          disabled={roll.isPending}
                        >
                          {roll.isPending ? "Rolling back…" : "Roll back"}
                        </Button>
                      ) : (
                        // A diff that came back empty never offers the rollback:
                        // the revision is what is already running, so applying it
                        // forward would be a new revision with nothing in it.
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => {
                            setDiff(null);
                            diffM.mutate(r.revision);
                          }}
                          disabled={diffM.isPending || roll.isPending}
                        >
                          {diffM.isPending && diffM.variables === r.revision
                            ? "Diffing…"
                            : diff?.toRevision === r.revision
                              ? "Diff again"
                              : "Roll back"}
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>

                  {diff?.toRevision === r.revision && (
                    <TableRow>
                      <TableCell colSpan={4} className="space-y-1 px-0">
                        <div className="flex flex-wrap items-center gap-2 text-xs">
                          {diff.changed ? (
                            <Badge variant="warning">changes</Badge>
                          ) : (
                            <Badge variant="success">no changes</Badge>
                          )}
                          <span className="text-muted-foreground">
                            against live revision {diff.revision}
                          </span>
                        </div>
                        {diff.output.trim() ? (
                          <DiffView output={diff.output} />
                        ) : (
                          <p className="text-sm text-muted-foreground">
                            Nothing would change — this revision matches what is running.
                          </p>
                        )}
                      </TableCell>
                    </TableRow>
                  )}

                  {openValues.includes(r.revision) && (
                    <TableRow>
                      <TableCell colSpan={4} className="px-0">
                        <RevisionValues
                          namespace={namespace}
                          release={release}
                          revision={r.revision}
                        />
                      </TableCell>
                    </TableRow>
                  )}
                </Fragment>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
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
    <div className="space-y-1">
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

// Uninstall is typed to confirm rather than guarded by a dialog. It removes a
// release that takes 20-40 minutes to load back, so the release name is the one
// thing the operator must have read before this goes ahead.
function Uninstall({
  namespace,
  release,
  exists,
}: {
  namespace: string;
  release: string;
  exists: boolean;
}) {
  const [typed, setTyped] = useState("");
  const navigate = useNavigate();
  const qc = useQueryClient();

  const run = useMutation({
    mutationFn: () => deployApi.uninstall(namespace, release),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["deployments"] });
      if (!r.planError) navigate("/");
    },
  });

  return (
    <Card className="border-destructive/30">
      <CardHeader>
        <CardTitle className="text-base">Uninstall</CardTitle>
        <p className="text-sm text-muted-foreground">
          Runs <code>helm uninstall</code> and removes the plan recorded beside the release.
          The audit log keeps the record. Reloading the weights afterwards takes 20–40
          minutes.
        </p>
      </CardHeader>
      <CardContent className="space-y-3">
        {!exists ? (
          <p className="text-sm text-muted-foreground">There is no live release to remove.</p>
        ) : (
          <>
            <Field label="Type the release name to confirm" hint={release}>
              <Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={release} />
            </Field>
            <Button
              variant="destructive"
              onClick={() => run.mutate()}
              disabled={typed !== release || run.isPending}
            >
              {run.isPending ? "Uninstalling…" : `Uninstall ${release}`}
            </Button>
          </>
        )}

        {run.error && <ErrorState what="the uninstall" error={run.error} />}
        {run.data?.planError && (
          <p className="text-sm text-warning">
            The release is gone, but its plan ConfigMap was not removed: {run.data.planError}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function PhaseBadge({ phase }: { phase: string }) {
  if (phase === "applied") return <Badge variant="success">applied</Badge>;
  if (phase === "failed") return <Badge variant="destructive">failed</Badge>;
  if (phase === "applying") return <Badge variant="warning">applying</Badge>;
  return <Badge variant="outline">{phase}</Badge>;
}

// An unset field is left out rather than rendered as a dash: the list is read at
// a glance, and empty rows bury the ones that were actually set.
function Row({ label, value }: { label: string; value?: string }) {
  if (!value?.trim()) return null;
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="break-all">{value}</dd>
    </>
  );
}
