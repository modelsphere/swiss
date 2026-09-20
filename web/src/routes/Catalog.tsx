import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, type IndexModel } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
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
  return (
    <Link to={`/catalog/${encodeURIComponent(m.name)}`} className="block">
      <Card className="h-full transition-colors hover:border-foreground/20">
        <CardHeader>
          <CardTitle className="flex flex-wrap items-center gap-2">
            {m.displayName ?? m.name}
            {m.variants.length > 1 && (
              <Badge variant="muted">{m.variants.length} variants</Badge>
            )}
          </CardTitle>
          <CardDescription className="line-clamp-2">{m.description}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-2">
          <div className="text-xs text-muted-foreground">
            {m.source.hf}
            {m.source.sizeGiB ? ` · ${m.source.sizeGiB} GiB` : ""}
          </div>
          <div className="flex flex-wrap gap-1">
            {m.variants.map((v) => (
              <Badge key={v.id} variant="outline">
                {v.engine} · {v.requires.nodes && v.requires.nodes > 1
                  ? `${v.requires.nodes}×${v.requires.gpus}`
                  : `${v.requires.gpus}`} GPU
              </Badge>
            ))}
          </div>
        </CardContent>
      </Card>
    </Link>
  );
}
