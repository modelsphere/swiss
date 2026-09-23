import { useState } from "react";
import { Navigate, useLocation, useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, Eye, EyeOff, Loader2, Server } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { useSessionQuery } from "@/components/Session";

// One account, because swissd manages one cluster and what it can do there is
// already bounded by its own RBAC. What the login adds is that the UI is not
// open to whoever can reach the Service.
//
// Everything lives inside the one card, title included: a heading stacked
// above it centres the column rather than the card, and the card is what the
// eye reads as the page.
export function Login() {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [reveal, setReveal] = useState(false);
  const [capsLock, setCapsLock] = useState(false);
  const navigate = useNavigate();
  const location = useLocation();
  const qc = useQueryClient();
  const session = useSessionQuery();

  const submit = useMutation({
    mutationFn: () => api.login(username, password),
    onSuccess: (s) => {
      // Stored so the gate lets the next render through without a round trip,
      // and re-asked because a session fetch may still be in flight from this
      // page's own mount -- its reply would otherwise land after this one and
      // overwrite it with the logged-out answer it set off with.
      qc.setQueryData(["session"], s);
      qc.invalidateQueries({ queryKey: ["session"] });
      const from = (location.state as { from?: string } | null)?.from;
      navigate(s.initialized ? (from ?? "/") : "/setup", { replace: true });
    },
  });

  // Already logged in: nothing to ask. A swissd with auth disabled answers the
  // session probe as authenticated too, so it lands here as well -- which is
  // right, since its /api/login refuses every post.
  if (session.data?.authenticated) return <Navigate to="/" replace />;

  const err = submit.error;
  // The server's own wording is kept: it says which of the several ways this
  // can fail happened, and a single "sign-in failed" would throw that away.
  // Anything that is not an API reply never reached swissd at all.
  const message = !err
    ? ""
    : err instanceof ApiError
      ? err.message
      : "swissd did not answer. Check that it is reachable, then try again.";
  const pending = submit.isPending;

  return (
    // dvh, not vh: on a phone the URL bar is part of the viewport height that
    // vh reports, so a vh-centred card sits low and shifts as the bar hides.
    // The card is one fluid width that the padding shrinks below on a narrow
    // screen -- no breakpoints, because there is only ever one column.
    <div className="relative flex min-h-dvh items-center justify-center overflow-hidden p-4">
      {/* Decoration only: a soft wash off the foreground token, so it reads as
          a light haze in the light theme and a glow in the dark one without
          introducing a colour the rest of the app does not use. */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-x-0 top-0 mx-auto h-[340px] max-w-2xl rounded-full bg-foreground/[0.07] blur-[120px]"
      />

      <Card className="relative w-full max-w-[32rem]">
        <CardContent className="space-y-6 p-7">
          <div className="flex flex-col items-center gap-3 text-center">
            <div className="flex size-11 items-center justify-center rounded-xl border bg-background shadow-sm">
              <Server className="size-5" />
            </div>
            <div className="space-y-1">
              <h1 className="text-2xl font-semibold tracking-tight">Swiss</h1>
              <p className="text-sm text-muted-foreground">
                Sign in to manage this cluster's model deployments.
              </p>
            </div>
          </div>

          {/* The session is still being asked for on a cold load of this page
              -- showing the form first means a logged-in browser sees it flash
              before the redirect it was always going to make. The height is
              held steady so the card does not jump when the answer lands. */}
          {session.isPending ? (
            <div className="flex min-h-[13.5rem] items-center justify-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              Checking the session…
            </div>
          ) : (
            <form
              className="space-y-4"
              onSubmit={(e) => {
                e.preventDefault();
                submit.mutate();
              }}
            >
              <Field label="User">
                <Input
                  className="h-10"
                  value={username}
                  autoComplete="username"
                  autoFocus
                  disabled={pending}
                  // A stale error under a field being retyped describes a
                  // password that is no longer in the box.
                  onChange={(e) => {
                    submit.reset();
                    setUsername(e.target.value);
                  }}
                />
              </Field>

              <div className="space-y-1">
                <label htmlFor="password" className="block text-sm font-medium">
                  Password
                </label>
                {/* Not a Field: the reveal toggle sits inside the input, and a
                    button nested in a label is a second thing that clicking
                    the label does. */}
                <div className="relative">
                  <Input
                    id="password"
                    type={reveal ? "text" : "password"}
                    className="h-10 pr-10"
                    value={password}
                    autoComplete="current-password"
                    disabled={pending}
                    aria-invalid={!!message}
                    aria-describedby={message ? "login-error" : undefined}
                    onChange={(e) => {
                      submit.reset();
                      setPassword(e.target.value);
                    }}
                    onKeyDown={(e) => setCapsLock(e.getModifierState("CapsLock"))}
                    onKeyUp={(e) => setCapsLock(e.getModifierState("CapsLock"))}
                    onBlur={() => setCapsLock(false)}
                  />
                  <button
                    type="button"
                    onClick={() => setReveal((v) => !v)}
                    aria-label={reveal ? "Hide password" : "Show password"}
                    aria-pressed={reveal}
                    tabIndex={-1}
                    className="absolute inset-y-0 right-0 flex w-10 items-center justify-center rounded-r-md text-muted-foreground hover:text-foreground"
                  >
                    {reveal ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                  </button>
                </div>
                {/* Worth saying out loud: the field is masked, so a wrong case
                    is otherwise only visible as a refusal. */}
                {capsLock && (
                  <span className="block text-xs text-warning">Caps Lock is on.</span>
                )}
              </div>

              {message && (
                <p
                  id="login-error"
                  role="alert"
                  className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-2.5 text-sm"
                >
                  <AlertCircle className="mt-0.5 size-4 shrink-0 text-destructive" />
                  {message}
                </p>
              )}

              <Button type="submit" className="h-10 w-full" disabled={pending || !password}>
                {pending && <Loader2 className="size-4 animate-spin" />}
                {pending ? "Signing in…" : "Sign in"}
              </Button>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
