import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, X } from "lucide-react";
import { deployApi, type SLOBound, type SLOConfig, type SLOMetric, type SLOSection } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { HoverHint } from "@/components/ui/hint";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { messageOf, useToast } from "@/components/ui/toast";
import { ErrorState, Loading } from "@/components/States";
import { cn } from "@/lib/utils";

const TYPES = ["avg", "p50", "p80", "p90", "p95", "p99"];

// Talks to swissd, which proxies slo-api /config/{serviceId}. The chart's
// object is often only serviceId plus minimumDeployment of one replica;
// everything else here is optional and is what a save adds.
//
// Three things the API cannot do, which this form has to stay honest about: it
// stores priority as high or normal and no tier in between, a save merges so
// only Reset clears a field, and it never creates a requirement — so with none
// installed there is nothing here to edit.
export function SLOCard({
  namespace,
  release,
  canEdit,
}: {
  namespace: string;
  release: string;
  canEdit: boolean;
}) {
  const qc = useQueryClient();
  const slo = useQuery({
    queryKey: ["slo", namespace, release],
    queryFn: () => deployApi.slo(namespace, release),
    retry: false,
  });
  const [form, setForm] = useState<Draft | null>(null);

  useEffect(() => {
    if (!slo.data || form) return;
    setForm(draftOf(slo.data));
  }, [slo.data, form]);

  // The outcome goes to a toast rather than into the card: a save is the one
  // thing here that reaches the cluster, and it should say so where it is seen
  // even if the card has scrolled away by the time the server answers.
  const toast = useToast();
  const save = useMutation({
    mutationFn: () => deployApi.saveSLO(namespace, release, editOf(form!)),
    onSuccess: (next) => {
      qc.setQueryData(["slo", namespace, release], next);
      setForm(draftOf(next));
      toast.success(`SLO saved for ${next.route || release}.`);
    },
    onError: (e) => toast.error(`SLO not saved: ${messageOf(e)}`),
  });
  const reset = useMutation({
    mutationFn: () => deployApi.resetSLO(namespace, release),
    onSuccess: (next) => {
      qc.setQueryData(["slo", namespace, release], next);
      setForm(draftOf(next));
      toast.success(`SLO for ${next.route || release} reset to the CRD defaults.`);
    },
    onError: (e) => toast.error(`SLO not reset: ${messageOf(e)}`),
  });

  // What is stored, to compare the draft against: a save that sends the values
  // already on the object is a write nobody asked for, and on a requirement
  // parked at a tier this API cannot express it would quietly flatten it.
  const stored = useMemo(() => (slo.data ? draftOf(slo.data) : null), [slo.data]);

  if (slo.isPending || !form) return <Loading what="SLO" />;
  if (slo.error) return <ErrorState what="SLO" error={slo.error} />;

  const data = slo.data!;
  const name = data.route || release;
  const busy = save.isPending || reset.isPending;
  // Nothing to edit until the requirement exists: this API only ever patches.
  const editable = canEdit && data.found;
  const changed = JSON.stringify(form) !== JSON.stringify(stored);

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-x-4 gap-y-3">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <HoverHint
            text={`What the scaler reads to size ${name}: the latency and throughput it has to hold, and the replica bounds it may move between. A save sends only the fields that have a value, so emptying one leaves what is stored untouched; Reset returns every field to the CRD defaults.`}
          >
            <CardTitle className="text-base">SLO</CardTitle>
          </HoverHint>
          {!data.found && <Badge variant="warning">not registered</Badge>}
          {data.found && !canEdit && <Badge variant="muted">read-only</Badge>}
          {data.found && form.highPriority && <Badge variant="success">high priority</Badge>}
        </div>
        {canEdit && (
          <div className="flex shrink-0 gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => reset.mutate()}
              disabled={busy || !data.found}
              title="Return every field to the CRD defaults"
            >
              {reset.isPending ? "Resetting…" : "Reset"}
            </Button>
            <Button
              size="sm"
              onClick={() => save.mutate()}
              disabled={busy || !editable || !changed || !canSave(form)}
            >
              {save.isPending ? "Saving…" : "Save"}
            </Button>
          </div>
        )}
      </CardHeader>

      <CardContent className="divide-y pt-0">
        {!data.found && (
          <p className="pb-4 text-sm text-muted-foreground">
            The chart creates this requirement at install — the SLO API only edits one that exists.
          </p>
        )}

        <Row label="Priority" hint="High is CRD priority 10, normal 0. The SLO API stores no tier in between, so 1..9 can only be set on the CR itself.">
          <div className="flex h-9 items-center gap-2.5">
            <Switch
              checked={form.highPriority}
              disabled={!editable}
              onChange={(highPriority) => setForm({ ...form, highPriority })}
              label="high priority"
            />
            <span className={cn("text-sm", !form.highPriority && "text-muted-foreground")}>
              {form.highPriority ? "high" : "normal"}
            </span>
          </div>
        </Row>

        <Row
          label="Bounds"
          hint="The floor the scaler never goes below and the ceiling it never passes. The floor counts replicas or concurrency; the ceiling is always replicas. An empty ceiling leaves the stored value alone — only Reset clears it."
        >
          <div className="flex items-center gap-2">
            <Select
              value={form.minType}
              disabled={!editable}
              options={["replica", "concurrency"]}
              className="w-32"
              onChange={(minType) => setForm({ ...form, minType })}
            />
            <Input
              value={form.minValue}
              disabled={!editable}
              inputMode="numeric"
              placeholder="min"
              aria-label="minimum value"
              className="min-w-0 flex-1"
              onChange={(e) => setForm({ ...form, minValue: e.target.value })}
            />
            <span className="shrink-0 text-xs text-muted-foreground">to</span>
            <Input
              value={form.maxValue}
              disabled={!editable}
              inputMode="numeric"
              placeholder="none"
              aria-label="maximum replicas"
              className="min-w-0 flex-1"
              onChange={(e) => setForm({ ...form, maxValue: e.target.value })}
            />
            <span className="w-8 shrink-0" aria-hidden />
          </div>
        </Row>

        <Row label="TTFT" hint="Time to first token, in seconds. A ceiling: a percentile above it is a violation, and the scaler adds capacity.">
          <Metrics
            label="TTFT"
            unit="s"
            rows={form.ttft}
            disabled={!editable}
            onChange={(ttft) => setForm({ ...form, ttft })}
          />
        </Row>

        <Row label="OTPS" hint="Output tokens per second, per request. A floor: a percentile below it is a violation, and the scaler adds capacity.">
          <Metrics
            label="OTPS"
            unit="tok/s"
            rows={form.otps}
            disabled={!editable}
            onChange={(otps) => setForm({ ...form, otps })}
          />
        </Row>

        {/* A write's outcome is a toast now. Repeating it in the card would
            report the same refusal twice, in two places, with two lifetimes. */}
      </CardContent>
    </Card>
  );
}

