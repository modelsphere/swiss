import { SLOCard } from "@/components/SLOCard";
import type { SLOConfig } from "@/lib/api";

// Dev-only: the SLO card against a canned slo-api, so its three states can be
// looked at without a cluster, a login, or a requirement to edit. The stub
// answers the way swissd does — it merges a PUT onto what is stored, adds
// `found`, and derives priority from highPriority — so what shows up here is
// what the real proxy would send back.
const STORE: Record<string, SLOConfig> = {
  full: {
    found: true,
    route: "glm-5",
    highPriority: true,
    priority: 10,
    minimumDeployment: { type: "replica", value: 2 },
    maximumDeployment: { type: "replica", value: 8 },
    ttft: { default: { metrics: [{ type: "p80", threshold: 20 }, { type: "p95", threshold: 35 }] } },
    otps: { default: { metrics: [{ type: "p80", threshold: 30 }] } },
  },
  // What the chart installs: a service id and one replica, nothing else.
  thin: {
    found: true,
    route: "qwen-3",
    highPriority: false,
    priority: 0,
    minimumDeployment: { type: "replica", value: 1 },
  },
  none: { found: false, route: "kimi-k2" },
};

// Installed on first render, never at import: main.tsx imports this module
// unconditionally and only the route behind it is DEV-gated, so a module-level
// patch would be hijacking fetch in a production build too.
function installStub() {
  if (!import.meta.env.DEV || Reflect.get(window, "__sloStub")) return;
  Reflect.set(window, "__sloStub", true);
  const real = window.fetch.bind(window);
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const key = url.match(/\/api\/releases\/[^/]+\/([^/]+)\/slo$/)?.[1];
    if (!key || !(key in STORE)) return real(input, init);
    await new Promise((r) => setTimeout(r, 200)); // let the loading state show
    const method = init?.method ?? "GET";
    if (method === "PUT") {
      const patch = JSON.parse(String(init?.body ?? "{}"));
      const { highPriority, ...rest } = patch;
      STORE[key] = {
        ...STORE[key],
        ...rest,
        found: true,
        highPriority: !!highPriority,
        priority: highPriority ? 10 : 0,
      };
    } else if (method === "DELETE") {
      STORE[key] = {
        found: true,
        route: STORE[key].route,
        highPriority: false,
        priority: 0,
        minimumDeployment: { type: "replica", value: 1 },
      };
    }
    return new Response(JSON.stringify(STORE[key]), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };
}

export function PreviewSLO() {
  installStub();
  return (
    <div className="mx-auto max-w-3xl space-y-6 p-6">
      <div>
        <h1 className="text-xl font-semibold">SLO card preview</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          The states the card has to hold: a fully configured requirement, the thin one the chart
          installs, a release with none, and a read-only swissd. Save and Reset work against an
          in-memory stub — no backend required.
        </p>
      </div>
      <Case title="Configured">
        <SLOCard namespace="modelforge" release="full" canEdit />
      </Case>
      <Case title="As the chart installs it">
        <SLOCard namespace="modelforge" release="thin" canEdit />
      </Case>
      <Case title="No requirement registered">
        <SLOCard namespace="modelforge" release="none" canEdit />
      </Case>
      <Case title="Read-only swissd">
        <SLOCard namespace="modelforge" release="full" canEdit={false} />
      </Case>
    </div>
  );
}

function Case({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="space-y-2">
      <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{title}</div>
      {children}
    </div>
  );
}
