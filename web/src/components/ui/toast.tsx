import { createContext, useCallback, useContext, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { AlertCircle, CircleCheck, X } from "lucide-react";
import { cn } from "@/lib/utils";

// Transient confirmation for a write, over the page rather than inside the card
// that started it: an outcome that only renders where the button was is an
// outcome nobody sees after scrolling away from it.
//
// A success fades -- it has been read by the time it does, and a banner saying
// "saved" three minutes later is noise. A failure stays until it is dismissed:
// it is something to act on, and the message is often the only description of
// what the server refused.
type Kind = "success" | "error";

interface Item {
  id: number;
  kind: Kind;
  text: string;
}

export interface Toaster {
  success: (text: string) => void;
  error: (text: string) => void;
}

const SUCCESS_MS = 3500;

const Ctx = createContext<Toaster | null>(null);

// Outside a provider this is a no-op rather than a crash: a component rendered
// in a preview or a test should not have to mount one to be looked at.
const noop: Toaster = { success: () => {}, error: () => {} };

export function useToast(): Toaster {
  return useContext(Ctx) ?? noop;
}

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<Item[]>([]);
  const nextId = useRef(0);

  const dismiss = useCallback((id: number) => {
    setItems((list) => list.filter((t) => t.id !== id));
  }, []);

  const push = useCallback(
    (kind: Kind, text: string) => {
      const id = ++nextId.current;
      setItems((list) => [...list, { id, kind, text }]);
      if (kind === "success") setTimeout(() => dismiss(id), SUCCESS_MS);
    },
    [dismiss],
  );

  const api = useMemo<Toaster>(
    () => ({
      success: (text) => push("success", text),
      error: (text) => push("error", text),
    }),
    [push],
  );

  return (
    <Ctx.Provider value={api}>
      {children}
      {createPortal(<Viewport items={items} onDismiss={dismiss} />, document.body)}
    </Ctx.Provider>
  );
}

// Portalled and fixed, so a card with its own overflow or stacking context
// cannot clip or bury it. Above the hover hints, which sit at z-50.
function Viewport({ items, onDismiss }: { items: Item[]; onDismiss: (id: number) => void }) {
  if (items.length === 0) return null;
  return (
    <div className="pointer-events-none fixed inset-x-0 top-4 z-[60] flex flex-col items-center gap-2 px-4">
      {items.map((t) => {
        const bad = t.kind === "error";
        // The same mark ErrorState uses, so a failure reads the same whether it
        // arrives over the page or inside a card.
        const Icon = bad ? AlertCircle : CircleCheck;
        return (
          <div
            key={t.id}
            role={bad ? "alert" : "status"}
            aria-live={bad ? "assertive" : "polite"}
            className={cn(
              "pointer-events-auto flex w-full max-w-md items-start gap-2.5 rounded-lg border p-3",
              "bg-card text-sm text-card-foreground shadow-lg",
              bad ? "border-destructive/30" : "border-success/30",
            )}
          >
            <Icon className={cn("mt-0.5 size-4 shrink-0", bad ? "text-destructive" : "text-success")} />
            <div className="min-w-0 flex-1 break-words leading-snug">{t.text}</div>
            <button
              type="button"
              aria-label="Dismiss"
              className="-m-1 shrink-0 rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
              onClick={() => onDismiss(t.id)}
            >
              <X className="size-4" />
            </button>
          </div>
        );
      })}
    </div>
  );
}

// The message a failed mutation should show. ApiError carries the server's own
// sentence, which is the useful half -- "slo server is not ready (...)" beats
// anything this layer could say about it.
export function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
