import { useState } from "react";
import { Navigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ProfileEditor } from "@/components/ProfileEditor";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorState, Loading } from "@/components/States";
import { Code } from "@/components/ui/code";
import { toYaml } from "@/lib/yaml";

export function SiteProfile() {
  const [editing, setEditing] = useState(false);
  const { data, isPending, error } = useQuery({
    queryKey: ["profile"],
    queryFn: api.profile,
  });

  if (isPending) return <Loading what="the site profile" />;
  if (error) return <ErrorState what="the site profile" error={error} />;

  // The stored document does not parse, so there is nothing to show as parsed.
  // Setup is where it gets fixed -- it opens on the same text -- and the site
  // reports itself uninitialised anyway, which is the state this is.
  if (!data.profile) return <Navigate to="/setup" replace />;

  const p = data.profile;
  const auth = p.route?.auth;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Site profile</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            <Badge variant="outline">{p.name}</Badge>{" "}
            <span className="font-mono text-xs">{data.source}</span>
          </p>
          <p className="mt-2 text-sm text-muted-foreground">
            The cluster-shaped layer: what the public catalog cannot know and a deploy form
            should not have to retype. Shown as parsed, after defaults.
          </p>
        </div>
        <Button size="sm" variant="outline" onClick={() => setEditing(!editing)}>
          {editing ? "Done" : "Edit"}
        </Button>
      </div>

      {editing && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Edit</CardTitle>
            <p className="text-sm text-muted-foreground">
              swissd owns this document and writes it to{" "}
              <span className="font-mono text-xs">{data.source}</span>. It is checked before it
              is stored — a profile that does not parse would take the deploy form down.
            </p>
          </CardHeader>
          <CardContent>
            <ProfileEditor
              profile={data.profile}
              yaml={data.yaml ?? ""}
              submitLabel="Save profile"
              onSaved={() => setEditing(false)}
            />
          </CardContent>
        </Card>
      )}

      {!editing && (
        <>
          <Section
            title="Placement"
            hint="The scheduler and priority class are applied before the deploy form merges, so a deploy overrides either."
          >
            <Row label="Namespace" value={p.namespace} fallback="the chart's default" />
            <Row label="GPUs per node" value={p.nodes?.gpusPerNode} />
            <Row
              label="Scheduler"
              value={p.schedule?.schedulerName}
              fallback="the chart's default"
            />
            <Row
              label="Priority class"
              value={p.schedule?.priorityClassName}
              fallback="the chart's default"
            />
          </Section>

          <Section title="Charts and images">
            <Row label="Chart repo" value={p.chartRepo} fallback="local chart path" />
            <Row label="Chart path" value={p.chartPath} />
            <Row
              label="Registry mirror"
              value={p.registry?.mirror}
              fallback="catalog repository as-is"
            />
          </Section>

          <Section
            title="Model paths"
            hint="model.localPath is built from the catalog's source.hf; {{hf}}, {{org}}, {{name}} and {{model}} expand."
          >
            <Row label="Path template" value={p.model.pathTemplate} mono />
          </Section>

          {p.model.overrides && Object.keys(p.model.overrides).length > 0 && (
            <Section
              title="Path overrides"
              hint="Keyed by catalog model name, for weights that do not sit where the template says."
            >
              {Object.entries(p.model.overrides).map(([k, v]) => (
                <Row key={k} label={k} value={v} mono />
              ))}
            </Section>
          )}

          <Section title="Routing">
            <Row
              label="Gateway"
              value={p.route?.gateway}
              fallback="no public URL; the status page shows the route only"
            />
            <Row label="Openresty ConfigMap" value={p.route?.nginxConfigMap} />
            <Row label="Openresty Service" value={p.route?.nginxService} />
            <Row label="Openresty selector" value={p.route?.nginxSelector} />
            <Row label="Entrypoint port" value={p.route?.nginxPort ?? 8080} />
            <Row label="Monitor ConfigMap" value={p.route?.monitorConfigMap} />
          </Section>

          <Section
            title="Entrypoint auth"
            hint="Used only by the serving and health checks. The profile names a Secret; the key itself is never stored here or shown."
          >
            {auth?.secretRef ? (
              <>
                <Row label="Key header" value={auth.header || "Authorization"} mono />
                <Row
                  label="Key prefix"
                  value={auth.prefix ?? (auth.header ? "" : "Bearer ")}
                  mono
                  fallback="none"
                />
                <Row
                  label="Secret"
                  value={`${auth.secretRef} → ${auth.secretKey || "apiKey"}`}
                  mono
                />
              </>
            ) : (
              <p className="text-sm text-muted-foreground">
                No key configured; the checks call the entrypoint unauthenticated.
              </p>
            )}
            {auth?.headers && Object.keys(auth.headers).length > 0 && (
              <>
                <dt className="text-muted-foreground">Static headers</dt>
                <dd className="space-y-0.5">
                  {Object.entries(auth.headers).map(([k, v]) => (
                    <div key={k} className="font-mono text-xs">
                      {k}: {v}
                    </div>
                  ))}
                </dd>
              </>
            )}
          </Section>

          <Section title="Cache and scaler">
            <Row label="Cache" value={p.cache?.enabled ? "enabled" : "disabled"} />
            <Row label="Cache host path" value={p.cache?.hostPath} mono />
            <Row label="Scaler server" value={p.scaler?.serverAddress} />
          </Section>

          {p.sites && p.sites.length > 0 && (
            <Section
              title="Other sites"
              hint="The swissd instances in other clusters, for the switcher in the header."
            >
              {p.sites.map((s, i) => (
                <Row key={`${s.name}-${i}`} label={s.name} value={s.url} mono />
              ))}
            </Section>
          )}

          {p.extra && Object.keys(p.extra).length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Extra</CardTitle>
                <p className="text-sm text-muted-foreground">
                  Site values merged into every plan that no named field covers.
                </p>
              </CardHeader>
              <CardContent>
                <Code lang="yaml">{toYaml(p.extra)}</Code>
              </CardContent>
            </Card>
          )}
        </>
      )}
    </div>
  );
}

function Section({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{title}</CardTitle>
        {hint && <p className="text-sm text-muted-foreground">{hint}</p>}
      </CardHeader>
      <CardContent>
        <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[14rem_1fr]">{children}</dl>
      </CardContent>
    </Card>
  );
}

// An unset field shows its fallback rather than a dash, because "unset" here
// means a default is in force and the default is the useful part.
function Row({
  label,
  value,
  fallback,
  mono,
}: {
  label: string;
  value?: string | number;
  fallback?: string;
  mono?: boolean;
}) {
  const text = value === undefined || value === null || value === "" ? fallback : String(value);
  if (!text) return null;
  const unset = value === undefined || value === null || value === "";
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd
        className={
          (mono && !unset ? "font-mono text-xs " : "") +
          (unset ? "text-muted-foreground italic break-all" : "break-all")
        }
      >
        {text}
      </dd>
    </>
  );
}
