import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, type IndexModel } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, ErrorState, Loading } from "@/components/States";

export function Catalog() {
  const { data, isPending, error } = useQuery({ queryKey: ["catalog"], queryFn: api.catalog });

  if (isPending) return <Loading what="catalog" />;
  if (error) return <ErrorState what="catalog" error={error} />;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-baseline gap-2">
        <h1 className="text-lg font-semibold">Catalog</h1>
        <span className="text-sm text-muted-foreground">{data.source}</span>
        <code className="ml-auto text-xs text-muted-foreground">{data.ref.slice(0, 19)}</code>
      </div>

      {data.index.models.length === 0 ? (
        <Empty>This catalog lists no models.</Empty>
      ) : (
        <div className="grid gap-3 md:grid-cols-2">
          {data.index.models.map((m) => (
            <ModelCard key={m.name} m={m} />
          ))}
        </div>
      )}
    </div>
  );
}

function ModelCard({ m }: { m: IndexModel }) {
  const latest = m.versions.find((v) => v.version === m.latest) ?? m.versions[0];
  const defaultVariant = latest.variants.find((v) => v.default) ?? latest.variants[0];

  return (
    <Card className="flex h-full flex-col">
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          <Link to={`/catalog/${encodeURIComponent(m.name)}`} className="hover:underline">
            {m.displayName ?? m.name}
          </Link>
          <Badge variant="muted">v{m.latest}</Badge>
          {m.versions.length > 1 && (
            <span className="text-xs font-normal text-muted-foreground">
              {m.versions.length} versions
            </span>
          )}
        </CardTitle>
        <CardDescription className="line-clamp-2">{m.description}</CardDescription>
      </CardHeader>
      <CardContent className="mt-auto space-y-3">
        <div className="text-xs text-muted-foreground">
          {m.source.hf}
          {m.source.sizeGiB ? ` · ${m.source.sizeGiB} GiB` : ""}
        </div>
        <div className="flex flex-wrap items-center gap-1">
          {latest.variants.map((v) => (
            <Badge key={v.id} variant="outline">
              {v.engine} ·{" "}
              {v.requires.nodes && v.requires.nodes > 1
                ? `${v.requires.nodes}×${v.requires.gpus}`
                : `${v.requires.gpus}`}{" "}
              GPU
            </Badge>
          ))}
        </div>
        <div className="flex items-center gap-2">
          <Link
            to={
              `/deploy/${encodeURIComponent(m.name)}` +
              (defaultVariant ? `?variant=${encodeURIComponent(defaultVariant.id)}` : "")
            }
          >
            <Button size="sm">Deploy</Button>
          </Link>
          <Link to={`/catalog/${encodeURIComponent(m.name)}`}>
            <Button size="sm" variant="ghost">
              {latest.variants.length > 1 ? `${latest.variants.length} variants` : "Details"}
            </Button>
          </Link>
        </div>
      </CardContent>
    </Card>
  );
}
