import { useState } from "react";
import type { ClusterInfo, Plan, PlanRequest, Variant } from "@/lib/api";
import { cn } from "@/lib/utils";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";

// One form, two routes. Deploy composes it against a catalog model and Upgrade
// against a release's stored plan, but a setting that means one thing on one
// page and another on the other is how the two drift into disagreeing about
// what a deploy is.
export interface Toleration {
  key: string;
  operator: "Exists" | "Equal";
  value: string;
  effect: string;
}

export interface Form {
  edits: string;
  gpuProducts: string[];
  tolerations: Toleration[];
  priorityClassName: string;
  schedulerName: string;
  cart: boolean;
  modelRoute: boolean;
  slo: boolean;
  serviceMonitor: boolean;
  namespace: string;
  // createNamespace is composed into the plan as helmfile's createNamespace:
  // helm creates the namespace, and the plan is where that is declared. The
  // site profile may already say so, in which case this adds nothing.
  createNamespace: boolean;
  serviceId: string;
  localPath: string;
  scaler: boolean;
  replicaCount: string;
  route: string;
  // image is repository:tag for this deploy alone. Empty is the site's answer,
  // which is the mirror when one is configured -- the switch beside the field
  // is a view of this one value, so the two cannot name different registries.
  image: string;
  // Named model-route knobs. Empty number fields leave chart defaults.
  exposeRoutedPeer: boolean;
  backendMaxConcurrency: string;
  cartMaxLoad: string;
  ttftLimitMs: string;
  tpsLimitTps: string;
  adaptiveCc: boolean;
  adaptiveCcMin: string;
  monitor: boolean;
  monitorGpuType: string;
}

export const EMPTY: Form = {
  edits: "",
  gpuProducts: [],
  tolerations: [],
  priorityClassName: "",
  schedulerName: "",
  cart: true,
  modelRoute: false,
  slo: true,
  serviceMonitor: true,
  namespace: "",
  createNamespace: false,
  serviceId: "",
  localPath: "",
  scaler: false,
  replicaCount: "",
  route: "",
  image: "",
  exposeRoutedPeer: true,
  backendMaxConcurrency: "",
  cartMaxLoad: "",
  ttftLimitMs: "",
  tpsLimitTps: "",
  adaptiveCc: true,
  adaptiveCcMin: "",
  monitor: true,
  monitorGpuType: "",
};

