import { useState } from "react";
import { Plus, X } from "lucide-react";
import { type SiteProfile } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";

// ProfileForm edits the site profile field by field, which is how it is
// normally edited: the document is a fixed set of cluster facts, not free-form
// values, and every one of them has a name and a reason.
//
// It works on the parsed profile and sends it back as an object. swissd renders
// the YAML with the same library that reads it, so nothing here has to know how
// a path template or a header value needs quoting.
export function ProfileForm({
  value,
  onChange,
}: {
  value: SiteProfile;
  onChange: (p: SiteProfile) => void;
}) {
  // Every setter is a shallow copy of one section, so an untouched section
  // round-trips exactly as it arrived -- including the ones with no field here.
  const set = <K extends keyof SiteProfile>(key: K, v: SiteProfile[K]) =>
    onChange({ ...value, [key]: v });
  const setIn = <K extends keyof SiteProfile>(key: K, patch: Partial<SiteProfile[K]>) =>
    onChange({ ...value, [key]: { ...(value[key] as object), ...patch } as SiteProfile[K] });

  return (
    <div className="space-y-6">
      <Group
        title="Placement"
        hint="Where releases land. The scheduler and priority class are applied before the deploy form merges, so a deploy overrides either."
      >
        <Field label="Cluster name" hint="Recorded against every plan composed here.">
          <Input value={value.name} onChange={(e) => set("name", e.target.value)} />
        </Field>
        <Field label="Namespace" hint="Unless a deploy overrides it.">
          <Input
            value={value.namespace ?? ""}
            placeholder="the chart's default"
            onChange={(e) => set("namespace", e.target.value)}
          />
        </Field>
        <Field label="Scheduler">
          <Input
            value={value.schedule?.schedulerName ?? ""}
            placeholder="the chart's default"
            onChange={(e) => setIn("schedule", { schedulerName: e.target.value })}
          />
        </Field>
        <Field label="Priority class">
          <Input
            value={value.schedule?.priorityClassName ?? ""}
            placeholder="the chart's default"
            onChange={(e) => setIn("schedule", { priorityClassName: e.target.value })}
          />
        </Field>
        <Field label="GPUs per node" hint="A full GPU node, used to derive cache.maxSlotsPerNode.">
          <Input
            type="number"
            min={0}
            value={value.nodes?.gpusPerNode ?? ""}
            onChange={(e) => setIn("nodes", { gpusPerNode: num(e.target.value) })}
          />
        </Field>
        <Toggle
          label="Create namespace"
          hint="Off by default: under helm v4 this needs patch on namespaces cluster-wide, for namespaces an admin already created."
          checked={!!value.createNamespace}
          onChange={(v) => set("createNamespace", v)}
        />
      </Group>

      <Group title="Charts and images" hint="The registry the catalog deliberately does not name.">
        <Field label="Chart repo" hint="e.g. oci://harbor.example.com/charts. Empty means a local chart path.">
          <Input
            value={value.chartRepo ?? ""}
            onChange={(e) => set("chartRepo", e.target.value)}
          />
        </Field>
        <Field label="Chart path">
          <Input
            value={value.chartPath ?? ""}
            placeholder="unused when a repo is set"
            onChange={(e) => set("chartPath", e.target.value)}
          />
        </Field>
        <Field
          label="Registry mirror"
          hint="Replaces the host/org of an engine image. The catalog pins which build; this says where it is pulled from."
        >
          <Input
            value={value.registry?.mirror ?? ""}
            placeholder="the catalog's repository as-is"
            onChange={(e) => setIn("registry", { mirror: e.target.value })}
          />
        </Field>
      </Group>

      <Group
        title="Model paths"
        hint="Built from the catalog's source.hf. {{hf}} is the full repo id, {{org}} and {{name}} its halves, {{model}} the catalog entry name."
      >
        <Field label="Path template">
          <Input
            className="font-mono text-xs"
            value={value.model?.pathTemplate ?? ""}
            placeholder="/mnt/disk0/models/{{name}}"
            onChange={(e) => setIn("model", { pathTemplate: e.target.value })}
          />
        </Field>
        <Pairs
          label="Path overrides"
          hint="Keyed by catalog model name, for weights that do not sit where the template says. Real layouts are not uniform."
          keyLabel="model"
          valueLabel="path"
          value={value.model?.overrides}
          onChange={(overrides) => setIn("model", { overrides })}
        />
      </Group>

      <Group title="Routing" hint="The openresty entrypoint every model publishes a route on.">
        <Field
          label="Gateway"
          hint="Where callers reach openresty from outside. Display only — a model's URL is shown as <gateway>/<route>, while every check still calls the service in-cluster."
        >
          <Input
            value={value.route?.gateway ?? ""}
            placeholder="https://llm.example.com"
            onChange={(e) => setIn("route", { gateway: e.target.value })}
          />
        </Field>
        <Field
          label="Openresty ConfigMap"
          hint="ns/name. The shared document every model writes a key into; the route-collision check is scoped to it."
        >
          <Input
            value={value.route?.nginxConfigMap ?? ""}
            onChange={(e) => setIn("route", { nginxConfigMap: e.target.value })}
          />
        </Field>
        <Field label="Openresty Service" hint="ns/name, called by the serving and health checks.">
          <Input
            value={value.route?.nginxService ?? ""}
            onChange={(e) => setIn("route", { nginxService: e.target.value })}
          />
        </Field>
        <Field label="Openresty selector">
          <Input
            value={value.route?.nginxSelector ?? ""}
            onChange={(e) => setIn("route", { nginxSelector: e.target.value })}
          />
        </Field>
        <Field label="Entrypoint port">
          <Input
            type="number"
            min={1}
            value={value.route?.nginxPort ?? ""}
            placeholder="8080"
            onChange={(e) => setIn("route", { nginxPort: num(e.target.value) })}
          />
        </Field>
        <Field label="Monitor ConfigMap">
          <Input
            value={value.route?.monitorConfigMap ?? ""}
            onChange={(e) => setIn("route", { monitorConfigMap: e.target.value })}
          />
        </Field>
      </Group>

      <Group
        title="Entrypoint auth"
        hint="Used only by the serving and health checks. This document is a ConfigMap, so it names a Secret rather than holding the key — nothing here is a credential."
      >
        <Field label="Secret" hint="ns/name, or a bare name for a Secret in swissd's own namespace.">
          <Input
            value={value.route?.auth?.secretRef ?? ""}
            onChange={(e) => setAuth(value, onChange, { secretRef: e.target.value })}
          />
        </Field>
        <Field label="Secret key">
          <Input
            value={value.route?.auth?.secretKey ?? ""}
            placeholder="apiKey"
            onChange={(e) => setAuth(value, onChange, { secretKey: e.target.value })}
          />
        </Field>
        <Field label="Key header">
          <Input
            value={value.route?.auth?.header ?? ""}
            placeholder="Authorization"
            onChange={(e) => setAuth(value, onChange, { header: e.target.value })}
          />
        </Field>
        <Field
          label="Key prefix"
          hint="What precedes the key. Empty on a custom header is meaningful — X-Api-Key carries no scheme."
        >
          <Input
            value={value.route?.auth?.prefix ?? ""}
            placeholder={value.route?.auth?.header ? "none" : "Bearer "}
            onChange={(e) => setAuth(value, onChange, { prefix: e.target.value })}
          />
        </Field>
        <Pairs
          label="Static headers"
          hint="Sent on every call to the entrypoint. Not a place for credentials, for the same reason as above."
          keyLabel="header"
          valueLabel="value"
          value={value.route?.auth?.headers}
          onChange={(headers) => setAuth(value, onChange, { headers })}
        />
      </Group>

      <Group title="Cache and scaler">
        <Toggle
          label="Cache"
          hint="Off emits nothing under cache: — the section does not exist in every chart version, and a values file carrying an unknown key is rejected rather than ignored."
          checked={!!value.cache?.enabled}
          onChange={(enabled) => setIn("cache", { enabled })}
        />
        <Field label="Cache host path">
          <Input
            className="font-mono text-xs"
            value={value.cache?.hostPath ?? ""}
            onChange={(e) => setIn("cache", { hostPath: e.target.value })}
          />
        </Field>
        <Field label="Scaler server" hint="The decision server or the Prometheus query API, depending on the provider a deploy picks.">
          <Input
            value={value.scaler?.serverAddress ?? ""}
            onChange={(e) => setIn("scaler", { serverAddress: e.target.value })}
          />
        </Field>
        <Pairs
          label="Scaler headers"
          keyLabel="header"
          valueLabel="value"
          value={value.scaler?.serverHeaders}
          onChange={(serverHeaders) => setIn("scaler", { serverHeaders })}
        />
      </Group>
    </div>
  );
}

