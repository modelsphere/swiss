import { cn } from "@/lib/utils";

export interface Tab {
  id: string;
  label: string;
  disabled?: boolean;
  hint?: string;
}

export function Tabs({
  tabs,
  active,
  onSelect,
}: {
  tabs: Tab[];
  active: string;
  onSelect: (id: string) => void;
}) {
  return (
    <div className="flex flex-wrap gap-1 border-b" role="tablist">
      {tabs.map((t) => (
        <button
          key={t.id}
          role="tab"
          aria-selected={active === t.id}
          disabled={t.disabled}
          title={t.hint}
          onClick={() => onSelect(t.id)}
          className={cn(
            "-mb-px border-b-2 px-3 py-2 text-sm transition-colors",
            active === t.id
              ? "border-foreground font-medium"
              : "border-transparent text-muted-foreground hover:text-foreground",
            t.disabled && "cursor-not-allowed opacity-40 hover:text-muted-foreground",
          )}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}
