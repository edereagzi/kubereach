import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { Events } from "@wailsio/runtime";
import { ForwardService, LogService, RouteService, ShellService } from "@bindings/internal/bindings";
import { writeShellOutput } from "@/components/terminal";
import { configQuery } from "@/queries";
import { router } from "@/router";
import { useUIStore } from "@/store";
import "@/theme";
import "@/index.css";

const queryClient = new QueryClient();
const {
  setRouteStatus,
  setForwardStatus,
  setLogStatus,
  appendLogs,
  setEventStatus,
  appendEvents,
  setShellStatus,
  addHostKeyPrompt,
  removeHostKeyPrompt,
  selectCluster,
  selectTab,
} = useUIStore.getState();

// Any state change after a host key prompt means the prompt was answered or the Route was stopped.
Events.On("route:state", ({ data }) => {
  setRouteStatus(data);
  removeHostKeyPrompt(data.routeId);
  for (const cluster of queryClient.getQueryData(configQuery.queryKey)?.clusters ?? []) {
    if (cluster.route === data.routeId) queryClient.invalidateQueries({ queryKey: ["cluster", cluster.id] });
  }
});
Events.On("cluster:changed", ({ data }) => {
  for (const kind of data.kinds ?? []) queryClient.invalidateQueries({ queryKey: ["cluster", data.clusterId, kind] });
});
Events.On("route:hostkey", ({ data }) => addHostKeyPrompt(data));
Events.On("forward:state", ({ data }) => setForwardStatus(data));
Events.On("logs:state", ({ data }) => setLogStatus(data));
Events.On("logs:lines", ({ data }) => appendLogs(data));
Events.On("events:state", ({ data }) => setEventStatus(data));
Events.On("events:batch", ({ data }) => appendEvents(data));
Events.On("shell:state", ({ data }) => setShellStatus(data));
Events.On("shell:output", ({ data }) => writeShellOutput(data));
Events.On("tray:open-cluster", ({ data }) => {
  selectCluster(data);
  selectTab("overview");
});
RouteService.Statuses().then((statuses) => statuses?.forEach(setRouteStatus));
ForwardService.Statuses().then((statuses) => statuses?.forEach(setForwardStatus));
LogService.Statuses().then((statuses) => statuses?.forEach(setLogStatus));
ShellService.Statuses().then((statuses) => statuses?.forEach(setShellStatus));

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
