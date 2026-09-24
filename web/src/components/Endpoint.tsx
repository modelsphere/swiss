import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { CircleCheck, CircleX, Loader2 } from "lucide-react";
import {
  api,
  deployApi,
  type ChatResult,
  type EntrypointAuth,
  type InferenceApi,
  type ProbeResult,
  type ReleaseStatus as Status,
} from "@/lib/api";
import { AuthFields, SentHeaders } from "@/components/EntrypointAuth";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Code } from "@/components/ui/code";
import { Field, Input } from "@/components/ui/input";
import { ErrorState } from "@/components/States";

// Endpoint is where this release is called and the two ways of asking whether
// calling it works. One card, because an operator arrives with one question --
// "can I use this yet" -- and the address, the route probe and a real inference
// request are three answers to it at increasing cost.
//
// The address is the part that is read every time, so it is the part that is
// open. Both checks spend something to run -- the health check spends GPU time
// -- so neither runs, or takes up room, until it is asked for.
export function Endpoint({
  namespace,
  release,
  status: s,
}: {
  namespace: string;
  release: string;
  status: Status;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          Endpoint
          {s.route && <Badge variant="outline">{s.route}</Badge>}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {s.url ? (
          <>
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <a
                href={s.url}
                target="_blank"
                rel="noreferrer"
                className="min-w-0 font-mono text-sm break-all hover:underline"
              >
                {s.url}
              </a>
              <CopyButton value={s.url} />
            </div>
            <div>
              <div className="mb-1.5 flex items-center justify-between gap-2">
                <span className="text-xs text-muted-foreground">
                  Fill in the key; the rest is this release.
                </span>
                <CopyButton value={curlFor(s)} />
              </div>
              <Code lang="sh">{curlFor(s)}</Code>
            </div>
          </>
        ) : s.route ? (
          <p className="text-sm text-muted-foreground">
            Published on <code>{s.route}</code>. The site profile names no{" "}
            <code>route.gateway</code>, so the outside URL is not known here.
          </p>
        ) : (
          <p className="text-sm text-muted-foreground">
            No route published for this release, so there is no address to call.
          </p>
        )}

        <Check
          title="Serving check"
          hint="Asks the openresty entrypoint for this route, not the pod. A ready pod behind a route that was never published serves nobody."
        >
          <ServingCheck namespace={namespace} release={release} />
        </Check>

        <Check
          title="Health check"
          hint="Sends a real request through the entrypoint and shows what came back. A model list comes back from an engine that cannot yet generate a token."
        >
          <HealthCheck namespace={namespace} release={release} />
        </Check>
      </CardContent>
    </Card>
  );
}

// Closed until asked for, and not mounted open: a check that has not been run
// shows nothing worth the room it would take.
function Check({
  title,
  hint,
  children,
}: {
  title: string;
  hint: string;
  children: React.ReactNode;
}) {
  return (
    <details className="rounded-md border">
      <summary className="cursor-pointer list-inside px-3 py-2 text-sm font-medium">
        {title}
        <span className="ml-2 font-normal text-muted-foreground">{hint}</span>
      </summary>
      <div className="space-y-3 border-t p-3">{children}</div>
    </details>
  );
}

// ServingCheck asks the entrypoint for /v1/models: it proves the route resolves
// and something is behind it.
function ServingCheck({ namespace, release }: { namespace: string; release: string }) {
  const [probe, setProbe] = useState<ProbeResult | null>(null);
  const [auth, setAuth] = useState<EntrypointAuth>({});

  const probeM = useMutation({
    mutationFn: () => deployApi.probe(namespace, release, auth),
    onSuccess: setProbe,
  });

  return (
    <>
      <AuthFields value={auth} onChange={setAuth} />

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
            <SentHeaders names={probe.sentHeaders} />
          </div>
          <Sent url={probe.url} curl={probe.curl} />
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
    </>
  );
}

// Named by protocol: "Chat" and "Messages" are the same word to an operator
// deciding which one their gateway accepts.
const API_FORMATS: { id: InferenceApi; label: string; path: string }[] = [
  { id: "chat", label: "OpenAI Chat Completions", path: "/v1/chat/completions" },
  { id: "completions", label: "OpenAI Completions (legacy)", path: "/v1/completions" },
  { id: "messages", label: "Anthropic Messages", path: "/v1/messages" },
];

