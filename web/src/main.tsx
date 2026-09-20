import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Layout } from "@/components/Layout";
import { Deployments } from "@/routes/Deployments";
import { Catalog } from "@/routes/Catalog";
import { Model } from "@/routes/Model";
import { Deploy } from "@/routes/Deploy";
import { Upgrade } from "@/routes/Upgrade";
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
      <BrowserRouter>
        <Routes>
          <Route element={<Layout />}>
            <Route index element={<Deployments />} />
            <Route path="catalog" element={<Catalog />} />
            <Route path="catalog/:name" element={<Model />} />
            <Route path="deploy/:name" element={<Deploy />} />
            <Route path="upgrade/:namespace/:release" element={<Upgrade />} />
            <Route path="*" element={<div className="text-sm text-muted-foreground">Not found.</div>} />
          </Route>
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
