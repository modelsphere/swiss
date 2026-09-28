import { useState } from "react";
import { createPortal } from "react-dom";
import { Info } from "lucide-react";

// Guidance that costs no vertical space until it is asked for: the label wears
// a small Info mark, and the text appears beside the pointer. A card whose
// every row carries a visible note reads as a wall of prose with controls in
// it; this keeps the rows to labels and boxes.
//
// Portalled to the body, so a row that clips its overflow cannot cut the
// tooltip off, and positioned from the trigger's own rect rather than by
// `title`, whose delay and placement belong to the browser.
export function HoverHint({ text, children }: { text?: string; children: React.ReactNode }) {
  const [box, setBox] = useState<{ left: number; top: number } | null>(null);
  if (!text) return children;
  return (
    <span
      className="inline-flex min-w-0 items-center gap-1"
      onMouseEnter={(e) => {
        const r = e.currentTarget.getBoundingClientRect();
        const width = 256;
        const left = Math.max(8, Math.min(r.left, window.innerWidth - width - 8));
        setBox({ left, top: r.bottom + 6 });
      }}
      onMouseLeave={() => setBox(null)}
    >
      {children}
      <Info className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      {box &&
        createPortal(
          <span
            role="tooltip"
            style={{ left: box.left, top: box.top }}
            className="pointer-events-none fixed z-50 w-64 rounded-md border bg-card px-2.5 py-2 text-xs leading-snug font-normal text-card-foreground shadow-md"
          >
            {text}
          </span>,
          document.body,
        )}
    </span>
  );
}
