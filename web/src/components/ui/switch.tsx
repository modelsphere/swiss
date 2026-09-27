import { cn } from "@/lib/utils";

// On is the success green, off is the muted track. The knob stays white in
// both themes so the two states read at a glance rather than as black on white.
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
        "relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border transition-colors",
        "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-success",
        "disabled:cursor-not-allowed disabled:opacity-50",
        checked ? "border-success bg-success" : "border-border bg-muted",
        !disabled && "cursor-pointer",
      )}
    >
      <span
        className={cn(
          "absolute top-1/2 size-4 -translate-y-1/2 rounded-full bg-white shadow-sm ring-1 ring-black/10 transition-[left]",
          checked ? "left-[22px]" : "left-0.5",
        )}
      />
    </button>
  );
}
