import { cn } from "@/lib/utils";

const ANSI = new RegExp(String.fromCharCode(27) + "\\[[0-9;]*m", "g");

export function DiffView({ output }: { output: string }) {
  const lines = output.replace(ANSI, "").split("\n");
  return (
    <pre className="max-h-[28rem] overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-relaxed">
      {lines.map((line, i) => (
        <div
          key={i}
          className={cn(
            "whitespace-pre",
            line.startsWith("+") && "text-success",
            line.startsWith("-") && "text-destructive",
            line.startsWith("@@") && "text-muted-foreground",
          )}
        >
          {line || " "}
        </div>
      ))}
    </pre>
  );
}
