import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, CircleCheck, CircleX, TriangleAlert } from "lucide-react";
import {
  api,
  deployApi,
  type ChatApi,
  type ChatResult,
  type EntrypointAuth,
  type PlanStatus,
  type ReleaseStatus as Status,
} from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { AuthFields, SentHeaders } from "@/components/EntrypointAuth";
import { Provenance } from "@/components/Provenance";
import { ReleaseStatus } from "@/components/ReleaseStatus";
import { ErrorState, Loading } from "@/components/States";

export function DeploymentDetail() {
  const { namespace = "", release = "" } = useParams();
  const cluster = useQuery({ queryKey: ["cluster"], queryFn: api.cluster });

  const status = useQuery({
    queryKey: ["status", namespace, release],
    queryFn: () => api.status(namespace, release),
    refetchInterval: 15_000,
  });
  // A release swiss did not deploy has no plan beside it. That is the untracked
  // row, and it is not an error here -- the page just shows less.
  const plan = useQuery({
    queryKey: ["releasePlan", namespace, release],
    queryFn: () => api.releasePlan(namespace, release),
    retry: false,
  });

  if (status.isPending) return <Loading what={release} />;
  if (status.error) return <ErrorState what={release} error={status.error} />;

  const s = status.data;

  return (
    <div className="space-y-5">
      <Link
        to="/"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ChevronLeft className="size-4" /> Deployments
      </Link>

      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">{release}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            <Badge variant="outline">{namespace}</Badge>{" "}
            {plan.data ? (
              <>
                {plan.data.source.model}
                {plan.data.source.version && ` v${plan.data.source.version}`} ·{" "}
                {plan.data.source.variant} · {plan.data.chart.name}-{plan.data.chart.version}
              </>
            ) : (
              <Badge variant="warning">untracked — no plan beside this release</Badge>
            )}
          </p>
        </div>
        <div className="flex gap-2">
          <Link
            to={`/runs?namespace=${encodeURIComponent(namespace)}&release=${encodeURIComponent(release)}`}
          >
            <Button size="sm" variant="outline">
              History
            </Button>
          </Link>
          {plan.data && (
            <Link
              to={`/upgrade/${encodeURIComponent(namespace)}/${encodeURIComponent(release)}`}
            >
              <Button size="sm" variant="outline">
                Upgrade
              </Button>
            </Link>
          )}
        </div>
      </div>

      <InstallStatus status={s} />

      <ReleaseStatus namespace={namespace} release={release} />

      <HealthCheck namespace={namespace} release={release} hasPlan={!!plan.data} />

      {plan.data && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Plan</CardTitle>
            <p className="text-sm text-muted-foreground">
              What this release was composed from, read from the ConfigMap beside it.
            </p>
          </CardHeader>
          <CardContent>
            <Provenance plan={plan.data} />
          </CardContent>
        </Card>
      )}

      {cluster.data?.allowDeploy && (
        <Uninstall namespace={namespace} release={release} exists={s.exists} />
      )}
    </div>
  );
}

// InstallStatus reads the status key swiss writes beside the release, which is
// the only thing that can report an apply that started and never finished --
// helm's own status describes the last apply that returned.
function InstallStatus({ status }: { status: Status }) {
  const p: PlanStatus | undefined = status.planStatus;

  if (!status.exists && !p) {
    return (
      <Card>
        <CardContent className="p-4 text-sm text-muted-foreground">
          No helm release here. Nothing has been installed under this name.
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          Install status
          {p && <PhaseBadge phase={p.phase} />}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[11rem_1fr]">
          <Row label="Helm status" value={status.helmStatus} />
          <Row label="Helm revision" value={status.exists ? String(status.revision) : "not installed"} />
          {p && (
            <>
              <Row label="Last action" value={p.action} />
              <Row label="Recorded revision" value={p.revision ? String(p.revision) : undefined} />
              <Row label="Started" value={p.startedAt} />
              <Row label="Updated" value={p.updatedAt} />
            </>
          )}
          <Row label="Route" value={status.route} />
        </dl>

        {p?.error && <p className="text-sm text-warning">{p.error}</p>}

        {p?.phase === "applying" && (
          <p className="flex items-start gap-2 text-sm text-warning">
            <TriangleAlert className="mt-0.5 size-4 shrink-0" />
            An apply was started and never recorded a result. It may still be running, or
            swissd may have been restarted mid-apply.
          </p>
        )}

        {!p && status.exists && (
          <p className="text-sm text-muted-foreground">
            No status recorded beside this release — swiss did not deploy it.
          </p>
        )}
      </CardContent>
    </Card>
  );
}

const APIS: { id: ChatApi; label: string; path: string }[] = [
  { id: "chat", label: "Chat", path: "/v1/chat/completions" },
  { id: "completions", label: "Completions", path: "/v1/completions" },
  { id: "messages", label: "Messages", path: "/v1/messages" },
];

// HealthCheck sends a real inference request. The pod readiness above and the
// /v1/models probe both pass on a model that cannot generate a token, so this
// is the only check that answers the question an operator actually has.
function HealthCheck({
  namespace,
  release,
  hasPlan,
}: {
  namespace: string;
  release: string;
  hasPlan: boolean;
}) {
  const [chatApi, setChatApi] = useState<ChatApi>("chat");
  const [prompt, setPrompt] = useState("Reply with the single word: ok");
  const [auth, setAuth] = useState<EntrypointAuth>({});
  const [result, setResult] = useState<ChatResult | null>(null);

  const run = useMutation({
    mutationFn: () => deployApi.chat(namespace, release, { api: chatApi, prompt, ...auth }),
    onSuccess: setResult,
  });

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Health check</CardTitle>
        <p className="text-sm text-muted-foreground">
          Sends a real request through the openresty entrypoint and shows what came back. A
          ready pod behind an unpublished route serves nobody, and a model list comes back
          from an engine that cannot yet generate a token.
        </p>
      </CardHeader>
      <CardContent className="space-y-3">
        {!hasPlan ? (
          <p className="text-sm text-muted-foreground">
            Needs the plan beside the release to know which route to call.
          </p>
        ) : (
          <>
            <div className="flex flex-wrap gap-1">
              {APIS.map((a) => (
                <button
                  key={a.id}
                  onClick={() => setChatApi(a.id)}
                  title={a.path}
                  className={
                    chatApi === a.id
                      ? "rounded-md border border-foreground px-3 py-1 text-sm font-medium"
                      : "rounded-md border px-3 py-1 text-sm text-muted-foreground hover:text-foreground"
                  }
                >
                  {a.label}
                </button>
              ))}
            </div>

            <Field label="Prompt">
              <Input value={prompt} onChange={(e) => setPrompt(e.target.value)} />
            </Field>

            <AuthFields value={auth} onChange={setAuth} />

            <Button onClick={() => run.mutate()} disabled={run.isPending}>
              {run.isPending ? "Asking the model…" : "Send request"}
            </Button>

            {run.error && <ErrorState what="the health check" error={run.error} />}
            {result && <ChatOutcome result={result} />}
          </>
        )}
      </CardContent>
    </Card>
  );
}

