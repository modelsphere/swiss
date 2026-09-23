import { tokenize, type Kind } from "@/lib/syntax";
import { cn } from "@/lib/utils";

// Existing palette only: four greys plus the two accents already defined in
// both themes. A syntax palette would be eight new values to pick blind.
// Contrast comes from values being coloured and keys being weighted, so the
// only thing this needs from the palette is what is already in it. `key` was
// text-foreground, which is exactly what the block already inherits -- a class
// that changed nothing.
const CLASS: Record<Kind, string> = {
  key: "font-semibold",
  string: "text-success",
  number: "text-warning",
  comment: "text-muted-foreground italic",
  flag: "text-warning",
  punct: "text-muted-foreground",
  plain: "text-muted-foreground",
};

export function Code({
  children,
  lang,
  className,
}: {
  children: string;
  lang: "yaml" | "sh";
  className?: string;
}) {
  return (
    <pre
      className={cn(
        "overflow-x-auto rounded-md border bg-muted/40 p-3 text-xs leading-relaxed",
        className,
      )}
    >
      <code>
        {tokenize(children, lang).map((line, i) => (
          <span key={i}>
            {i > 0 && "\n"}
            {line.map((t, j) =>
              t.kind === "plain" ? (
                t.text
              ) : (
                <span key={j} className={CLASS[t.kind]}>
                  {t.text}
                </span>
              ),
            )}
          </span>
        ))}
      </code>
    </pre>
  );
}
