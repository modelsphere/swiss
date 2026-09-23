import { cn } from "@/lib/utils";

// Track and knob are drawn from primary / primary-foreground, which swap
// between themes, so both states stay legible in light and dark.
export function Switch({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        "relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors",
        "focus-visible:outline-2 focus-visible:outline-offset-2",
        "disabled:cursor-not-allowed disabled:opacity-60",
        checked ? "bg-primary" : "bg-muted",
        !disabled && "cursor-pointer",
      )}
    >
      <span
        className={cn(
          "size-3.5 rounded-full transition-transform",
          checked
            ? "translate-x-[1.125rem] bg-primary-foreground"
            : "translate-x-0.5 bg-muted-foreground",
        )}
      />
    </button>
  );
}