// Auth is two levels down, so it gets its own setter rather than a nested
// spread at every call site.
function setAuth(
  value: SiteProfile,
  onChange: (p: SiteProfile) => void,
  patch: Partial<NonNullable<NonNullable<SiteProfile["route"]>["auth"]>>,
) {
  onChange({
    ...value,
    route: { ...value.route, auth: { ...value.route?.auth, ...patch } },
  });
}

// An empty number field means "unset", not zero: a 0 here would be a real
// value the chart would act on.
function num(raw: string): number | undefined {
  const n = Number(raw);
  return raw.trim() === "" || Number.isNaN(n) ? undefined : n;
}

function Group({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-3">
      <div>
        <h3 className="text-sm font-semibold">{title}</h3>
        {hint && <p className="mt-0.5 text-xs text-muted-foreground">{hint}</p>}
      </div>
      <div className="grid gap-4 sm:grid-cols-2">{children}</div>
    </section>
  );
}

function Toggle({
  label,
  hint,
  checked,
  onChange,
}: {
  label: string;
  hint?: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-2">
        <Switch checked={checked} onChange={onChange} label={label} />
        <span className="text-sm font-medium">{label}</span>
      </div>
      {hint && <span className="block text-xs text-muted-foreground">{hint}</span>}
    </div>
  );
}

