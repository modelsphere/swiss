import { useState } from "react";
import { Navigate, useLocation, useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Server } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { useSessionQuery } from "@/components/Session";

// One account, because swissd manages one cluster and what it can do there is
// already bounded by its own RBAC. What the login adds is that the UI is not
// open to whoever can reach the Service.
export function Login() {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
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

  // Already logged in: nothing to ask.
  if (session.data?.authenticated) return <Navigate to="/" replace />;

  const err = submit.error;
  const message =
    err instanceof ApiError && err.status === 429
      ? "Too many failed attempts. Wait a minute."
      : err
        ? err.message
        : "";

  return (
    <div className="mx-auto flex min-h-screen max-w-sm items-center px-4">
      <Card className="w-full">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Server className="size-4" /> Swiss
          </CardTitle>
          <p className="text-sm text-muted-foreground">
            Sign in to manage this cluster's model deployments.
          </p>
        </CardHeader>
        <CardContent>
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              submit.mutate();
            }}
          >
            <Field label="User">
              <Input
                value={username}
                autoComplete="username"
                onChange={(e) => setUsername(e.target.value)}
              />
            </Field>
            <Field label="Password">
              <Input
                type="password"
                value={password}
                autoComplete="current-password"
                onChange={(e) => setPassword(e.target.value)}
              />
            </Field>

            {message && <p className="text-sm text-destructive">{message}</p>}

            <Button type="submit" className="w-full" disabled={submit.isPending || !password}>
              {submit.isPending ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
