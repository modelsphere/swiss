import { NavLink, Outlet } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Boxes, Server } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export function Layout() {
  const { data: cluster } = useQuery({ queryKey: ["cluster"], queryFn: api.cluster });

  return (
    <div className="min-h-screen">
      <header className="border-b">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3">
          <div className="flex items-center gap-2 font-semibold">
            <Server className="size-4" />
            Swiss
          </div>

          <nav className="flex gap-1 text-sm">
            <Tab to="/">Deployments</Tab>
            <Tab to="/catalog">Catalog</Tab>
            <Tab to="/nodes">Nodes</Tab>
            <Tab to="/runs">Operations</Tab>
            <Tab to="/profile">Profile</Tab>
          </nav>

          <div className="ml-auto flex items-center gap-2 text-sm">
            <ClusterSwitcher />
          </div>
        </div>

        {cluster?.warnings?.map((w) => (
          <div
            key={w}
            className="flex items-center gap-2 bg-warning/10 px-4 py-2 text-xs text-warning"
          >
            <AlertTriangle className="size-3.5 shrink-0" />
            {w}
          </div>
        ))}
      </header>

      <main className="mx-auto max-w-6xl px-4 py-6">
        <Outlet />
      </main>
    </div>
  );
}

function Tab({ to, children }: { to: string; children: React.ReactNode }) {
  return (
    <NavLink
      to={to}
      end={to === "/"}
      className={({ isActive }) =>
        cn(
          "rounded-md px-3 py-1.5 transition-colors",
          isActive ? "bg-muted font-medium" : "text-muted-foreground hover:bg-muted/60",
        )
      }
    >
      {children}
    </NavLink>
  );
}

// Peers are other clusters' swissd instances, each a separate origin. Switching
// is a real navigation, not client-side routing -- and every instance serves
// this same switcher, so any one of them is a valid entry point.
//
// The swissd version is shown deliberately: N instances drift, and seeing that
// here beats debugging a bug report that is really a stale deploy.
function ClusterSwitcher() {
  const { data, isError } = useQuery({ queryKey: ["cluster"], queryFn: api.cluster });

  if (isError) return <Badge variant="destructive">cluster unreachable</Badge>;
  if (!data) return <span className="text-muted-foreground">…</span>;

  return (
    <div className="flex items-center gap-2">
      <Boxes className="size-4 text-muted-foreground" />
      <select
        className="rounded-md border bg-background px-2 py-1 text-sm"
        value=""
        onChange={(e) => {
          if (e.target.value) window.location.assign(e.target.value);
        }}
      >
        <option value="">{data.name}</option>
        {data.peers?.map((p) => (
          <option key={p.name} value={p.url}>
            {p.name}
          </option>
        ))}
      </select>
      <span className="text-xs text-muted-foreground">{data.version}</span>
    </div>
  );
}
