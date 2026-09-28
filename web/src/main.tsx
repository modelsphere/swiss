import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Layout } from "@/components/Layout";
import { Deployments } from "@/routes/Deployments";
import { DeploymentDetail } from "@/routes/DeploymentDetail";
import { Catalog } from "@/routes/Catalog";
import { Model } from "@/routes/Model";
import { Deploy } from "@/routes/Deploy";
import { Upgrade } from "@/routes/Upgrade";
import { Runs } from "@/routes/Runs";
import { Nodes } from "@/routes/Nodes";
import { SiteProfile } from "@/routes/SiteProfile";
import { Login } from "@/routes/Login";
import { Setup } from "@/routes/Setup";
import { PreviewDeploySettings } from "@/routes/PreviewDeploySettings";
import { PreviewSLO } from "@/routes/PreviewSLO";
import { Gate } from "@/components/Session";
import { ToastProvider } from "@/components/ui/toast";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // The server is the source of truth; nothing here is cached durably or
      // written optimistically. Cluster state is what it is.
      staleTime: 5_000,
      retry: 1,
      refetchOnWindowFocus: true,
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <BrowserRouter>
          <Routes>
            {/* Outside the gate: the login is what the gate sends you to, and
                the setup page is what it sends you to when there is no profile
                yet -- neither can be behind the thing that redirects to it. */}
            <Route path="login" element={<Login />} />
            {import.meta.env.DEV && (
              <Route path="preview/deploy-settings" element={<PreviewDeploySettings />} />
            )}
            {import.meta.env.DEV && <Route path="preview/slo" element={<PreviewSLO />} />}
            <Route
              path="setup"
              element={
                <Gate>
                  <Setup />
                </Gate>
              }
            />
            <Route
              element={
                <Gate>
                  <Layout />
                </Gate>
              }
            >
              <Route index element={<Deployments />} />
              <Route path="deployments/:namespace/:release" element={<DeploymentDetail />} />
              <Route path="nodes" element={<Nodes />} />
              <Route path="site-profile" element={<SiteProfile />} />
              <Route path="runs" element={<Runs />} />
              <Route path="catalog" element={<Catalog />} />
              <Route path="catalog/:name" element={<Model />} />
              <Route path="deploy/:name" element={<Deploy />} />
              <Route path="upgrade/:namespace/:release" element={<Upgrade />} />
              <Route path="*" element={<div className="text-sm text-muted-foreground">Not found.</div>} />
            </Route>
          </Routes>
        </BrowserRouter>
      </ToastProvider>
    </QueryClientProvider>
  </StrictMode>,
);