// One label column for every row, the switch included, so the card has a single
// alignment axis rather than one per control shape. Every control fills the
// column, so the boxes share a left and a right edge down the card, and the
// guidance hides behind the label's mark instead of sitting under it.
function Row({ label, hint, children }: { label: string; hint: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-x-4 gap-y-1 py-3 sm:grid-cols-[9rem_minmax(0,1fr)] sm:items-start">
      <div className="flex min-h-9 min-w-0 items-center">
        <HoverHint text={hint}>
          <span className="text-sm font-medium">{label}</span>
        </HoverHint>
      </div>
      <div className="min-w-0">{children}</div>
    </div>
  );
}

interface MetricRow {
  type: string;
  threshold: string;
}

interface Draft {
  highPriority: boolean;
  minType: string;
  minValue: string;
  maxValue: string;
  ttft: MetricRow[];
  otps: MetricRow[];
}

function draftOf(data: SLOConfig): Draft {
  // swissd sends both; highPriority is what the SLO server actually stored.
  return {
    highPriority: data.highPriority ?? (data.priority ?? 0) >= 10,
    minType: data.minimumDeployment?.type || "replica",
    minValue: data.minimumDeployment ? String(data.minimumDeployment.value) : "1",
    maxValue: data.maximumDeployment ? String(data.maximumDeployment.value) : "",
    ttft: rowsOf(data.ttft),
    otps: rowsOf(data.otps),
  };
}

function rowsOf(section?: SLOSection): MetricRow[] {
  const metrics = section?.default?.metrics;
  if (!metrics?.length) return [];
  return metrics.map((m) => ({ type: m.type, threshold: String(m.threshold) }));
}

