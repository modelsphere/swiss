import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleCheck, TriangleAlert } from "lucide-react";
import { api, deployApi, type ApplyResult, type DiffResult, type Plan } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { HoverHint } from "@/components/ui/hint";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Tabs } from "@/components/ui/tabs";
import { DiffView } from "@/components/DiffView";
import { Provenance } from "@/components/Provenance";
import { ReleaseStatus } from "@/components/ReleaseStatus";
import { Empty, ErrorState } from "@/components/States";

// Pipeline is compose -> dry run -> apply -> status, shared by Deploy, Upgrade
// and rollback.
//
// The pages differ in what they compose from -- a catalog model, a release's
// stored plan, or an archived revision that is not composed at all -- and in
// nothing after that. Three copies of this would be three answers to "is a dry
// run required", which is exactly the question an operator must not have to ask
// per page.
//
// The page keeps one button, Compose. Everything a composed plan can be done
// with happens in the dialog it opens, which is what makes the dry run
// unskippable: the apply button is reached by going through it, rather than
// sitting beside it with a warning attached.
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
  // cluster exactly as it ran -- so the button opens the dialog directly.
  rollbackTo?: number;
  // Rendered on the Plan tab above the composed plan: what moves, on upgrade.
  children?: React.ReactNode;
}) {
  // Why, in the operator's own words. Recorded with the run and written beside
  // the release: the diff says what moved, and nothing but this says why.
  const [note, setNote] = useState("");
  // Off every time the dialog is built. Forcing is a decision about one apply,
  // and a switch that remembered yes would force the next upgrade too --
  // quietly, because nobody sets it twice.
  const [force, setForce] = useState(false);
  const [open, setOpen] = useState(false);
  // Set when the compose button is clicked, so the dialog opens on the plan
  // that click produced rather than on whatever was lying around. Composing is
  // a round trip, so this cannot be done in the click handler.
  const [opening, setOpening] = useState(false);
  const qc = useQueryClient();

  useEffect(() => {
    if (opening && plan && !composing) {
      setOpening(false);
      setOpen(true);
    }
  }, [opening, plan, composing]);

  // Editing the form drops the plan, and so does starting a compose. Whatever
  // is on screen describes something that no longer exists, so it closes
  // rather than going stale in place -- which is also what stops a recompose
  // from flashing the dialog open over the plan it is replacing.
  //
  // `opening` deliberately survives this: it is the pending intent to open,
  // and the plan is null for the whole round trip it is waiting on.
  useEffect(() => {
    if (!plan) setOpen(false);
  }, [plan]);

  const diffM = useMutation({
    mutationFn: () =>
      rollbackTo
        ? deployApi.diffRevision(namespace, release, rollbackTo)
        : deployApi.diff({ planHash: plan!.hash }),
    onSuccess: onDiff,
  });

  // Whether the release is already there is a question about the cluster, not
  // about the diff. A diff answers it as a side effect; asking directly is what
  // lets the dialog name the action before the dry run has run. Same query key
  // as the Status tab.
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
          // acting on the same release.
          deployApi.rollback(
            namespace,
            release,
            rollbackTo,
            diff?.revision ?? live.data?.revision ?? 0,
            note.trim(),
          )
        : install
          ? deployApi.install(plan!.hash, note.trim())
          : // expectRevision is the optimistic lock the dry run computed. It
            // always exists here: nothing reaches this call without one.
            deployApi.apply(plan!.hash, diff?.revision, note.trim(), force),
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
  // Install and Apply are different words for different things, and which one
  // this is depends on whether the release exists. Until that query answers,
  // the honest label is neither -- the dry run is safe to run either way, and
  // by the time it returns `exists` is known from the diff itself.
  const known = exists !== undefined;
  const actionLabel = rollbackTo ? "Roll back" : install ? "Install" : "Apply";

  // The two halves of one button. A dry run is the diff, and it is the only way
  // to reach the apply -- so the apply cannot be clicked without one, and the
  // lock the diff computed is always the one the apply asserts.
  //
  // An empty diff stops here on purpose, for every flow rather than just
  // rollback. Applying a plan that changes nothing writes a revision with
  // nothing in it: harmless on an upgrade, and on a rollback a sign that the
  // revision being restored is already the one running.
  const dryRunDone = !!diff;
  const nothingToDo = dryRunDone && !diff.changed;
  const canApply = dryRunDone && diff.changed && !applied && exists !== undefined;

  return (
    <>
      {/* One button. Everything a plan can be done with is behind it, which is
          what keeps the dry run on the path rather than beside it. */}
      <div className="flex flex-wrap items-center gap-3">
        <Button
          onClick={() => {
            if (rollbackTo) {
              setOpen(true);
              return;
            }
            setOpening(true);
            onCompose();
          }}
          disabled={composeDisabled || composing}
        >
          {composing
            ? "Composing…"
            : rollbackTo
              ? `Review rollback to revision ${rollbackTo}`
              : (composeLabel ?? (plan ? "Recompose plan" : "Compose plan"))}
        </Button>

        {/* Closing the dialog keeps the plan and the dry run, so this comes
            back to them rather than recomposing. Not shown on a rollback: the
            button beside it already opens the same dialog. */}
        {plan && !open && !rollbackTo && (
          <button
            type="button"
            onClick={() => setOpen(true)}
            className="text-sm text-muted-foreground underline hover:text-foreground"
          >
            reopen
          </button>
        )}
        {plan && <span className="font-mono text-xs text-muted-foreground">{plan.hash}</span>}
      </div>

      <Dialog
        open={open && !!plan}
        onClose={() => setOpen(false)}
        title={
          rollbackTo
            ? `Roll back ${plan?.release.name ?? release} to revision ${rollbackTo}`
            : `${known ? actionLabel : "Review"} ${plan?.release.name ?? release}`
        }
        subtitle={
          plan && (
            <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <Badge variant="outline">{plan.release.namespace}</Badge>
              <span>
                {plan.source.model}
                {plan.source.version && ` v${plan.source.version}`} · {plan.source.variant} ·
                chart {plan.chart.name}-{plan.chart.version}
              </span>
              <span className="font-mono text-xs break-all">{plan.hash}</span>
            </span>
          )
        }
        footer={
          plan && (
            <Action
              actionLabel={actionLabel}
              known={known}
              rollbackTo={rollbackTo}
              install={install}
              diff={diff}
              applied={applied}
              note={note}
              onNote={setNote}
              force={force}
              onForce={setForce}
              diffPending={diffM.isPending}
              applyPending={applyM.isPending}
              dryRunDone={dryRunDone}
              nothingToDo={nothingToDo}
              canApply={canApply}
              onDryRun={() => {
                onTab("diff");
                diffM.mutate();
              }}
              onApply={() => applyM.mutate()}
            />
          )
        }
      >
        {applied && <Applied result={applied} />}

        <Tabs
          tabs={[
            { id: "plan", label: "Plan" },
            { id: "diff", label: "Dry run" },
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

        {plan && live.error && (
          <p className="text-sm text-warning">
            Could not tell whether {plan.release.name} is already installed:{" "}
            {live.error instanceof Error ? live.error.message : String(live.error)}
          </p>
        )}

        {tab === "plan" && plan && (
          <div className="space-y-4">
            {children}
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Composed plan</CardTitle>
              </CardHeader>
              <CardContent>
                <Provenance plan={plan} />
              </CardContent>
            </Card>
          </div>
        )}

        {tab === "diff" && plan && (
          <div className="space-y-4">
            {diffM.isPending ? (
              <Empty>Running the dry run against the live release…</Empty>
            ) : diff ? (
              <Card>
                <CardHeader>
                  <CardTitle className="flex flex-wrap items-center gap-2 text-base">
                    Dry run
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
                The dry run is what shows what this does to the live release, and it is what
                pins the {actionLabel.toLowerCase()} to the revision it saw. Nothing is written
                until it has run.
              </Empty>
            )}
          </div>
        )}

        {tab === "status" && (
          <ReleaseStatus
            namespace={plan?.release.namespace ?? namespace}
            release={plan?.release.name ?? release}
          />
        )}
      </Dialog>
    </>
  );
}

// Action is the footer: the note, then the one button that is a dry run until
// it has run and the thing itself afterwards.
function Action({
  actionLabel,
  known,
  rollbackTo,
  install,
  diff,
  applied,
  note,
  onNote,
  force,
  onForce,
  diffPending,
  applyPending,
  dryRunDone,
  nothingToDo,
  canApply,
  onDryRun,
  onApply,
}: {
  actionLabel: string;
  known: boolean;
  rollbackTo?: number;
  install: boolean;
  diff: DiffResult | null;
  applied: ApplyResult | null;
  note: string;
  onNote: (v: string) => void;
  force: boolean;
  onForce: (v: boolean) => void;
  diffPending: boolean;
  applyPending: boolean;
  dryRunDone: boolean;
  nothingToDo: boolean;
  canApply: boolean;
  onDryRun: () => void;
  onApply: () => void;
}) {
  if (applied) {
    return (
      <p className="text-sm text-muted-foreground">
        Submitted at revision {applied.revision}. Close this to get back to the release.
      </p>
    );
  }

  // One line of guidance at a time, under the row rather than beside the
  // button: three sentences competing for the space next to it is what made
  // this wrap into a paragraph.
  const guidance = !dryRunDone
    ? `Nothing is written. This shows what would change${
        install ? " when the release is created" : ", and pins the revision"
      }.`
    : canApply && diff
      ? (diff.exists
          ? `Asserting the release is still at revision ${diff.revision}.`
          : "The release does not exist; this creates it.") +
        (force ? " Fields a hand edit left kubectl owning will be overwritten." : "")
      : "";

  return (
    <div className="space-y-2">
      {/* The note, the one install-time choice and the button on one row: the
          note is optional and deliberately not validated -- a note nobody can
          skip is a note that reads "n/a" -- and it belongs beside the button
          because that is where the decision is made. */}
      <div className="flex flex-wrap items-center gap-3">
        <Input
          value={note}
          onChange={(e) => onNote(e.target.value)}
          aria-label="note"
          placeholder={
            rollbackTo
              ? "note — what went wrong with what is running"
              : "note — why, for the log and the release"
          }
          className="min-w-48 flex-1 sm:max-w-md"
        />

        {/* Upgrades only. An install has no live object whose fields another
            manager could own, and a rollback restores a plan that applied
            cleanly once -- forcing during one would be a second surprise on
            top of the one being undone. */}
        {!install && !rollbackTo && (
          <HoverHint text="helm applies server-side, so a field changed by hand with kubectl belongs to kubectl and an upgrade refuses to overwrite it. This takes those fields back — the hand edits on this release are replaced by what the plan says, and are not recoverable from here.">
            <span className="flex items-center gap-2 text-sm">
              <Switch checked={force} onChange={onForce} label="force conflicts" />
              <span className={force ? "text-warning" : "text-muted-foreground"}>Force conflicts</span>
            </span>
          </HoverHint>
        )}

        <div className="ml-auto">
          {!dryRunDone ? (
            <Button onClick={onDryRun} disabled={diffPending}>
              {diffPending ? "Running…" : known ? `${actionLabel} (dry run)` : "Dry run"}
            </Button>
          ) : (
            <Button
              variant={rollbackTo ? "destructive" : "default"}
              onClick={onApply}
              disabled={!canApply || applyPending}
            >
              {applyPending ? "Submitting…" : actionLabel}
            </Button>
          )}
        </div>
      </div>

      {nothingToDo ? (
        <p className="flex items-start gap-2 text-xs text-warning">
          <TriangleAlert className="mt-px size-3.5 shrink-0" />
          {rollbackTo
            ? `Revision ${rollbackTo} matches what is running — nothing to roll back to.`
            : "Nothing would change, so there is nothing to apply. Recompose after editing the settings above."}
        </p>
      ) : (
        guidance && <p className="text-xs text-muted-foreground">{guidance}</p>
      )}
    </div>
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