export function DeploySettings({
  form,
  onChange,
  cluster,
  serviceIdPlaceholder,
  localPathDefault,
  localPathPlaceholder,
  image,
  supportedGPUs,
  clusterGPUs,
  // Upgrade locks the namespace: it names the helm release being upgraded, and
  // changing it does not move the release, it installs a second one.
  lockIdentity = false,
  // Rollback shows the form and lets nothing be typed into it: it re-applies an
  // archived plan verbatim, so an editable field beside it would promise a
  // change that the rollback would then not make.
  readOnly = false,
}: {
  form: Form;
  onChange: (patch: Partial<Form>) => void;
  cluster?: ClusterInfo;
  serviceIdPlaceholder?: string;
  // What the site's template resolves to for this model, and the template
  // itself for when it does not resolve. Only the first is a value Tab can fill
  // a field with.
  localPathDefault?: string;
  localPathPlaceholder?: string;
  // The engine image the catalog pins, and what the site's mirror rewrites it
  // to. Equal when this site mirrors nothing, which is when there is no rewrite
  // to offer a choice about.
  image?: { catalog: string; site: string };
  lockIdentity?: boolean;
  readOnly?: boolean;
  // Products the chosen variant declares, and those a node in this cluster
  // actually reports. The intersection is what is worth offering.
  supportedGPUs?: string[];
  clusterGPUs?: string[];
}) {
  const set =
    (k: keyof Form) =>
    (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
      onChange({ [k]: e.target.value } as Partial<Form>);
  const toggle = (k: keyof Form) => (v: boolean) => onChange({ [k]: v } as Partial<Form>);
  const on = effective(form);

  return (
    // One disabled fieldset rather than a disabled prop threaded through every
    // control: the browser already disables everything inside one, and a form
    // that is read-only in fifteen places is read-only in fourteen after the
    // next field is added.
    <fieldset disabled={readOnly} className="min-w-0 space-y-5">
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Deploy settings</CardTitle>
          <p className="text-sm text-muted-foreground">
            {readOnly
              ? "What this revision ran with, exactly as it ran. A rollback re-applies it verbatim — to change any of it, upgrade instead."
              : "Engine flags, probes and the image come from the catalog. Pick another variant to change those."}
          </p>
        </CardHeader>
        {/* One label column for every row, toggles included. Mixing
            label-above fields with label-beside checkboxes gives a form three
            alignment axes and no two rows that line up. */}
        {/* One label column for every row, switches included. Guidance lives
            in the box it applies to; the label carries the longer note for the
            rows whose control is a switch and has no box to put it in. */}
        {/* One label column for every row, switches included. Guidance lives
            in the box it applies to; the label carries the longer note for the
            rows whose control is a switch and has no box to put it in. */}
        <CardContent className="divide-y pt-0">
          {/* Locked on an upgrade, like the namespace: the route, the scaler
              and the SLO are all named after it, and changing it renames none
              of them -- helm renders a second set and orphans the first. The
              server refuses it either way; disabling the box is so nobody
              types a rename and reads the refusal as a bug. */}
          <Row
            label="Service ID"
            note={
              lockIdentity
                ? "fixed for the life of the release: the route, the scaler and the SLO are named after it"
                : "names the helm release, the route, the scaler and the SLO"
            }
          >
            <Suggest
              value={form.serviceId}
              onChange={(v) => onChange({ serviceId: v })}
              disabled={lockIdentity}
              suggestion={serviceIdPlaceholder}
              placeholder={serviceIdPlaceholder}
            />
          </Row>

          {/* The path and the switch that publishes it are one decision: a
              path typed under a disabled switch published nothing. */}
          <Row
            label="Route path"
            note="publishes a modelRoute to openresty and the monitor"
            toggle={
              <Switch
                checked={on.modelRoute}
                onChange={(v) =>
                  onChange(v ? { modelRoute: true } : { modelRoute: false, route: "" })
                }
                label="publish a route"
              />
            }
          >
            {on.modelRoute && (
              <PathInput
                value={form.route}
                onChange={(v) => onChange({ route: v })}
                suggestion={form.serviceId}
                placeholder={form.serviceId || "the service ID"}
              />
            )}
          </Row>

          <Row label="Model path" note="overrides the site's path template">
            <Suggest
              value={form.localPath}
              onChange={(v) => onChange({ localPath: v })}
              suggestion={localPathDefault}
              placeholder={localPathDefault ?? localPathPlaceholder}
            />
          </Row>

          <Row
            label="Autoscale"
            note="an LLMScaler owns the replica count, bounds included"
            toggle={
              <Switch checked={form.scaler} onChange={toggle("scaler")} label="autoscale" />
            }
          >
            {!form.scaler && (
              <Input
                className="w-32"
                value={form.replicaCount}
                onChange={set("replicaCount")}
                inputMode="numeric"
                aria-label="replicas"
                placeholder="replicas"
              />
            )}
          </Row>

          <Row
            label="Namespace"
            note={lockIdentity ? "moving a release between namespaces installs a second one" : undefined}
          >
            <Suggest
              value={form.namespace}
              onChange={(v) => onChange({ namespace: v })}
              disabled={lockIdentity}
              suggestion={cluster?.namespace}
              placeholder={cluster?.namespace ?? "the site profile's"}
            />
          </Row>

          {/* A property of the plan, not of the apply: it renders as
              helmDefaults.createNamespace in the helmfile the plan carries, and
              helm is what creates the namespace. Composing it here is what
              makes the rendered helmfile on the Plan tab the one that runs.

              Hidden on an upgrade: the live release is in that namespace
              already. */}
          {!lockIdentity && (
            <Row
              label="Create namespace"
              note="helm creates it during install — helmDefaults.createNamespace in the plan's helmfile"
              toggle={
                <Switch
                  checked={form.createNamespace}
                  onChange={(v) => onChange({ createNamespace: v })}
                  label="create the namespace"
                />
              }
            />
          )}

        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Routing options</CardTitle>
          <p className="text-sm text-muted-foreground">
            SLO-first latency and throughput: LLMSLORequirement declares ttft_metrics / tps_metrics for openresty.
            Static TTFT/TPS limits are fallback when nothing is declared. Empty number fields leave the chart default.
          </p>
        </CardHeader>
        <CardContent className="divide-y pt-0">
          <Row
            label="Expose routed peer"
            note="echoes the chosen backend as X-Routed-Peer — chart default on"
            toggle={
              <Switch
                checked={form.exposeRoutedPeer}
                onChange={toggle("exposeRoutedPeer")}
                label="expose routed peer"
              />
            }
          />

          <Row
            label="Backend concurrency"
            note="per-backend peer max concurrency — empty keeps the chart default"
          >
            <Input
              className="w-32"
              value={form.backendMaxConcurrency}
              onChange={set("backendMaxConcurrency")}
              inputMode="numeric"
              aria-label="backend concurrency"
              placeholder="omit"
            />
          </Row>

          <Row
            label="CART"
            note="Cache-aware router"
            toggle={
              <Switch checked={form.cart} onChange={toggle("cart")} label="CART" />
            }
          />

          <Row
            label="CART max load"
            note={
              form.cart
                ? "per-worker max load — empty keeps the chart default"
                : "enable CART to edit"
            }
            muted={!form.cart}
          >
            <Input
              className="w-32"
              value={form.cartMaxLoad}
              onChange={set("cartMaxLoad")}
              inputMode="numeric"
              aria-label="CART max load"
              placeholder="omit"
              disabled={!form.cart}
            />
          </Row>

          <Row
            label="SLO requirement"
            note="primary — LLMSLORequirement declares ttft_metrics / tps_metrics for openresty (beats static limits)"
            hint="Primary source for TTFT/TPS decisions. openresty priority: declared metrics (from this CR) > runtime override > static ttft_limit_ms / tps_limit_tps"
            caption="primary — declares ttft_metrics / tps_metrics for openresty"
            toggle={
              <Switch checked={form.slo} onChange={toggle("slo")} label="SLO requirement" />
            }
          />

          <Row
            label={form.slo ? "TTFT limit (fallback)" : "TTFT limit (ms)"}
            note={
              form.slo
                ? "static fallback — ttft_limit_ms only when SLO declares no ttft_metrics; empty omits"
                : "static threshold (SLO off) — empty omits"
            }
            hint={
              form.slo
                ? "Emergency static fallback. openresty: declared ttft_metrics > override > static ttft_limit_ms. Editable while SLO is on for bare-metal / no-metrics cases."
                : "Primary static TTFT threshold while SLO is off (no LLMSLORequirement → no declared metrics)."
            }
            caption={
              form.slo
                ? "static fallback when no declared ttft_metrics"
                : "static threshold — SLO is off"
            }
            muted={form.slo}
          >
            <Input
              className="w-32"
              value={form.ttftLimitMs}
              onChange={set("ttftLimitMs")}
              inputMode="numeric"
              aria-label="TTFT limit ms"
              placeholder="omit"
            />
          </Row>

          <Row
            label={form.slo ? "TPS limit (fallback)" : "TPS limit (tok/s)"}
            note={
              form.slo
                ? "static fallback — tps_limit_tps only when SLO declares no tps_metrics; empty omits"
                : "static threshold (SLO off) — empty omits"
            }
            hint={
              form.slo
                ? "Emergency static fallback. openresty: declared tps_metrics > override > static tps_limit_tps. AIMD (Adaptive concurrency) also reads the effective TPS threshold."
                : "Primary static TPS threshold while SLO is off (no LLMSLORequirement → no declared metrics)."
            }
            caption={
              form.slo
                ? "static fallback when no declared tps_metrics"
                : "static threshold — SLO is off"
            }
            muted={form.slo}
          >
            <Input
              className="w-32"
              value={form.tpsLimitTps}
              onChange={set("tpsLimitTps")}
              inputMode="numeric"
              aria-label="TPS limit tok/s"
              placeholder="omit"
            />
          </Row>

          <Row
            label="Adaptive concurrency"
            note="AIMD on the effective TPS signal — defaults on; stays next to TPS"
            toggle={
              <Switch
                checked={form.adaptiveCc}
                onChange={(v) =>
                  onChange(v ? { adaptiveCc: true } : { adaptiveCc: false, adaptiveCcMin: "" })
                }
                label="adaptive concurrency"
              />
            }
          />

          <Row
            label="Adaptive floor"
            note={
              form.adaptiveCc
                ? "adaptive_cc_min — empty omits"
                : "enable Adaptive concurrency to edit"
            }
            hint={
              form.adaptiveCc
                ? "adaptive_cc_min — AIMD concurrency floor (lower clamp); empty omits and openresty derives floor from static peer max × min_frac; only applies when Adaptive concurrency is on"
                : "enable Adaptive concurrency to edit"
            }
            muted={!form.adaptiveCc}
          >
            <Input
              className="w-32"
              value={form.adaptiveCcMin}
              onChange={set("adaptiveCcMin")}
              inputMode="numeric"
              aria-label="adaptive floor"
              placeholder="omit"
              disabled={!form.adaptiveCc}
            />
          </Row>
        </CardContent>
      </Card>

      {/* Folded away, but every flag is still written into the plan either
          way -- a section nobody opens must not hand the decision back to a
          chart default. */}
      <details className="rounded-lg border">
        <summary className="cursor-pointer px-4 py-3 text-sm font-medium">Advanced</summary>
        <div className="divide-y border-t px-4">
          <Row
            label="ServiceMonitor"
            note="Prometheus metrics scraped alongside the release"
            toggle={
              <Switch
                checked={form.serviceMonitor}
                onChange={toggle("serviceMonitor")}
                label="ServiceMonitor"
              />
            }
          />

          {/* Off is the answer almost always: the catalog pins the tag and the
              site rewrites the repository to its mirror, and between them the
              image is already decided. The switch is the escape hatch for the
              one deploy that has to pull something else -- a release-candidate
              engine build, or the original registry when the mirror is behind.

              Off empties the field, so turning it off puts the inherited answer
              back rather than leaving a stale override behind. */}
          {image && (
            <Row
              label="Image"
              note="repository:tag — off inherits the catalog's tag and the site's mirror"
              toggle={
                <Switch
                  checked={!!form.image.trim()}
                  onChange={(v) => onChange({ image: v ? image.site : "" })}
                  label="override"
                />
              }
            >
              {form.image.trim() ? (
                <Suggest
                  value={form.image}
                  onChange={(v) => onChange({ image: v })}
                  suggestion={image.catalog}
                  placeholder={image.site}
                />
              ) : (
                <span className="truncate font-mono text-xs text-muted-foreground" title={image.site}>
                  {image.site}
                </span>
              )}
            </Row>
          )}
          <Row label="GPU product" note="restricts scheduling to these products; any one will do">
            <Products
              selected={form.gpuProducts}
              onChange={(v) => onChange({ gpuProducts: v })}
              options={gpuOptions(supportedGPUs, clusterGPUs)}
            />
          </Row>
          <Row label="Tolerations" note="lets this deploy land on tainted GPU nodes">
            <Tolerations
              rows={form.tolerations}
              onChange={(v) => onChange({ tolerations: v })}
            />
          </Row>
          <Row label="Priority class">
            <Suggest
              value={form.priorityClassName}
              onChange={(v) => onChange({ priorityClassName: v })}
              suggestion={cluster?.priorityClassName}
              placeholder={cluster?.priorityClassName ?? "the chart's default"}
            />
          </Row>
          <Row label="Scheduler">
            <Suggest
              value={form.schedulerName}
              onChange={(v) => onChange({ schedulerName: v })}
              suggestion={cluster?.schedulerName}
              placeholder={cluster?.schedulerName ?? "the chart's default"}
            />
          </Row>

          <Row
            label="Monitor"
            note="writes backends into monitor's config"
            toggle={
              <Switch
                checked={form.monitor}
                onChange={toggle("monitor")}
                label="monitor"
              />
            }
          >
            {form.monitor && (
              <Suggest
                value={form.monitorGpuType}
                onChange={(v) => onChange({ monitorGpuType: v })}
                placeholder="GPU type (optional)"
              />
            )}
          </Row>
        </div>

      </details>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Plan editor</CardTitle>
          <p className="text-sm text-muted-foreground">
            Any values key, applied after every layer and exempt from layer ownership. Shown as
            its own <span className="font-medium">edit</span> layer in the composed plan, so it
            is never invisible.
          </p>
        </CardHeader>
        <CardContent>
          <textarea
            value={form.edits}
            onChange={set("edits")}
            spellCheck={false}
            rows={10}
            placeholder={EDITS_PLACEHOLDER}
            className="w-full rounded-md border bg-background px-3 py-2 font-mono text-xs"
          />
        </CardContent>
      </Card>
    </fieldset>
  );
}

// Three columns: label, switch, control. The switch column is reserved so a
// switch beside an input never shoves that input sideways relative to rows
// without one. Switch-only rows put the switch in the control column instead,
// so toggles and number fields share one start edge (Routing options especially
// alternates the two). The control column keeps the input's height either way,
// so toggling a row grows it sideways and never moves what is below.
function Row({
  label,
  note,
  hint,
  caption,
  toggle,
  children,
  muted,
}: {
  label: string;
  note?: string;
  // Longer hover text when note stays short (or when note is absent).
  hint?: string;
  // Optional visible subline under the label (Routing SLO/fallback rows).
  caption?: string;
  toggle?: React.ReactNode;
  children?: React.ReactNode;
  muted?: boolean;
}) {
  // undefined children = switch-only row: put the switch in the control
  // column so it shares a start edge with number inputs. false/element means
  // the row owns a control slot (even when conditionally empty), so the switch
  // stays in its reserved column and does not jump when the input appears.
  const hasControl = children !== undefined;
  return (
    <div
      className={cn(
        "grid gap-x-3 gap-y-1 py-2.5 sm:grid-cols-[11rem_2.25rem_minmax(0,1fr)] sm:items-center",
        muted && "opacity-60",
      )}
    >
      <div className="min-w-0" title={hint ?? note}>
        <div className="text-sm font-medium">{label}</div>
        {caption ? (
          <p className="mt-0.5 text-[11px] leading-snug text-muted-foreground">{caption}</p>
        ) : null}
      </div>
      <div className="flex min-h-9 items-center">{hasControl ? toggle : null}</div>
      <div className="flex min-h-9 min-w-0 items-center">
        {hasControl ? children : toggle}
      </div>
    </div>
  );
}

// The products worth offering are those the variant declares and a node in
// this cluster reports. Either list alone is a guess: the catalog does not know
// what is racked here, and the cluster does not know what the model supports.
function tolerationsOf(o: Record<string, unknown>): Toleration[] {
  if (!Array.isArray(o.tolerations)) return [];
  return o.tolerations.flatMap((t): Toleration[] => {
    if (!t || typeof t !== "object") return [];
    const r = t as Record<string, unknown>;
    if (typeof r.key !== "string" || !r.key) return [];
    return [
      {
        key: r.key,
        operator: r.operator === "Equal" ? "Equal" : "Exists",
        value: typeof r.value === "string" ? r.value : "",
        effect: typeof r.effect === "string" ? r.effect : "",
      },
    ];
  });
}

// The products sit in a required nodeAffinity term under a vendor-specific
// label key, so match the key by suffix rather than guessing the vendor.
function gpuProductsOf(o: Record<string, unknown>): string[] {
  const dig = (v: unknown, k: string): unknown =>
    v && typeof v === "object" ? (v as Record<string, unknown>)[k] : undefined;
  const req = dig(
    dig(o.affinity, "nodeAffinity"),
    "requiredDuringSchedulingIgnoredDuringExecution",
  );
  const terms = dig(req, "nodeSelectorTerms");
  if (!Array.isArray(terms)) return [];
  for (const term of terms) {
    const exprs = dig(term, "matchExpressions");
    if (!Array.isArray(exprs)) continue;
    for (const e of exprs) {
      const key = dig(e, "key");
      const vals = dig(e, "values");
      if (
        typeof key === "string" &&
        /\.product$|device-id$|ascend910$/i.test(key) &&
        Array.isArray(vals)
      ) {
        return vals.filter((v): v is string => typeof v === "string");
      }
    }
  }
  return [];
}

function gpuOptions(supported?: string[], present?: string[]): string[] {
  const has = new Set(present?.filter(Boolean) ?? []);
  if (!supported?.length) return [...has].sort();
  const both = supported.filter((p) => has.has(p));
  return (both.length ? both : supported).slice().sort();
}

// Tab fills an empty field with the default it is showing, the way a shell
// completes: the field is no longer empty afterwards, so the next Tab moves on.
function accept(
  e: React.KeyboardEvent,
  suggestion: string | undefined,
  onChange: (v: string) => void,
) {
  if (e.key !== "Tab" || e.shiftKey || e.altKey || e.ctrlKey || e.metaKey || !suggestion) return;
  e.preventDefault();
  onChange(suggestion);
}

function useSuggestion(value: string, suggestion?: string, disabled?: boolean) {
  const [focused, setFocused] = useState(false);
  const offer = !value && !disabled ? suggestion : undefined;
  return {
    offer,
    hint: !!offer && focused,
    focus: { onFocus: () => setFocused(true), onBlur: () => setFocused(false) },
  };
}

function TabHint() {
  return (
    <kbd className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 rounded border px-1 py-px text-[10px] leading-none text-muted-foreground">
      tab
    </kbd>
  );
}

function Suggest({
  value,
  onChange,
  suggestion,
  placeholder,
  disabled,
}: {
  value: string;
  onChange: (v: string) => void;
  suggestion?: string;
  placeholder?: string;
  disabled?: boolean;
}) {
  const s = useSuggestion(value, suggestion, disabled);
  return (
    <div className="relative min-w-0 flex-1">
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => accept(e, s.offer, onChange)}
        disabled={disabled}
        placeholder={placeholder}
        className={cn(s.hint && "pr-11")}
        {...s.focus}
      />
      {s.hint && <TabHint />}
    </div>
  );
}

