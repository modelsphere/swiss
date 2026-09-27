import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import { deployApi, type SLOBound, type SLOConfig, type SLOMetric, type SLOSection } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { ErrorState, Loading } from "@/components/States";
import { cn } from "@/lib/utils";

const TYPES = ["avg", "p50", "p80", "p90", "p95", "p99"];

// Talks to slo-api /config/{serviceId}. The chart's object is often only
// serviceId plus minimumDeployment of one replica; everything else here is
// optional and is what a save adds.
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

  const save = useMutation({
    mutationFn: () => deployApi.saveSLO(namespace, release, editOf(form!)),
    onSuccess: (next) => {
      qc.setQueryData(["slo", namespace, release], next);
      setForm(draftOf(next));
    },
  });
  const reset = useMutation({
    mutationFn: () => deployApi.resetSLO(namespace, release),
    onSuccess: (next) => {
      qc.setQueryData(["slo", namespace, release], next);
      setForm(draftOf(next));
    },
  });

  if (slo.isPending || !form) return <Loading what="SLO" />;
  if (slo.error) return <ErrorState what="SLO" error={slo.error} />;

  const data = slo.data!;
  const busy = save.isPending || reset.isPending;

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-3 space-y-0">
        <CardTitle className="text-base">SLO</CardTitle>
        {canEdit && (
          <div className="flex shrink-0 gap-2">
            <Button variant="outline" size="sm" onClick={() => reset.mutate()} disabled={busy || !data.found}>
              {reset.isPending ? "Resetting…" : "Reset"}
            </Button>
            <Button size="sm" onClick={() => save.mutate()} disabled={busy || !canSave(form)}>
              {save.isPending ? "Saving…" : "Save"}
            </Button>
          </div>
        )}
      </CardHeader>
      <CardContent className="space-y-5">
        {!data.found && (
          <p className="text-sm text-muted-foreground">No requirement for {data.route || release} is registered yet.</p>
        )}
        {!canEdit && <p className="text-sm text-muted-foreground">This swissd is read-only.</p>}
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="Priority" hint="Integer 0–10. The installed object is usually 0.">
            <Input
              value={form.priority}
              disabled={!canEdit}
              inputMode="numeric"
              onChange={(e) => setForm({ ...form, priority: e.target.value })}
            />
          </Field>
          <Field label="Minimum" hint="Replicas, or concurrency.">
            <div className="flex gap-2">
              <Select
                value={form.minType}
                disabled={!canEdit}
                options={["replica", "concurrency"]}
                onChange={(minType) => setForm({ ...form, minType })}
              />
              <Input
                value={form.minValue}
                disabled={!canEdit}
                inputMode="numeric"
                className="min-w-0 flex-1"
                onChange={(e) => setForm({ ...form, minValue: e.target.value })}
              />
            </div>
          </Field>
          <Field label="Maximum" hint="Replicas. Empty leaves it unset.">
            <Input
              value={form.maxValue}
              disabled={!canEdit}
              inputMode="numeric"
              placeholder="none"
              onChange={(e) => setForm({ ...form, maxValue: e.target.value })}
            />
          </Field>
        </div>
        <div className="grid gap-4 lg:grid-cols-2">
          <Metrics
            label="TTFT"
            hint="Seconds. A ceiling — above this is a violation."
            rows={form.ttft}
            disabled={!canEdit}
            onChange={(ttft) => setForm({ ...form, ttft })}
          />
          <Metrics
            label="OTPS"
            hint="tok/s. A floor — below this is a violation."
            rows={form.otps}
            disabled={!canEdit}
            onChange={(otps) => setForm({ ...form, otps })}
          />
        </div>
        {save.error && <ErrorState what="the SLO" error={save.error} />}
        {reset.error && <ErrorState what="the reset" error={reset.error} />}
      </CardContent>
    </Card>
  );
}

interface MetricRow {
  type: string;
  threshold: string;
}

interface Draft {
  priority: string;
  minType: string;
  minValue: string;
  maxValue: string;
  ttft: MetricRow[];
  otps: MetricRow[];
}

function draftOf(data: SLOConfig): Draft {
  const priority = data.priority ?? (data.highPriority ? 10 : 0);
  return {
    priority: String(priority),
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
    priority?: number;
    minimumDeployment?: SLOBound;
    maximumDeployment?: SLOBound;
    ttft?: SLOSection;
    otps?: SLOSection;
  } = {};
  const priority = intIn(form.priority, 0, 10);
  if (priority != null) body.priority = priority;
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
  if (intIn(form.priority, 0, 10) == null) return false;
  if (form.minValue.trim() !== "" && intOf(form.minValue) == null) return false;
  if (form.maxValue.trim() !== "" && intOf(form.maxValue) == null) return false;
  const rows = [...form.ttft, ...form.otps];
  return rows.every((r) => r.threshold.trim() === "" || (TYPES.includes(r.type) && Number(r.threshold) >= 0));
}

function Field({ label, hint, children }: { label: string; hint: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="text-sm font-medium">{label}</span>
      {children}
      <span className="block text-[11px] leading-snug text-muted-foreground">{hint}</span>
    </label>
  );
}

function Select({
  value,
  options,
  disabled,
  onChange,
}: {
  value: string;
  options: string[];
  disabled?: boolean;
  onChange: (v: string) => void;
}) {
  return (
    <select
      className="h-9 rounded-md border bg-background px-2 text-sm focus-visible:outline-2 focus-visible:outline-offset-1 disabled:opacity-50"
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

function Metrics({
  label,
  hint,
  rows,
  disabled,
  onChange,
}: {
  label: string;
  hint: string;
  rows: MetricRow[];
  disabled: boolean;
  onChange: (rows: MetricRow[]) => void;
}) {
  return (
    <div className="space-y-1.5">
      <div className="text-sm font-medium">{label}</div>
      {rows.length === 0 && <p className="text-sm text-muted-foreground">None.</p>}
      {rows.map((row, i) => (
        <div key={i} className="flex items-center gap-2">
          <Select
            value={row.type}
            disabled={disabled}
            options={TYPES}
            onChange={(type) => onChange(rows.map((r, j) => (j === i ? { ...r, type } : r)))}
          />
          <Input
            value={row.threshold}
            disabled={disabled}
            inputMode="decimal"
            placeholder="threshold"
            aria-label={`${label} threshold`}
            className="min-w-0 flex-1"
            onChange={(e) => onChange(rows.map((r, j) => (j === i ? { ...r, threshold: e.target.value } : r)))}
          />
          {!disabled && (
            <button
              type="button"
              aria-label={`Remove ${label} metric`}
              className={cn(
                "inline-flex size-9 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted",
              )}
              onClick={() => onChange(rows.filter((_, j) => j !== i))}
            >
              <X className="size-4" />
            </button>
          )}
        </div>
      ))}
      <p className="text-[11px] leading-snug text-muted-foreground">{hint}</p>
      {!disabled && (
        <button
          type="button"
          className="text-sm text-muted-foreground hover:text-foreground"
          onClick={() => onChange([...rows, { type: "p80", threshold: "" }])}
        >
          Add metric
        </button>
      )}
    </div>
  );
}