// HealthCheck sends a real inference request. Pod readiness and the /v1/models
// probe both pass on a model that cannot generate a token, so this is the only
// check that answers the question an operator actually has.
function HealthCheck({ namespace, release }: { namespace: string; release: string }) {
  const [format, setFormat] = useState<InferenceApi>("chat");
  const [prompt, setPrompt] = useState("Reply with the single word: ok");
  // Room for a reasoning model to finish thinking before it answers; on a
  // tighter budget every such model stops mid-thought and looks broken.
  const [maxTokens, setMaxTokens] = useState(256);
  const [auth, setAuth] = useState<EntrypointAuth>({});
  const [result, setResult] = useState<ChatResult | null>(null);

  // The route to call is read off the plan beside the release, so a release
  // swiss did not deploy has nothing to send this to.
  const plan = useQuery({
    queryKey: ["releasePlan", namespace, release],
    queryFn: () => api.releasePlan(namespace, release),
    retry: false,
  });

  const run = useMutation({
    mutationFn: () =>
      deployApi.chat(namespace, release, { api: format, prompt, maxTokens, ...auth }),
    onSuccess: setResult,
  });

  if (!plan.data) {
    return (
      <p className="text-sm text-muted-foreground">
        {plan.isPending
          ? "Loading…"
          : "Needs the plan beside the release to know which route to call."}
      </p>
    );
  }

  return (
    <>
      <div className="flex flex-wrap gap-1">
        {API_FORMATS.map((a) => (
          <button
            key={a.id}
            onClick={() => setFormat(a.id)}
            className={
              format === a.id
                ? "rounded-md border border-foreground px-3 py-1 text-left"
                : "rounded-md border px-3 py-1 text-left text-muted-foreground hover:text-foreground"
            }
          >
            <span className="block text-sm font-medium">{a.label}</span>
            <span className="block font-mono text-xs opacity-70">{a.path}</span>
          </button>
        ))}
      </div>

      <div className="grid gap-3 sm:grid-cols-[1fr_8rem]">
        <Field label="Prompt">
          <Input value={prompt} onChange={(e) => setPrompt(e.target.value)} />
        </Field>
        <Field label="Max tokens">
          <Input
            type="number"
            min={1}
            value={maxTokens}
            onChange={(e) => setMaxTokens(Number(e.target.value) || 1)}
          />
        </Field>
      </div>

      <AuthFields value={auth} onChange={setAuth} />

      <Button onClick={() => run.mutate()} disabled={run.isPending}>
        {run.isPending ? "Asking the model…" : "Send request"}
      </Button>

      {run.error && <ErrorState what="the health check" error={run.error} />}
      {result && <ChatOutcome result={result} />}
    </>
  );
}

// A reasoning model that spent the budget thinking generated tokens, so the
// check passes -- but it is not an answer, and saying so is the difference
// between "raise the budget" and "the model is broken".
function outcomeText(result: ChatResult): string {
  if (!result.ok) return "No usable answer";
  if (!result.reply) return "Reasoning only, no answer";
  return "The model answered";
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
        <span className="font-medium">{outcomeText(result)}</span>
        {result.status ? <Badge variant="muted">HTTP {result.status}</Badge> : null}
        <Badge variant="outline">{result.latencyMs} ms</Badge>
        {result.finishReason && <Badge variant="muted">stopped: {result.finishReason}</Badge>}
        {result.model && <Badge variant="muted">{result.model}</Badge>}
        <SentHeaders names={result.sentHeaders} />
      </div>

      <Sent url={result.url} curl={result.curl} />

      {result.reply && (
        <pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs whitespace-pre-wrap">
          {result.reply}
        </pre>
      )}
      {result.reasoning && (
        <details className="rounded-md border">
          <summary className="cursor-pointer px-3 py-2 text-xs text-muted-foreground">
            Reasoning ({result.reasoning.length} chars)
          </summary>
          <pre className="overflow-x-auto border-t p-3 text-xs whitespace-pre-wrap text-muted-foreground">
            {result.reasoning}
          </pre>
        </details>
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

// What swissd sent, rendered by swissd rather than rebuilt here: a check that
// fails is usually asking whether the entrypoint is unreachable or the request
// was wrong, and only the sender can answer that. Shown with the result rather
// than behind a disclosure -- it is the first thing wanted when a check fails,
// and it replaces the bare URL line that used to sit here. The key is a shell
// variable in it, so this is runnable and carries no credential.
function Sent({ url, curl }: { url: string; curl?: string }) {
  if (!curl) return <div className="font-mono text-xs break-all text-muted-foreground">{url}</div>;
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">
          Request sent — runnable from a debug pod once {KEY} is exported.
        </span>
        <CopyButton value={curl} />
      </div>
      <Code lang="sh" className="whitespace-pre-wrap">
        {curl}
      </Code>
    </div>
  );
}

// A request that works as-is: the served model name rather than the catalog id,
// and the header shape the site profile declares. The key line is double-quoted
// on purpose -- inside single quotes the shell would send the variable name
// itself, which looks like it works and does not.
const KEY = "$SWISS_API_KEY";

function curlFor(s: Status): string {
  const header = s.authHeader || "Authorization";
  const prefix = s.authPrefix ?? "Bearer ";
  return [
    `curl ${s.url}/v1/chat/completions \\`,
    `  -H 'Content-Type: application/json' \\`,
    `  -H "${header}: ${prefix}${KEY}" \\`,
    `  -d '{`,
    `    "model": "${s.model ?? ""}",`,
    `    "messages": [{"role": "user", "content": "hello"}]`,
    `  }'`,
  ].join("\n");
}

function CopyButton({ value }: { value: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      onClick={() => {
        void navigator.clipboard?.writeText(value).then(
          () => {
            setDone(true);
            setTimeout(() => setDone(false), 1500);
          },
          () => {},
        );
      }}
      className="shrink-0 text-xs text-muted-foreground underline hover:text-foreground"
    >
      {done ? "copied" : "copy"}
    </button>
  );
}
