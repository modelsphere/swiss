// A small YAML emitter for display only. Nothing parses this back: it renders a
// composed plan the way an overrides file looks, so a reviewer reads what they
// would have written by hand.

export function toYaml(value: unknown, indent = 0): string {
  const pad = " ".repeat(indent);

  if (Array.isArray(value)) {
    if (value.length === 0) return `${pad}[]\n`;
    return value
      .map((item) => {
        if (isScalar(item)) return `${pad}- ${scalar(item)}\n`;
        const body = toYaml(item, indent + 2);
        return `${pad}-${body.slice(indent + 1)}`;
      })
      .join("");
  }

  if (isRecord(value)) {
    const keys = Object.keys(value);
    if (keys.length === 0) return `${pad}{}\n`;
    return keys
      .map((k) => {
        const v = value[k];
        if (isScalar(v)) return `${pad}${k}: ${scalar(v)}\n`;
        if (Array.isArray(v) && v.length === 0) return `${pad}${k}: []\n`;
        if (isRecord(v) && Object.keys(v).length === 0) return `${pad}${k}: {}\n`;
        return `${pad}${k}:\n${toYaml(v, indent + 2)}`;
      })
      .join("");
  }

  return `${pad}${scalar(value)}\n`;
}

function isScalar(v: unknown): boolean {
  return v === null || (typeof v !== "object" && typeof v !== "undefined");
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function scalar(v: unknown): string {
  if (v === null || v === undefined) return "null";
  if (typeof v !== "string") return String(v);
  return needsQuote(v) ? JSON.stringify(v) : v;
}

// Quote anything YAML would read back as something other than this string. A
// leading "-" is fine unless it is a bare dash or a dash-space, so engine flags
// stay readable as --tp-size=8 rather than "--tp-size=8".
function needsQuote(s: string): boolean {
  return (
    s === "" ||
    s === "-" ||
    /^\s|\s$/.test(s) ||
    /\n/.test(s) ||
    /^(true|false|yes|no|on|off|null|~)$/i.test(s) ||
    /^-?\d+(\.\d+)?([eE][-+]?\d+)?$/.test(s) ||
    /^[-?:,[\]{}#&*!|>'"%@`]\s/.test(s) ||
    /^[?:,[\]{}#&*!|>'"%@`]/.test(s) ||
    /:\s/.test(s) ||
    /\s#/.test(s) ||
    s.endsWith(":")
  );
}
