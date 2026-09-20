import { useState } from "react";
import { Link, useParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowRight, ChevronLeft, CircleCheck, TriangleAlert } from "lucide-react";
import { api, deployApi, type ApplyResult, type DiffResult, type Plan } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { DiffView } from "@/components/DiffView";
import { ErrorState, Loading } from "@/components/States";

export function Upgrade() {
  const { namespace = "", release = "" } = useParams();
  const [version, setVersion] = useState("");
  const [proposed, setProposed] = useState<Plan | null>(null);
  const [diff, setDiff] = useState<DiffResult | null>(null);
  const [applied, setApplied] = useState<ApplyResult | null>(null);

  const current = useQuery({
    queryKey: ["release-plan", namespace, release],
    queryFn: () => api.releasePlan(namespace, release),
  });
  const catalog = useQuery({ queryKey: ["catalog"], queryFn: api.catalog });
  const cluster = useQuery({ queryKey: ["cluster"], queryFn: api.cluster });

  const composeM = useMutation({
    mutationFn: () => deployApi.plan({ fromRelease: release, namespace, version: version || undefined }),
    onSuccess: (p) => {
      setProposed(p);
      setDiff(null);
      setApplied(null);
    },
  });
  const diffM = useMutation({
    mutationFn: () => deployApi.diff({ planHash: proposed!.hash } as never),
    onSuccess: setDiff,
  });
  const applyM = useMutation({
    mutationFn: () => deployApi.apply(proposed!.hash, diff!.revision),
    onSuccess: setApplied,
  });

  if (current.isPending) return <Loading what={release} />;
  if (current.error) return <ErrorState what={`the plan for ${release}`} error={current.error} />;

  const cur = current.data;
  const versions =
    catalog.data?.index.models.find((m) => m.name === cur.source.model)?.versions.map((v) => v.version) ?? [];
  const error = composeM.error ?? diffM.error ?? applyM.error;

  if (!cluster.data?.allowDeploy) {
    return (
      <div className="space-y-4">
        <Back namespace={namespace} />
        <Card>
          <CardContent className="flex items-start gap-3 p-4 text-sm">
            <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
            <div>This swissd is read-only; upgrades are disabled.</div>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <Back namespace={namespace} />

      <div>
        <h1 className="text-xl font-semibold">Upgrade {release}</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          {cur.source.model} · {cur.source.variant} · {namespace}
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Target model version</CardTitle>
          <p className="text-sm text-muted-foreground">
            Only the catalog layer moves. Everything you set at deploy time is carried forward
            unchanged.
          </p>
        </CardHeader>
        <CardContent className="flex flex-wrap items-center gap-3">
          <select
            className="rounded-md border bg-background px-2 py-1.5 text-sm"
            value={version}
            onChange={(e) => {
              setVersion(e.target.value);
              setProposed(null);
              setDiff(null);
            }}
            aria-label="target model version"
          >
            <option value="">latest</option>
            {versions.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
          <Button onClick={() => composeM.mutate()} disabled={composeM.isPending}>
            {composeM.isPending ? "Composing…" : "Compose upgrade"}
          </Button>
        </CardContent>
      </Card>

      {error && <ErrorState what="the request" error={error} />}

      {proposed && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">What moves</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            <Change
              label="Model version"
              from={cur.source.version}
              to={proposed.source.version}
            />
            <Change
              label="Chart"
              from={`${cur.chart.name}-${cur.chart.version}`}
              to={`${proposed.chart.name}-${proposed.chart.version}`}
            />
            <Change
              label="Entry digest"
              from={cur.source.digest?.slice(7, 19)}
              to={proposed.source.digest?.slice(7, 19)}
            />
            <Change label="Variant" from={cur.source.variant} to={proposed.source.variant} />
            {cur.hash === proposed.hash && (
              <p className="pt-1 text-sm text-muted-foreground">
                Identical to what is deployed — nothing to upgrade.
              </p>
            )}
          </CardContent>
        </Card>
      )}

      {proposed && (
        <div className="grid gap-4 md:grid-cols-2">
          <Carried title="Site settings" plan={proposed} layer="site" />
          <Carried title="Your deploy settings" plan={proposed} layer="form" />
        </div>
      )}

      {applied && (
        <Card>
          <CardContent className="flex items-start gap-3 p-4 text-sm">
            <CircleCheck className="mt-0.5 size-4 shrink-0 text-success" />
            <div>
              <div className="font-medium">
                {applied.release} upgraded to revision {applied.revision}
              </div>
              <p className="text-muted-foreground">
                Not waiting: a cold load takes 20–40 minutes. Watch the pods.
              </p>
            </div>
          </CardContent>
        </Card>
      )}

      {diff && !applied && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              Diff
              {diff.changed ? (
                <Badge variant="warning">changes</Badge>
              ) : (
                <Badge variant="success">no changes</Badge>
              )}
              <span className="text-xs font-normal text-muted-foreground">
                live revision {diff.revision}
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
      )}

      {proposed && !applied && (
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" onClick={() => diffM.mutate()} disabled={diffM.isPending}>
            {diffM.isPending ? "Diffing…" : "Diff"}
          </Button>
          <Button onClick={() => applyM.mutate()} disabled={!diff || applyM.isPending}>
            {applyM.isPending ? "Applying…" : "Approve upgrade"}
          </Button>
          {!diff && (
            <span className="text-xs text-muted-foreground">
              Review the diff before approving.
            </span>
          )}
        </div>
      )}
    </div>
  );
}

function Change({ label, from, to }: { label: string; from?: string; to?: string }) {
  const same = from === to;
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <span className="w-32 shrink-0 text-muted-foreground">{label}</span>
      <span className="font-mono">{from || "—"}</span>
      {!same && (
        <>
          <ArrowRight className="size-3.5 text-muted-foreground" />
          <span className="font-mono font-medium text-success">{to || "—"}</span>
        </>
      )}
      {same && <Badge variant="muted">unchanged</Badge>}
    </div>
  );
}

function Carried({ title, plan, layer }: { title: string; plan: Plan; layer: string }) {
  const paths = Object.entries(plan.provenance ?? {})
    .filter(([, l]) => l === layer)
    .map(([p]) => p)
    .sort();

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{title}</CardTitle>
        <p className="text-xs text-muted-foreground">
          {paths.length} values, carried forward unchanged
        </p>
      </CardHeader>
      <CardContent>
        <dl className="grid gap-x-3 text-xs sm:grid-cols-[minmax(0,14rem)_1fr]">
          {paths.map((path) => (
            <div key={path} className="contents">
              <dt className="truncate text-muted-foreground">{path}</dt>
              <dd className="truncate font-mono">{render(get(plan.values, path))}</dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  );
}

function get(tree: Record<string, unknown>, path: string): unknown {
  let cur: unknown = tree;
  for (const seg of path.split(".")) {
    if (typeof cur !== "object" || cur === null) return undefined;
    cur = (cur as Record<string, unknown>)[seg];
  }
  return cur;
}

function render(v: unknown): string {
  if (v === undefined) return "";
  if (Array.isArray(v)) return v.map(render).join(" ");
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

function Back({ namespace }: { namespace: string }) {
  return (
    <Link to="/" className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ChevronLeft className="size-4" /> Deployments in {namespace}
    </Link>
  );
}