// Pairs edits a string map. Rows rather than a YAML box: these are two-column
// facts, and the one thing a free-text field adds here is a way to mistype the
// indentation.
function Pairs({
  label,
  hint,
  keyLabel,
  valueLabel,
  value,
  onChange,
}: {
  label: string;
  hint?: string;
  keyLabel: string;
  valueLabel: string;
  value?: Record<string, string>;
  onChange: (v: Record<string, string> | undefined) => void;
}) {
  // Kept as a list while editing: a map keyed by what is being typed loses the
  // row the moment two keys collide or one is briefly empty.
  const [rows, setRows] = useState<[string, string][]>(() => Object.entries(value ?? {}));

  const push = (next: [string, string][]) => {
    setRows(next);
    const out: Record<string, string> = {};
    for (const [k, v] of next) if (k.trim()) out[k] = v;
    onChange(Object.keys(out).length ? out : undefined);
  };

  return (
    <div className="space-y-1 sm:col-span-2">
      <span className="text-sm font-medium">{label}</span>
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}

      <div className="space-y-2 pt-1">
        {rows.map(([k, v], i) => (
          <div key={i} className="flex items-center gap-2">
            <Input
              value={k}
              placeholder={keyLabel}
              className="font-mono text-xs"
              onChange={(e) => push(rows.map((r, j) => (i === j ? [e.target.value, r[1]] : r)))}
            />
            <Input
              value={v}
              placeholder={valueLabel}
              className="font-mono text-xs"
              onChange={(e) => push(rows.map((r, j) => (i === j ? [r[0], e.target.value] : r)))}
            />
            <button
              type="button"
              aria-label={`Remove ${k || keyLabel}`}
              onClick={() => push(rows.filter((_, j) => j !== i))}
              className="shrink-0 text-muted-foreground hover:text-destructive"
            >
              <X className="size-4" />
            </button>
          </div>
        ))}
        <Button size="sm" variant="outline" onClick={() => setRows([...rows, ["", ""]])}>
          <Plus className="size-4" /> Add
        </Button>
      </div>
    </div>
  );
}
