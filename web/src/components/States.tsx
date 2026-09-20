import { AlertCircle, Loader2 } from "lucide-react";

export function Loading({ what }: { what: string }) {
  return (
    <div className="flex items-center gap-2 py-12 text-sm text-muted-foreground">
      <Loader2 className="size-4 animate-spin" />
      Loading {what}…
    </div>
  );
}

// Errors say which dependency failed, because swissd's own readiness check
// reports them separately -- collapsing that back into "something went wrong"
// would throw away the only useful part.
export function ErrorState({ what, error }: { what: string; error: unknown }) {
  const message = error instanceof Error ? error.message : String(error);
  return (
    <div className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm">
      <AlertCircle className="mt-0.5 size-4 shrink-0 text-destructive" />
      <div>
        <div className="font-medium">Could not load {what}</div>
        <div className="mt-1 text-muted-foreground">{message}</div>
      </div>
    </div>
  );
}

export function Empty({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed py-12 text-center text-sm text-muted-foreground">
      {children}
    </div>
  );
}