function ChatOutcome({ result }: { result: ChatResult }) {
  return (
    <div className="space-y-2 rounded-lg border p-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        {result.ok ? (
          <CircleCheck className="size-4 shrink-0 text-success" />
        ) : (
          <CircleX className="size-4 shrink-0 text-destructive" />
        )}
        <span className="font-medium">{result.ok ? "The model answered" : "No usable answer"}</span>
        {result.status ? <Badge variant="muted">HTTP {result.status}</Badge> : null}
        <Badge variant="outline">{result.latencyMs} ms</Badge>
        {result.model && <Badge variant="muted">{result.model}</Badge>}
        <SentHeaders names={result.sentHeaders} />
      </div>

      <div className="font-mono text-xs break-all text-muted-foreground">{result.url}</div>

      {result.reply && (
        <pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs whitespace-pre-wrap">
          {result.reply}
        </pre>
      )}
      {result.error && <p className="text-sm text-destructive">{result.error}</p>}
      {result.body && (
        <pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs whitespace-pre-wrap">
          {result.body}
        </pre>
      )}
    </div>
  );
}

// Uninstall is typed to confirm rather than guarded by a dialog. It removes a
// release that takes 20-40 minutes to load back, so the release name is the one
// thing the operator must have read before this goes ahead.
function Uninstall({
  namespace,
  release,
  exists,
}: {
  namespace: string;
  release: string;
  exists: boolean;
}) {
  const [typed, setTyped] = useState("");
  const navigate = useNavigate();
  const qc = useQueryClient();

  const run = useMutation({
    mutationFn: () => deployApi.uninstall(namespace, release),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["deployments"] });
      if (!r.planError) navigate("/");
    },
  });

  return (
    <Card className="border-destructive/30">
      <CardHeader>
        <CardTitle className="text-base">Uninstall</CardTitle>
        <p className="text-sm text-muted-foreground">
          Runs <code>helm uninstall</code> and removes the plan recorded beside the release.
          The audit log keeps the record. Reloading the weights afterwards takes 20–40
          minutes.
        </p>
      </CardHeader>
      <CardContent className="space-y-3">
        {!exists ? (
          <p className="text-sm text-muted-foreground">There is no live release to remove.</p>
        ) : (
          <>
            <Field label="Type the release name to confirm" hint={release}>
              <Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={release} />
            </Field>
            <Button
              variant="destructive"
              onClick={() => run.mutate()}
              disabled={typed !== release || run.isPending}
            >
              {run.isPending ? "Uninstalling…" : `Uninstall ${release}`}
            </Button>
          </>
        )}

        {run.error && <ErrorState what="the uninstall" error={run.error} />}
        {run.data?.planError && (
          <p className="text-sm text-warning">
            The release is gone, but its plan ConfigMap was not removed: {run.data.planError}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function PhaseBadge({ phase }: { phase: string }) {
  if (phase === "applied") return <Badge variant="success">applied</Badge>;
  if (phase === "failed") return <Badge variant="destructive">failed</Badge>;
  if (phase === "applying") return <Badge variant="warning">applying</Badge>;
  return <Badge variant="outline">{phase}</Badge>;
}

// An unset field is left out rather than rendered as a dash: the list is read at
// a glance, and empty rows bury the ones that were actually set.
function Row({ label, value }: { label: string; value?: string }) {
  if (!value?.trim()) return null;
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="break-all">{value}</dd>
    </>
  );
}
