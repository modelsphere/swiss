import { useState } from "react";
import { DeploySettings, EMPTY, type Form } from "@/components/DeploySettings";

// Dev-only: Deploy settings routing mock without session or catalog.
export function PreviewDeploySettings() {
  const [form, setForm] = useState<Form>({
    ...EMPTY,
    serviceId: "glm-5",
    modelRoute: true,
    route: "glm-5",
    cart: true,
    // One known knob and one this build has never heard of: a route carrying a
    // key newer than the UI still has to be editable rather than invisible.
    nginxExtras: [
      { key: "adaptive_cc_min", value: "8" },
      { key: "some_future_knob", value: "7" },
    ],
  });
  return (
    <div className="mx-auto max-w-3xl space-y-4 p-6">
      <div>
        <h1 className="text-xl font-semibold">Deploy settings preview</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Core deploy settings, Routing options (SLO-first TTFT/TPS, CART, adaptive, monitor), then Advanced.
          SLO, adaptive concurrency, and autoscale default on. Static TTFT/TPS stay fully visible when SLO is on
          and are labeled "(fallback)" with captions. Monitor (enable, model, GPU type) lives on Routing.
          CART max load and the adaptive rows mute until their switch is on. Adaptive floor fraction is a
          named row; every other openresty knob is added from the Add setting dialog, including a custom key.
          ServiceMonitor lives under Advanced. No backend required.
        </p>
      </div>
      <DeploySettings
        form={form}
        onChange={(patch) => setForm((f) => ({ ...f, ...patch }))}
        serviceIdPlaceholder="glm-5"
        localPathPlaceholder="/mnt/models/glm-5"
        image={{
          catalog: "lmsysorg/sglang:latest",
          site: "harbor.example/sglang:latest",
        }}
        supportedGPUs={["NVIDIA-H100-80GB-HBM3", "NVIDIA-B300-SXM6-AC"]}
        clusterGPUs={["NVIDIA-H100-80GB-HBM3", "NVIDIA-B300-SXM6-AC"]}
      />
    </div>
  );
}
