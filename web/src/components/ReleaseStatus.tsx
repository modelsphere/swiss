import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Endpoint } from "@/components/Endpoint";
import { ErrorState, Loading } from "@/components/States";

export function ReleaseStatus({ namespace, release }: { namespace: string; release: string }) {
  const status = useQuery({
    queryKey: ["status", namespace, release],
    queryFn: () => api.status(namespace, release),
    refetchInterval: 10_000,
  });

  if (status.isPending) return <Loading what="status" />;
  if (status.error) return <ErrorState what="status" error={status.error} />;

  const s = status.data;

  return (
    <div className="space-y-4">
      <Endpoint namespace={namespace} release={release} status={s} />

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
