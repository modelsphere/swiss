import { Link, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Library } from "lucide-react";
import { api, type CatalogInfo } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Empty } from "@/components/States";

// The catalog a page works in. It travels in the URL as ?catalog=, from the
// catalog page to a model to its deploy, so a link says which catalog it means.
// With one catalog configured there is nothing to choose. With several, the
// one the site profile marks default is used; with no default, nothing is
// chosen until the operator picks one -- a page that fell back to whichever
// catalog is listed first would deploy from a catalog nobody chose.
export function useCatalogChoice() {
  const [params, setParams] = useSearchParams();
  const cluster = useQuery({ queryKey: ["cluster"], queryFn: api.cluster });
  const catalogs = cluster.data?.catalogs ?? [];
  const named = params.get("catalog") ?? "";
  const fallback = catalogs.length === 1 ? catalogs[0] : catalogs.find((c) => c.default);
  const selected = catalogs.some((c) => c.name === named) ? named : !named && fallback ? fallback.name : "";
  const choose = (name: string) =>
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set("catalog", name);
      return next;
    });
  return {
    catalogs,
    selected,
    named,
    choose,
    // Several catalogs: the pages say which one they are showing.
    several: catalogs.length > 1,
    isPending: cluster.isPending,
    error: cluster.error,
  };
}

// The catalog a release belongs to, by the rules swissd upgrades it by
// (Server.ReleaseCatalog): the one its plan names; for a plan that names none,
// the one at the location it records; otherwise the default, or the only
// catalog -- which is how a release from before this site listed catalogs, or
// from the CLI, keeps upgrading without its plan being edited.
export function releaseCatalog(
  catalogs: CatalogInfo[],
  source: { catalog?: string; catalogName?: string },
): string | undefined {
  if (source.catalogName) return catalogs.find((c) => c.name === source.catalogName)?.name;
  const at = source.catalog ? catalogs.find((c) => c.source === source.catalog) : undefined;
  return (at ?? (catalogs.length === 1 ? catalogs[0] : catalogs.find((c) => c.default)))?.name;
}

// A path with ?catalog= set, so a link keeps the page's catalog.
export function withCatalog(path: string, catalog: string): string {
  if (!catalog) return path;
  return `${path}${path.includes("?") ? "&" : "?"}catalog=${encodeURIComponent(catalog)}`;
}

// Shown in place of a page until a catalog is chosen.
export function CatalogGate({
  catalogs,
  named,
  choose,
  what,
}: {
  catalogs: CatalogInfo[];
  named: string;
  choose: (name: string) => void;
  // What the page is about to do with it: "browse", "deploy from".
  what: string;
}) {
  if (catalogs.length === 0) {
    return (
      <Empty>
        No catalog is configured.{" "}
        <Link to="/site-profile" className="underline">
          Add one to the site profile.
        </Link>
      </Empty>
    );
  }
  return (
    <div className="mx-auto max-w-2xl space-y-4 py-6">
      <div className="space-y-1">
        <h1 className="text-lg font-semibold">Choose a catalog</h1>
        <p className="text-sm text-muted-foreground">
          This site deploys from {catalogs.length} catalogs. Pick the one to {what}.
        </p>
        {named && <p className="text-sm text-warning">No catalog is named “{named}”.</p>}
      </div>
      <div className="grid gap-2">
        {catalogs.map((c) => (
          <button
            key={c.name}
            type="button"
            onClick={() => choose(c.name)}
            className="flex items-start gap-3 rounded-lg border bg-card p-4 text-left shadow-sm transition-colors hover:bg-muted/40"
          >
            <Library className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0">
              <span className="flex items-center gap-2 font-semibold">
                {c.name}
                {c.default && <Badge variant="muted">default</Badge>}
              </span>
              <span className="mt-0.5 block font-mono text-xs break-all text-muted-foreground">{c.url}</span>
              {!c.ref && <span className="mt-1 block text-xs text-warning">could not be read right now</span>}
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}

// The catalog page's switcher. Absent with one catalog: nothing to switch to.
export function CatalogSwitch({
  catalogs,
  selected,
  choose,
}: {
  catalogs: CatalogInfo[];
  selected: string;
  choose: (name: string) => void;
}) {
  if (catalogs.length < 2) return null;
  return (
    <label className="inline-flex items-center gap-2 text-sm">
      <span className="sr-only">Catalog</span>
      <Library className="size-4 text-muted-foreground" />
      <select
        className="h-8 rounded-md border bg-background px-2 text-sm font-medium"
        value={selected}
        onChange={(e) => choose(e.target.value)}
      >
        {catalogs.map((c) => (
          <option key={c.name} value={c.name}>
            {c.default ? `${c.name} (default)` : c.name}
          </option>
        ))}
      </select>
    </label>
  );
}

// Which catalog a model or deploy page is in, shown when there are several.
// Read-only: a model belongs to its catalog, so switching is done from the
// catalog page.
export function CatalogBadge({ name, show }: { name: string; show: boolean }) {
  if (!show || !name) return null;
  return (
    <Badge variant="outline" className="gap-1">
      <Library className="size-3 text-muted-foreground" />
      {name}
    </Badge>
  );
}