function editOf(form: Draft) {
  const body: {
    highPriority?: boolean;
    minimumDeployment?: SLOBound;
    maximumDeployment?: SLOBound;
    ttft?: SLOSection;
    otps?: SLOSection;
  } = {};
  body.highPriority = form.highPriority;
  const min = intOf(form.minValue);
  if (min != null) body.minimumDeployment = { type: form.minType, value: min };
  const max = intOf(form.maxValue);
  if (max != null) body.maximumDeployment = { type: "replica", value: max };
  const ttft = metricsOf(form.ttft);
  const otps = metricsOf(form.otps);
  if (ttft) body.ttft = { default: { metrics: ttft } };
  if (otps) body.otps = { default: { metrics: otps } };
  return body;
}

function metricsOf(rows: MetricRow[]): SLOMetric[] | undefined {
  const metrics = rows
    .filter((r) => r.threshold.trim() !== "")
    .map((r) => ({ type: r.type, threshold: Number(r.threshold) }));
  return metrics.length ? metrics : undefined;
}

function intOf(raw: string): number | null {
  return intIn(raw, 1, Number.POSITIVE_INFINITY);
}

function intIn(raw: string, lo: number, hi: number): number | null {
  if (raw.trim() === "") return null;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < lo || n > hi) return null;
  return n;
}

function canSave(form: Draft): boolean {
  if (form.minValue.trim() !== "" && intOf(form.minValue) == null) return false;
  if (form.maxValue.trim() !== "" && intOf(form.maxValue) == null) return false;
  const rows = [...form.ttft, ...form.otps];
  return rows.every((r) => r.threshold.trim() === "" || (TYPES.includes(r.type) && Number(r.threshold) >= 0));
}

function Select({
  value,
  options,
  disabled,
  className,
  onChange,
}: {
  value: string;
  options: string[];
  disabled?: boolean;
  className?: string;
  onChange: (v: string) => void;
}) {
  return (
    <select
      className={cn(
        "h-9 shrink-0 rounded-md border bg-background px-2 text-sm",
        "focus-visible:outline-2 focus-visible:outline-offset-1",
        "disabled:cursor-not-allowed disabled:opacity-60",
        className,
      )}
      value={value}
      disabled={disabled}
      onChange={(e) => onChange(e.target.value)}
    >
      {options.map((t) => (
        <option key={t}>{t}</option>
      ))}
    </select>
  );
}

// The unit sits inside the box it belongs to: a threshold of 20 means nothing
// until you know whether it is seconds or tokens per second.
function Metrics({
  label,
  unit,
  rows,
  disabled,
  onChange,
}: {
  label: string;
  unit: string;
  rows: MetricRow[];
  disabled: boolean;
  onChange: (rows: MetricRow[]) => void;
}) {
  return (
    <div className="space-y-2">
      {rows.length === 0 && (
        <p className="flex h-9 items-center text-sm text-muted-foreground">
          None — the scaler falls back to its own defaults.
        </p>
      )}
      {rows.map((row, i) => (
        <div key={i} className="flex items-center gap-2">
          <Select
            value={row.type}
            disabled={disabled}
            options={TYPES}
            className="w-32"
            onChange={(type) => onChange(rows.map((r, j) => (j === i ? { ...r, type } : r)))}
          />
          <div className="relative min-w-0 flex-1">
            <Input
              value={row.threshold}
              disabled={disabled}
              inputMode="decimal"
              placeholder="threshold"
              aria-label={`${label} threshold`}
              className="pr-14"
              onChange={(e) => onChange(rows.map((r, j) => (j === i ? { ...r, threshold: e.target.value } : r)))}
            />
            <span className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-xs text-muted-foreground">
              {unit}
            </span>
          </div>
          <Button
            variant="ghost"
            size="sm"
            className={cn(
              "size-8 shrink-0 px-0 text-muted-foreground hover:text-foreground",
              disabled && "invisible",
            )}
            aria-label={`Remove ${label} metric`}
            disabled={disabled}
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
          >
            <X className="size-4" />
          </Button>
        </div>
      ))}
      {!disabled && (
        <Button
          variant="ghost"
          size="sm"
          className="-ml-3 text-muted-foreground hover:text-foreground"
          onClick={() => onChange([...rows, { type: "p80", threshold: "" }])}
        >
          <Plus className="size-3.5" />
          Add metric
        </Button>
      )}
    </div>
  );
}
