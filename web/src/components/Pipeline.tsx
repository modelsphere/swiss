import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleCheck, TriangleAlert } from "lucide-react";
import { api, deployApi, type ApplyResult, type DiffResult, type Plan } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { Tabs } from "@/components/ui/tabs";
import { DiffView } from "@/components/DiffView";
import { Provenance } from "@/components/Provenance";
import { ReleaseStatus } from "@/components/ReleaseStatus";
import { Empty, ErrorState } from "@/components/States";

// Pipeline is compose -> diff -> apply -> status, shared by Deploy, Upgrade and
// rollback.
//
// The pages differ in what they compose from -- a catalog model, a release's
// stored plan, or an archived revision that is not composed at all -- and in
// nothing after that. Three copies of this would be three answers to "is a diff
// required", which is exactly the question an operator must not have to ask per
// page.
export function Pipeline({
  namespace,
  release,
  plan,
  diff,
  applied,
  tab,
  onTab,
  onCompose,
  composing,
  composeDisabled,
  composeLabel,
  onDiff,
  onApplied,
  rollbackTo,
  children,
}: {
  namespace: string;
  release: string;
  plan: Plan | null;
  diff: DiffResult | null;
  applied: ApplyResult | null;
  tab: string;
  onTab: (t: string) => void;
  onCompose: () => void;
  composing: boolean;
  composeDisabled?: boolean;
  composeLabel?: string;
  onDiff: (d: DiffResult | null) => void;
  onApplied: (a: ApplyResult) => void;
  // Set when this is a rollback: the revision whose archived plan is being
  // re-applied. There is nothing to compose -- the plan came out of the
  // cluster exactly as it ran -- so the first step is the diff.
  rollbackTo?: number;
  // Rendered on the Plan tab above the composed plan: what moves, on upgrade.
  children?: React.ReactNode;
}) {
  // Why, in the operator's own words. Recorded with the run and written beside
  // the release: the diff says what moved, and nothing but this says why.
  const [note, setNote] = useState("");
  const qc = useQueryClient();

  const diffM = useMutation({
    mutationFn: () =>
      rollbackTo
        ? deployApi.diffRevision(namespace, release, rollbackTo)
        : deployApi.diff({ planHash: plan!.hash }),
    onSuccess: onDiff,
  });

  // Whether the release is already there is a question about the cluster, not
  // about the diff. A diff answers it as a side effect; asking directly is what
  // lets the diff stay optional. Same query key as the Status tab.
  const live = useQuery({
    queryKey: ["status", plan?.release.namespace ?? namespace, plan?.release.name ?? release],
    queryFn: () =>
      api.status(plan?.release.namespace ?? namespace, plan?.release.name ?? release),
    enabled: !!(plan?.release.name ?? release),
  });
  const exists = diff?.exists ?? live.data?.exists;
  const install = exists === false;

  const applyM = useMutation({
    mutationFn: () =>
      rollbackTo
        ? // A rollback must assert a revision -- it runs when something is
          // already wrong, which is when a second operator is most likely to be
          // acting on the same release. The diff's when there is one, else the
          // revision this page last read.
          deployApi.rollback(
            namespace,
            release,
            rollbackTo,
            diff?.revision ?? live.data?.revision ?? 0,
            note.trim(),
          )
        : install
          ? deployApi.install(plan!.hash, note.trim())
          : // expectRevision is the optimistic lock, and it exists only when a
            // diff computed it. Applying without one asserts nothing.
            deployApi.apply(plan!.hash, diff?.revision, note.trim()),
    onSuccess: (r) => {
      onApplied(r);
      onTab("status");
      // Everything that describes this release now describes the revision
      // before this one: the status, its revisions, the log, and the
      // reconciliation row on the deployments page.
      const ns = plan?.release.namespace ?? namespace;
      const rel = plan?.release.name ?? release;
      for (const key of [
        ["status", ns, rel],
        ["revisions", ns, rel],
        ["releasePlan", ns, rel],
        ["runs"],
        ["deployments"],
      ]) {
        qc.invalidateQueries({ queryKey: key });
      }
    },
  });

  const error = diffM.error ?? applyM.error;
  const actionLabel = rollbackTo ? "Roll back" : install ? "Install" : "Apply";

  return (
    <>
      {/* The pipeline is the button row, left to right: compose, diff, apply.
          The tabs below are what each step produced, not where its action
          lives -- a diff button reachable only by opening the diff tab makes
          the step feel required, which it is not. */}
      <div className="space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          {/* Nothing to compose on a rollback: the plan is the archive, and
              recomposing it is what an upgrade does. */}
          {!rollbackTo && (
            <Button onClick={onCompose} disabled={composeDisabled || composing}>
              {composing
                ? "Composing…"
                : (composeLabel ?? (plan ? "Recompose plan" : "Compose plan"))}
            </Button>
          )}
          <Button
            variant="outline"
            onClick={() => {
              onTab("diff");
              diffM.mutate();
            }}
            disabled={!plan || diffM.isPending}
          >
            {diffM.isPending ? "Diffing…" : diff ? "Diff again" : "Diff"}
          </Button>

          {/* Everything left of this reads the cluster; the button right of it
              changes it. */}
          <span className="mx-1 h-6 w-px shrink-0 bg-border" aria-hidden />

          <Button
            variant={rollbackTo ? "destructive" : "default"}
            onClick={() => applyM.mutate()}
            disabled={!plan || applyM.isPending || !!applied || exists === undefined}
          >
            {applyM.isPending ? "Submitting…" : actionLabel}
          </Button>

          {plan && <span className="font-mono text-xs text-muted-foreground">{plan.hash}</span>}
        </div>

        {/* Optional, and deliberately not validated: a note nobody can skip is
            a note that reads "n/a". It sits with the button rather than on the
            apply tab, because that is where the decision is made. */}
        {plan && !applied && (
          <Field label="Note" hint="why — recorded in the log and beside the release">
            <Input
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder={rollbackTo ? "what went wrong with what is running" : "optional"}
              className="max-w-xl"
            />
          </Field>
        )}

        {/* Both of these sit with the button rather than on the apply tab. The
            warning guards a click that is now reachable from anywhere, so
            hiding it behind a tab nobody has to open would be worse than not
            showing it at all. */}
        {plan && !install && !diff && (
          <p className="flex items-start gap-2 text-sm text-warning">
            <TriangleAlert className="mt-0.5 size-4 shrink-0" />
            {/* A rollback still asserts a revision without a diff -- the one
                this page read -- so what it is missing is the preview, not the
                lock. Saying otherwise would teach an operator to distrust a
                warning that is right everywhere else. */}
            <span>
              {rollbackTo
                ? `No diff was run. Nothing has shown what going back to revision ${rollbackTo} changes; it will still refuse if the release has moved since this page read it.`
                : "No diff was run. Nothing has shown what this changes, and nothing asserts the release has not moved since — if someone else applied in the meantime, this overwrites them with no signal."}
            </span>
          </p>
        )}
        {plan && live.error && (
          <p className="text-sm text-warning">
            Could not tell whether {plan.release.name} is already installed:{" "}
            {live.error instanceof Error ? live.error.message : String(live.error)}
          </p>
        )}
      </div>

      {applied && <Applied result={applied} />}

      <Tabs
        tabs={[
          { id: "plan", label: "Plan" },
          { id: "diff", label: "Diff", disabled: !plan, hint: "compose a plan first" },
          {
            id: "apply",
            label: actionLabel,
            disabled: !plan,
            hint: "compose a plan first",
          },
          {
            id: "status",
            label: "Status",
            disabled: !applied && !exists,
            hint: "nothing deployed yet",
          },
        ]}
        active={tab}
        onSelect={onTab}
      />

      {error && <ErrorState what="the request" error={error} />}

      {tab === "plan" && (
        <div className="space-y-4">
          {children}
          {plan ? (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Composed plan</CardTitle>
              </CardHeader>
              <CardContent>
                <Provenance plan={plan} />
              </CardContent>
            </Card>
          ) : (
            <Empty>Compose a plan to see every layer that went into it.</Empty>
          )}
        </div>
      )}

      {tab === "diff" && plan && (
        <div className="space-y-4">
          {diffM.isPending ? (
            <Empty>Diffing against the live release…</Empty>
          ) : diff ? (
            <Card>
              <CardHeader>
                <CardTitle className="flex flex-wrap items-center gap-2 text-base">
                  Diff
                  {diff.changed ? (
                    <Badge variant="warning">changes</Badge>
                  ) : (
                    <Badge variant="success">no changes</Badge>
                  )}
                  <span className="text-xs font-normal text-muted-foreground">
                    {diff.exists
                      ? `against live revision ${diff.revision}`
                      : "release does not exist yet"}
                  </span>
                </CardTitle>
              </CardHeader>
              <CardContent>
                {diff.output.trim() ? (
                  <DiffView output={diff.output} />
                ) : (
                  <p className="text-sm text-muted-foreground">Nothing would change.</p>
                )}
              </CardContent>
            </Card>
          ) : (
            <Empty>
              Optional, and the only thing that shows what this plan does to the live release.
              Running it also pins the apply to the revision it saw.
            </Empty>
          )}
        </div>
      )}

      {tab === "apply" && plan && (
        <div className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">{actionLabel}</CardTitle>
              <p className="text-sm text-muted-foreground">
                {rollbackTo
                  ? `Re-applying revision ${rollbackTo} as a new revision, with the image, chart and engine flags it had — not whatever the catalog says that version is today.`
                  : install
                    ? "The release does not exist; this creates it."
                    : diff
                      ? `Upgrading the live release, asserting it is still at revision ${diff.revision}.`
                      : "Upgrading the live release."}
              </p>
            </CardHeader>
            <CardContent>
              <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[10rem_1fr]">
                <dt className="text-muted-foreground">Release</dt>
                <dd>
                  {plan.release.namespace}/{plan.release.name}
                </dd>
                <dt className="text-muted-foreground">Chart</dt>
                <dd>
                  {plan.chart.name}-{plan.chart.version}
                </dd>
                <dt className="text-muted-foreground">Model</dt>
                <dd>
                  {plan.source.model} v{plan.source.version}
                </dd>
                <dt className="text-muted-foreground">Plan</dt>
                <dd className="font-mono text-xs break-all">{plan.hash}</dd>
              </dl>
            </CardContent>
          </Card>

        </div>
      )}

      {tab === "status" && (
        <ReleaseStatus
          namespace={plan?.release.namespace ?? namespace}
          release={plan?.release.name ?? release}
        />
      )}
    </>
  );
}

function Applied({ result }: { result: ApplyResult }) {
  return (
    <Card>
      <CardContent className="flex items-start gap-3 p-4 text-sm">
        <CircleCheck className="mt-0.5 size-4 shrink-0 text-success" />
        <div className="space-y-1">
          <div className="font-medium">
            {result.release} submitted at revision {result.revision}
          </div>
          <p className="text-muted-foreground">
            Not waiting: a cold load takes 20–40 minutes, and helm cannot tell loading from
            broken. Watch the pods.
          </p>
          {result.statusError && (
            <p className="text-warning">
              The release is live but its status was not updated: {result.statusError}
            </p>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
