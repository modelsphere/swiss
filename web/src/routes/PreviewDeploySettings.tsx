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
  });
  return (
    <div className="mx-auto max-w-3xl space-y-4 p-6">
      <div>
        <h1 className="text-xl font-semibold">Deploy settings preview</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Core deploy settings, Routing options (CART / SLO / adaptive knobs), then Advanced.
          CART max load and Adaptive floor stay visible; each is muted until its switch is on.
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
