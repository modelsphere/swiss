import { useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

// A modal, deliberately minimal: there is one in this app and it holds the
// apply pipeline, so it needs to be dismissable, focus-trapping and scrollable,
// and nothing else.
//
// Dismissing is not the same as cancelling. Closing this leaves the composed
// plan and the diff exactly where they were -- reopening returns to them --
// because the only irreversible thing in here is the apply button, and that is
// never what a stray Escape should reach.
export function Dialog({
  open,
  onClose,
  title,
  subtitle,
  footer,
  children,
}: {
  open: boolean;
  onClose: () => void;
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  // Pinned below the scrolling body: the action lives here, so it stays on
  // screen no matter how long the diff is. A diff is thousands of lines, and a
  // button that scrolls away is a button nobody trusts they have found.
  footer?: React.ReactNode;
  children: React.ReactNode;
}) {
  const panel = useRef<HTMLDivElement>(null);
  // Read through a ref, so this runs on open and close and not on every render
  // of the caller: callers pass a fresh arrow function each time, and re-running
  // it moved focus back to the panel after every keystroke in the footer.
  const close = useRef(onClose);
  close.current = onClose;

  // Escape closes, and focus moves into the panel so the tab order starts here
  // rather than back at the page behind it.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") close.current();
    };
    document.addEventListener("keydown", onKey);
    const previous = document.activeElement as HTMLElement | null;
    panel.current?.focus();
    // The page behind must not scroll under the overlay.
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = overflow;
      previous?.focus?.();
    };
  }, [open]);

  if (!open) return null;

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/50 p-4 sm:items-center"
      // Only a click that starts and ends on the backdrop closes. Dragging to
      // select text inside a diff and releasing outside it must not.
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-label={typeof title === "string" ? title : undefined}
        tabIndex={-1}
        className={cn(
          "flex max-h-[calc(100vh-2rem)] w-full max-w-5xl flex-col rounded-lg border bg-background shadow-lg outline-none",
        )}
      >
        <div className="flex items-start gap-3 border-b p-4">
          <div className="min-w-0 flex-1">
            <h2 className="font-semibold leading-none tracking-tight">{title}</h2>
            {subtitle && <div className="mt-1 text-sm text-muted-foreground">{subtitle}</div>}
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            <X className="size-4" />
          </button>
        </div>

        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">{children}</div>

        {footer && <div className="border-t p-4">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}