// The slash is drawn, never stored: the chart wants the path without it, and a
// leading one typed or pasted here is stripped rather than sent.
function PathInput({
  value,
  onChange,
  suggestion,
  placeholder,
}: {
  value: string;
  onChange: (v: string) => void;
  suggestion?: string;
  placeholder?: string;
}) {
  const s = useSuggestion(value, suggestion);
  return (
    <div className="relative flex h-9 min-w-0 flex-1 items-center rounded-md border bg-background pl-3 focus-within:outline-2 focus-within:outline-offset-1">
      <span className="select-none text-sm text-muted-foreground">/</span>
      <input
        className={cn(
          "h-full min-w-0 flex-1 bg-transparent px-1 text-sm outline-none placeholder:text-muted-foreground",
          s.hint && "pr-11",
        )}
        value={value}
        onChange={(e) => onChange(e.target.value.replace(/^\/+/, ""))}
        onKeyDown={(e) => accept(e, s.offer, onChange)}
        placeholder={placeholder}
        {...s.focus}
      />
      {s.hint && <TabHint />}
    </div>
  );
}


function nginxValuesMap(v: unknown): Record<string, string> {
  if (!v || typeof v !== "object" || Array.isArray(v)) return {};
  const out: Record<string, string> = {};
  for (const [key, val] of Object.entries(v as Record<string, unknown>)) {
    if (val === undefined || val === null) continue;
    out[key] = String(val);
  }
  return out;
}

