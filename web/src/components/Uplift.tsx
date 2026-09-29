import { formatUplift } from "@/lib/catalog";

// Result literals, set apart from the prose around them: the number and the
// workload in mono, "at" and "tokens" plain. Matches the catalog's own site.

type Workload = { name: string; uplift: number };

// `50k + 1.5k` tokens
export function WorkloadName({ name }: { name: string }) {
  return (
    <>
      <code className="rounded border bg-muted px-1 font-mono text-[0.92em] text-foreground">{name}</code> tokens
    </>
  );
}

// +23.4% at `8k + 1k` tokens
export function WorkloadResult({ w }: { w: Workload }) {
  return (
    <span className="whitespace-nowrap">
      <span className="font-mono font-semibold text-success">{formatUplift(w.uplift)}</span> at{" "}
      <WorkloadName name={w.name} />
    </span>
  );
}

// Several results on one wrapping line.
export function WorkloadList({ workloads }: { workloads: Workload[] }) {
  return (
    <>
      {workloads.map((w, i) => (
        <span key={w.name}>
          {/* Spaces around the dot are where the line may wrap. */}
          {i > 0 && <> <span className="text-muted-foreground">·</span> </>}
          <WorkloadResult w={w} />
        </span>
      ))}
    </>
  );
}
