import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, TriangleAlert } from "lucide-react";
import { api, deployApi, type Plan, type PlanStatus, type ReleaseStatus as Status } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { Provenance } from "@/components/Provenance";
import { ReleaseStatus } from "@/components/ReleaseStatus";
import { SLOCard } from "@/components/SLOCard";
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
          {/* Revisions are rows in the log now: an applied revision and the
              run that produced it are one event, and rolling back starts from
              the row that records it. */}
          <Link
            to={`/runs?namespace=${encodeURIComponent(namespace)}&release=${encodeURIComponent(release)}`}
          >
            <Button size="sm" variant="outline">
              History &amp; rollback
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

      {s.planStatus?.phase === "applied" && sloEnabled(plan.data) && (
        <SLOCard namespace={namespace} release={release} canEdit={!!cluster.data?.allowDeploy} />
      )}

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
              {/* Why, in the words of whoever did it. Written here as well as
                  to the log, so it survives losing the database. */}
              <Row label="Note" value={p.note} />
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

// The form layer is what the operator set. Compose writes sloRequirement.enabled
// there explicitly, so a missing key is off rather than the chart's default.
function sloEnabled(plan?: Plan): boolean {
  const raw = plan?.layers?.form?.sloRequirement;
  if (!raw || typeof raw !== "object") return false;
  return (raw as { enabled?: boolean }).enabled === true;
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