function backendMaxConcurrencyOf(v: unknown): string {
  if (!Array.isArray(v)) return "";
  for (const p of v) {
    if (!p || typeof p !== "object") continue;
    const row = p as Record<string, unknown>;
    if (row.use === "backend" && row.maxConcurrency !== undefined && row.maxConcurrency !== null) {
      return String(row.maxConcurrency);
    }
  }
  return "";
}

function truthyStr(v: string | undefined, fallback: boolean): boolean {
  if (v === undefined || v === "") return fallback;
  return v !== "false" && v !== "0";
}

const EFFECTS = ["", "NoSchedule", "PreferNoSchedule", "NoExecute"];

// Structured rows rather than a YAML box: a toleration is four fields, and
// three of them are closed sets.
function Tolerations({
  rows,
  onChange,
}: {
  rows: Toleration[];
  onChange: (v: Toleration[]) => void;
}) {
  const patch = (i: number, p: Partial<Toleration>) =>
    onChange(rows.map((r, j) => (i === j ? { ...r, ...p } : r)));

  return (
    <div className="w-full space-y-2">
      {rows.map((r, i) => (
        <div key={i} className="flex flex-wrap items-center gap-2">
          <Input
            className="w-44"
            value={r.key}
            onChange={(e) => patch(i, { key: e.target.value })}
            placeholder="taint key"
            aria-label="taint key"
          />
          <Select
            value={r.operator}
            onChange={(v) => patch(i, { operator: v as Toleration["operator"] })}
            options={["Exists", "Equal"]}
            label="operator"
          />
          {r.operator === "Equal" && (
            <Input
              className="w-32"
              value={r.value}
              onChange={(e) => patch(i, { value: e.target.value })}
              placeholder="value"
              aria-label="taint value"
            />
          )}
          <Select
            value={r.effect}
            onChange={(v) => patch(i, { effect: v })}
            options={EFFECTS}
            label="effect"
            emptyLabel="any effect"
          />
          <button
            type="button"
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
            aria-label="remove toleration"
            className="text-sm text-muted-foreground hover:text-destructive"
          >
            remove
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={() => onChange([...rows, { key: "", operator: "Exists", value: "", effect: "" }])}
        className="text-sm text-muted-foreground underline hover:text-foreground"
      >
        Add toleration
      </button>
    </div>
  );
}

function Select({
  value,
  onChange,
  options,
  label,
  emptyLabel,
}: {
  value: string;
  onChange: (v: string) => void;
  options: string[];
  label: string;
  emptyLabel?: string;
}) {
  return (
    <select
      aria-label={label}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="h-9 rounded-md border bg-background px-2 text-sm"
    >
      {options.map((o) => (
        <option key={o} value={o}>
          {o || (emptyLabel ?? "any")}
        </option>
      ))}
    </select>
  );
}

function Products({
  selected,
  onChange,
  options,
}: {
  selected: string[];
  onChange: (v: string[]) => void;
  options: string[];
}) {
  if (options.length === 0) {
    return (
      <span className="text-xs text-muted-foreground">
        no GPU product reported by this cluster
      </span>
    );
  }
  return (
    <div className="flex flex-wrap gap-1.5">
      {options.map((o) => {
        const active = selected.includes(o);
        return (
          <button
            key={o}
            type="button"
            aria-pressed={active}
            onClick={() =>
              onChange(active ? selected.filter((p) => p !== o) : [...selected, o])
            }
            className={
              active
                ? "rounded-full border border-foreground px-2.5 py-1 text-xs font-medium"
                : "rounded-full border px-2.5 py-1 text-xs text-muted-foreground hover:text-foreground"
            }
          >
            {o}
          </button>
        );
      })}
      {selected.length === 0 && (
        <span className="self-center text-xs text-muted-foreground">
          none selected — any product the variant supports
        </span>
      )}
    </div>
  );
}

const EDITS_PLACEHOLDER = `# last word, any key
nodeSelector:
  nvidia.com/gpu.product: NVIDIA-B300-SXM6-AC
tolerations:
  - key: gpu
    operator: Exists
    effect: NoSchedule
resources:
  limits:
    rdma/hca_shared: "1"
extraArgs:
  - --tp-size=4`;

const num = (v: string) => (v.trim() === "" ? undefined : Number(v));

// imageOf pairs the image the catalog pins with the one the site mirrors it to,
// both as repository:tag. Undefined when the variant carries no image: there is
// then nothing to show and nothing to choose between.
//
// The site rewrites only the repository -- the tag is the catalog's, and is
// carried through -- so the same tag goes on both halves.
export function imageOf(
  variant?: Variant,
  mirrored?: Record<string, string>,
): { catalog: string; site: string } | undefined {
  const repo = variant?.image?.repository;
  if (!repo) return undefined;
  const tag = variant.image?.tag ?? "";
  return {
    catalog: joinImage(repo, tag),
    site: joinImage(mirrored?.[variant.id] ?? repo, tag),
  };
}

// splitImage turns what was typed into the two values the chart takes. The last
// colon separates the tag only when it comes after the last slash:
// harbor.example.com:5000/sglang is a registry port, not a tag.
//
// Only repository and tag. A digest pin is not expressible here on purpose --
// it would need a third field and the plan editor already takes one.
export function splitImage(ref: string): { repository: string; tag?: string } {
  const s = ref.trim();
  const colon = s.lastIndexOf(":");
  if (colon === -1 || colon < s.lastIndexOf("/")) return { repository: s };
  return { repository: s.slice(0, colon), tag: s.slice(colon + 1) };
}

function joinImage(repository: string, tag: string): string {
  return repository && tag ? `${repository}:${tag}` : repository;
}

// Naming a route turns routing on, so the toggle and the request cannot
// disagree about what this deploy is asking for.
export function effective(f: Form) {
  return { modelRoute: f.modelRoute || f.route.trim() !== "" };
}

export function planRequest(
  f: Form,
  opts: { model: string; version?: string; variant?: string; fromRelease?: string },
): PlanRequest {
  const overrides: Record<string, unknown> = {};
  const on = effective(f);

  // replicaCount and the scaler bounds are the same decision, so only the one
  // that applies is sent: a fixed count under a live scaler is two answers to
  // one question, and the scaler wins at a time nobody chose.
  if (f.scaler) {
    overrides.scaler = { enabled: true };
  } else {
    overrides.scaler = { enabled: false };
    if (num(f.replicaCount) !== undefined) overrides.replicaCount = num(f.replicaCount);
  }

  // Every flag is sent explicitly. Leaving one out would hand the decision to a
  // chart default, which is the thing this is here to avoid.
  overrides.cart = { enabled: f.cart };
  overrides.sloRequirement = { enabled: f.slo };
  overrides.serviceMonitor = { enabled: f.serviceMonitor };

  const route = f.route.trim();
  const modelRoute: Record<string, unknown> = { enabled: on.modelRoute };
  const nginx: Record<string, unknown> = {};
  if (route) nginx.route = route;

  const nginxValues: Record<string, string> = {
    expose_routed_peer: f.exposeRoutedPeer ? "true" : "false",
  };
  if (f.ttftLimitMs.trim()) nginxValues.ttft_limit_ms = f.ttftLimitMs.trim();
  if (f.tpsLimitTps.trim()) nginxValues.tps_limit_tps = f.tpsLimitTps.trim();
  nginxValues.adaptive_cc = f.adaptiveCc ? "true" : "false";
  if (f.adaptiveCc && f.adaptiveCcMin.trim()) nginxValues.adaptive_cc_min = f.adaptiveCcMin.trim();
  nginx.values = nginxValues;

  const maxConc = num(f.backendMaxConcurrency);
  if (maxConc !== undefined) {
    // Lists replace, so keep the chart's two-tier shape and only change backend.
    nginx.peers = [
      { use: "backend", priority: 2, maxConcurrency: maxConc },
      { use: "backend-svc", priority: 1 },
    ];
  }
  if (Object.keys(nginx).length) modelRoute.nginx = nginx;

  const maxLoad = num(f.cartMaxLoad);
  if (f.cart && maxLoad !== undefined) modelRoute.cart = { maxLoad };

  // modelRoute.monitor.nginx / .router: chart values.schema.json allows
  // object|null only (not boolean). Chart defaults are null (= autoconfig
  // default on when nginx.service / cart are set). Omit those keys so helm
  // keeps null — do not write form booleans (deploy fails schema) and do not
  // invent an object shape the template does not consume.
  const monitor: Record<string, unknown> = { enabled: f.monitor };
  if (f.monitor && f.monitorGpuType.trim()) monitor.gpuType = f.monitorGpuType.trim();
  modelRoute.monitor = monitor;

  overrides.modelRoute = modelRoute;

  // A row with no key tolerates nothing; it is a half-typed row, not a value.
  const tolerations = f.tolerations
    .filter((t) => t.key.trim())
    .map((t) => {
      const out: Record<string, unknown> = { key: t.key.trim(), operator: t.operator };
      if (t.operator === "Equal" && t.value.trim()) out.value = t.value.trim();
      if (t.effect) out.effect = t.effect;
      return out;
    });
  if (tolerations.length) overrides.tolerations = tolerations;

  // Only the repository: the tag and digest are the catalog's, and a form that
  // wrote the whole image section would drop them.
  // The tag lands in the form layer, which merges after the catalog's -- so it
  // wins, and the plan reports it as shadowed rather than the two disagreeing
  // quietly.
  if (f.image.trim()) overrides.image = splitImage(f.image);

  if (f.priorityClassName.trim()) overrides.priorityClassName = f.priorityClassName.trim();
  if (f.schedulerName.trim()) overrides.schedulerName = f.schedulerName.trim();

  return {
    model: opts.model,
    fromRelease: opts.fromRelease,
    version: opts.version || undefined,
    editsYAML: f.edits.trim() || undefined,
    gpuProducts: f.gpuProducts.length ? f.gpuProducts : undefined,
    variant: opts.variant || undefined,
    release: f.serviceId.trim() || undefined,
    namespace: f.namespace.trim() || undefined,
    createNamespace: f.createNamespace || undefined,
    serviceId: f.serviceId.trim() || undefined,
    localPath: f.localPath.trim() || undefined,
    overrides,
  };
}

// formFromPlan seeds the upgrade form from what a release was deployed with.
// The plan stores the composed overrides tree, not the form fields, so this is
// the inverse of planRequest -- and the pair is why an upgrade can show the
// settings rather than only promise it carried them forward.
export function formFromPlan(plan: Plan): Form {
  // The form's own document, not the composed result: an upgrade seeds from
  // what the operator set, so recomposing against a newer catalog keeps it.
  const o = (plan.layers?.form ?? {}) as Record<string, unknown>;
  const at = (path: string): unknown =>
    path.split(".").reduce<unknown>((acc, k) => {
      if (acc && typeof acc === "object" && k in (acc as Record<string, unknown>)) {
        return (acc as Record<string, unknown>)[k];
      }
      return undefined;
    }, o);

  const str = (path: string) => {
    const v = at(path);
    return v === undefined || v === null ? "" : String(v);
  };
  const bool = (path: string, fallback = false) => {
    const v = at(path);
    return typeof v === "boolean" ? v : fallback;
  };
  const nv = nginxValuesMap(at("modelRoute.nginx.values"));

  return {
    ...EMPTY,
    namespace: plan.release.namespace,
    serviceId: str("serviceId"),
    localPath: str("model.localPath"),
    scaler: bool("scaler.enabled"),
    // The product sits under the vendor's own label key, so find it by suffix
    // rather than guessing which vendor this variant was built for.
    gpuProducts: gpuProductsOf(o),
    tolerations: tolerationsOf(o),
    replicaCount: str("replicaCount"),
    route: str("modelRoute.nginx.route"),
    image: joinImage(str("image.repository"), str("image.tag")),
    cart: bool("cart.enabled", true),
    modelRoute: bool("modelRoute.enabled"),
    slo: bool("sloRequirement.enabled", true),
    serviceMonitor: bool("serviceMonitor.enabled"),
    priorityClassName: str("priorityClassName"),
    schedulerName: str("schedulerName"),
    exposeRoutedPeer: truthyStr(nv.expose_routed_peer, true),
    ttftLimitMs: nv.ttft_limit_ms ?? "",
    tpsLimitTps: nv.tps_limit_tps ?? "",
    adaptiveCc: truthyStr(nv.adaptive_cc, true),
    adaptiveCcMin: nv.adaptive_cc_min ?? "",
    backendMaxConcurrency: backendMaxConcurrencyOf(at("modelRoute.nginx.peers")),
    cartMaxLoad: str("modelRoute.cart.maxLoad"),
    monitor: bool("modelRoute.monitor.enabled", true),
    monitorGpuType: str("modelRoute.monitor.gpuType"),
    edits:
      plan.layers?.edit && Object.keys(plan.layers.edit).length > 0
        ? toYamlish(plan.layers.edit)
        : "",
  };
}

// The plan editor round-trips through text, so edits carried forward have to be
// rendered back to YAML. Display-grade is enough: the server re-parses it.
function toYamlish(tree: Record<string, unknown>): string {
  return yamlLines(tree, 0).join("\n");
}

function yamlLines(value: Record<string, unknown>, indent: number): string[] {
  const pad = " ".repeat(indent);
  const out: string[] = [];
  for (const [k, v] of Object.entries(value)) {
    if (Array.isArray(v)) {
      out.push(`${pad}${k}:`);
      for (const item of v) {
        if (item && typeof item === "object") {
          const nested = yamlLines(item as Record<string, unknown>, indent + 4);
          out.push(`${pad}  - ${nested[0].trim()}`, ...nested.slice(1));
        } else {
          out.push(`${pad}  - ${scalar(item)}`);
        }
      }
    } else if (v && typeof v === "object") {
      out.push(`${pad}${k}:`, ...yamlLines(v as Record<string, unknown>, indent + 2));
    } else {
      out.push(`${pad}${k}: ${scalar(v)}`);
    }
  }
  return out;
}

function scalar(v: unknown): string {
  if (typeof v === "string" && (v === "" || /[:#{}[\],&*?|<>=!%@`"']/.test(v))) {
    return JSON.stringify(v);
  }
  return String(v);
}
