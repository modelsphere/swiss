import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Field, Input } from "@/components/ui/input";
import type { EntrypointAuth } from "@/lib/api";

// The credential inputs shared by the serving check and the health check.
//
// Folded away by default: when the site profile names the key, nothing here
// needs filling in, and an always-open key box invites pasting one that should
// have gone into the Secret. Kept in component state and nowhere else -- a key
// in localStorage outlives the tab, the session and the operator's attention.
export function AuthFields({
  value,
  onChange,
}: {
  value: EntrypointAuth;
  onChange: (a: EntrypointAuth) => void;
}) {
  const [raw, setRaw] = useState("");
  const [bad, setBad] = useState<string[]>([]);

  const parse = (text: string) => {
    setRaw(text);
    const headers: Record<string, string> = {};
    const rejected: string[] = [];
    for (const line of text.split("\n")) {
      if (!line.trim()) continue;
      const at = line.indexOf(":");
      if (at <= 0) {
        rejected.push(line.trim());
        continue;
      }
      headers[line.slice(0, at).trim()] = line.slice(at + 1).trim();
    }
    setBad(rejected);
    onChange({ ...value, headers: Object.keys(headers).length ? headers : undefined });
  };

  const count = Object.keys(value.headers ?? {}).length;
  const summary = [value.apiKey ? "key set" : "", count ? `${count} header${count > 1 ? "s" : ""}` : ""]
    .filter(Boolean)
    .join(" · ");

  return (
    <details className="rounded-lg border">
      <summary className="flex cursor-pointer flex-wrap items-center gap-2 px-3 py-2 text-sm">
        Auth
        <span className="text-xs text-muted-foreground">
          {summary || "using the site profile"}
        </span>
      </summary>
      <div className="space-y-3 border-t p-3">
        <Field
          label="API key"
          hint="overrides the key the site profile names; sent, never stored"
        >
          <Input
            type="password"
            autoComplete="off"
            value={value.apiKey ?? ""}
            onChange={(e) => onChange({ ...value, apiKey: e.target.value || undefined })}
            placeholder="leave empty to use route.auth.secretRef"
          />
        </Field>
        <Field label="Extra headers" hint="one per line, Name: value">
          <textarea
            value={raw}
            onChange={(e) => parse(e.target.value)}
            spellCheck={false}
            rows={3}
            placeholder={"X-Tenant: research\nX-Request-Source: swissd"}
            className="w-full rounded-md border bg-background px-3 py-2 font-mono text-xs"
          />
        </Field>
        {bad.length > 0 && (
          <p className="text-xs text-warning">
            Ignored, no colon: {bad.join(", ")}
          </p>
        )}
      </div>
    </details>
  );
}

// SentHeaders reports what a check actually carried. Names only -- the server
// does not return values, which is what makes this safe to show at all.
export function SentHeaders({ names }: { names?: string[] }) {
  if (!names?.length) {
    return <Badge variant="muted">no headers sent</Badge>;
  }
  return (
    <>
      {names.map((n) => (
        <Badge key={n} variant="muted">
          {n}
        </Badge>
      ))}
    </>
  );
}
