import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { Events } from "@wailsio/runtime";
import { ForwardService, LogService, RouteService, ShellService, TerminalService } from "@bindings/internal/bindings";
import { restoreSession, writeSessionOutput } from "@/components/terminal";
import { configQuery, reachabilityQuery } from "@/queries";
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
  setTerminalStatus,
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
// An open detail is fetched under its kind's singular key, so it follows its list, through an outage and back included.
const detailKey: Record<string, string> = { pods: "pod", workloads: "workload", nodes: "node", pvcs: "pvc", hpas: "hpa", ingresses: "ingress", configmaps: "cm", secrets: "secret" };
Events.On("cluster:changed", ({ data }) => {
  for (const kind of data.kinds ?? []) {
    queryClient.invalidateQueries({ queryKey: ["cluster", data.clusterId, kind] });
    if (detailKey[kind]) queryClient.invalidateQueries({ queryKey: ["cluster", data.clusterId, detailKey[kind]] });
  }
  // A Cluster first found unreachable, say before a VPN came up, is asked again once its watches hear from it.
  const reachability = reachabilityQuery(data.clusterId).queryKey;
  if (queryClient.getQueryState(reachability)?.status === "error") queryClient.invalidateQueries({ queryKey: reachability });
});
Events.On("route:hostkey", ({ data }) => addHostKeyPrompt(data));
Events.On("forward:state", ({ data }) => setForwardStatus(data));
Events.On("logs:state", ({ data }) => setLogStatus(data));
Events.On("logs:lines", ({ data }) => appendLogs(data));
Events.On("events:state", ({ data }) => setEventStatus(data));
Events.On("events:batch", ({ data }) => appendEvents(data));
Events.On("shell:state", ({ data }) => setShellStatus(data));
Events.On("shell:output", ({ data }) => writeSessionOutput(data));
Events.On("terminal:state", ({ data }) => setTerminalStatus(data));
Events.On("terminal:output", ({ data }) => writeSessionOutput(data));
Events.On("tray:open-cluster", ({ data }) => {
  selectCluster(data);
  selectTab("overview");
});
RouteService.Statuses().then((statuses) => statuses?.forEach(setRouteStatus));
ForwardService.Statuses().then((statuses) => statuses?.forEach(setForwardStatus));
LogService.Statuses().then((statuses) => statuses?.forEach(setLogStatus));
ShellService.Statuses().then((statuses) =>
  statuses?.forEach((s) => {
    setShellStatus(s);
    restoreSession(s.id, ShellService.Tail(s.id));
  }),
);
TerminalService.Statuses().then((statuses) =>
  statuses?.forEach((s) => {
    setTerminalStatus(s);
    restoreSession(s.id, TerminalService.Tail(s.id));
  }),
);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
