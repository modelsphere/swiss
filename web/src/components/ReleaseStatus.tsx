import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { CircleCheck, CircleX, Loader2 } from "lucide-react";
import { api, deployApi, type ProbeResult } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ErrorState, Loading } from "@/components/States";

export function ReleaseStatus({ namespace, release }: { namespace: string; release: string }) {
  const [probe, setProbe] = useState<ProbeResult | null>(null);

  const status = useQuery({
    queryKey: ["status", namespace, release],
    queryFn: () => api.status(namespace, release),
    refetchInterval: 10_000,
  });
  const probeM = useMutation({
    mutationFn: () => deployApi.probe(namespace, release),
    onSuccess: setProbe,
  });

  if (status.isPending) return <Loading what="status" />;
  if (status.error) return <ErrorState what="status" error={status.error} />;

  const s = status.data;

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-3">
        <Stat label="Revision" value={s.exists ? String(s.revision) : "not installed"} />
        <Stat label="Helm" value={s.helmStatus ?? "—"} />
        <Stat label="Pods ready" value={`${s.ready} / ${s.total}`} tone={s.total > 0 && s.ready < s.total ? "wait" : undefined} />
      </div>

      {s.total > 0 && s.ready < s.total && (
        <p className="text-sm text-muted-foreground">
          A cold load takes 20–40 minutes. A pod that is scheduled and not ready has most
          likely been reading weights, not failing.
        </p>
      )}

      {s.warning && <p className="text-sm text-warning">{s.warning}</p>}

      <Card>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Pod</TableHead>
              <TableHead>Phase</TableHead>
              <TableHead>Ready</TableHead>
              <TableHead>Restarts</TableHead>
              <TableHead>Age</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {s.pods.length === 0 && (
              <TableRow>
                <TableCell className="text-muted-foreground" colSpan={5}>
                  No pods yet.
                </TableCell>
              </TableRow>
            )}
            {s.pods.map((p) => (
              <TableRow key={p.name}>
                <TableCell className="font-medium">
                  {p.name}
                  {p.message && (
                    <div className="mt-0.5 text-xs text-muted-foreground">{p.message}</div>
                  )}
                </TableCell>
                <TableCell className="text-muted-foreground">{p.phase}</TableCell>
                <TableCell>
                  {p.ready ? (
                    <Badge variant="success">ready</Badge>
                  ) : (
                    <Badge variant="muted">loading</Badge>
                  )}
                </TableCell>
                <TableCell className="tabular-nums">{p.restarts}</TableCell>
                <TableCell className="tabular-nums text-muted-foreground">
                  {age(p.ageSeconds)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex flex-wrap items-center gap-2 text-base">
            Serving check
            {s.route && <Badge variant="outline">{s.route}</Badge>}
          </CardTitle>
          <p className="text-sm text-muted-foreground">
            Asks the openresty entrypoint for this route, not the pod. A ready pod behind a
            route that was never published serves nobody.
          </p>
        </CardHeader>
        <CardContent className="space-y-3">
          <Button size="sm" onClick={() => probeM.mutate()} disabled={probeM.isPending}>
            {probeM.isPending ? (
              <>
                <Loader2 className="size-4 animate-spin" /> Checking…
              </>
            ) : (
              "Check service"
            )}
          </Button>

          {probeM.error && <ErrorState what="the check" error={probeM.error} />}

          {probe && (
            <div className="space-y-2 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                {probe.ok && probe.models?.length ? (
                  <>
                    <CircleCheck className="size-4 text-success" />
                    <span className="font-medium">Serving</span>
                  </>
                ) : (
                  <>
                    <CircleX className="size-4 text-destructive" />
                    <span className="font-medium">Not serving yet</span>
                  </>
                )}
                {probe.status ? <Badge variant="muted">HTTP {probe.status}</Badge> : null}
                <span className="text-xs text-muted-foreground">{probe.latencyMs} ms</span>
              </div>
              <div className="font-mono text-xs break-all text-muted-foreground">{probe.url}</div>
              {probe.models?.length ? (
                <div className="flex flex-wrap gap-1">
                  {probe.models.map((m) => (
                    <Badge key={m} variant="success">
                      {m}
                    </Badge>
                  ))}
                </div>
              ) : null}
              {probe.error && <p className="text-destructive">{probe.error}</p>}
              {probe.body && (
                <pre className="overflow-x-auto rounded-md border bg-muted/40 p-2 text-xs">
                  {probe.body}
                </pre>
              )}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: "wait" }) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="text-xs tracking-wide text-muted-foreground uppercase">{label}</div>
        <div className={tone === "wait" ? "text-xl font-semibold text-warning" : "text-xl font-semibold"}>
          {value}
        </div>
      </CardContent>
    </Card>
  );
}

function age(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  return `${Math.floor(seconds / 3600)}h${Math.floor((seconds % 3600) / 60)}m`;
}
