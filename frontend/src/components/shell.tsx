import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowsClockwiseIcon, CubeIcon, FolderOpenIcon, WarningIcon } from "@phosphor-icons/react";
import { ClusterService, RouteService } from "@bindings/internal/bindings";
import type { Cluster } from "@bindings/internal/service";
import { ClusterOverview } from "@/components/cluster-overview";
import { ImportExport } from "@/components/import-export";
import { PortForwards } from "@/components/forwards";
import { Logs } from "@/components/logs";
import { PodShell } from "@/components/terminal";
import { RouteList, statusLabel, StateDot } from "@/components/routes";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { configQuery, reachabilityLabel, reachabilityQuery } from "@/queries";
import { useUIStore } from "@/store";
import { cn } from "@/lib/utils";

export function Shell() {
  const queryClient = useQueryClient();
  const importKubeconfig = useMutation({
    mutationFn: () => ClusterService.Import(),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["config"] }),
  });

  return (
    <div className="flex h-screen bg-background text-foreground">
      <aside className="flex w-64 shrink-0 flex-col border-r">
        <div className="flex h-12 items-center gap-1 border-b px-4 text-sm font-medium">
          <span className="flex-1">Clusters</span>
          <Button
            variant="ghost"
            size="icon-sm"
            title="Refresh reachability"
            onClick={() => queryClient.invalidateQueries({ queryKey: ["cluster"] })}
          >
            <ArrowsClockwiseIcon />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            title="Import kubeconfig"
            disabled={importKubeconfig.isPending}
            onClick={() => importKubeconfig.mutate()}
          >
            <FolderOpenIcon />
          </Button>
        </div>
        {importKubeconfig.error && (
          <p className="border-b px-4 py-2 text-xs text-destructive">{String(importKubeconfig.error)}</p>
        )}
        <div className="min-h-0 flex-1 overflow-auto">
          <ClusterList />
        </div>
        <RouteList />
        <ImportExport />
      </aside>
      <main className="flex min-w-0 flex-1 flex-col">
        <ClusterTabs />
      </main>
    </div>
  );
}

function ClusterList() {
  const { data, error } = useQuery(configQuery);
  const { selectedClusterId, selectCluster } = useUIStore();
  const clusters = data?.clusters ?? [];

  if (error) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <WarningIcon />
          </EmptyMedia>
          <EmptyTitle>Configuration could not be loaded</EmptyTitle>
          <EmptyDescription>{String(error)}</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  if (clusters.length === 0) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <CubeIcon />
          </EmptyMedia>
          <EmptyTitle>No clusters</EmptyTitle>
          <EmptyDescription>Import a kubeconfig to add your first cluster.</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  return (
    <ul className="flex flex-col gap-1 p-2">
      {clusters.map((c) => (
        <li key={c.id}>
          <button
            type="button"
            onClick={() => selectCluster(c.id)}
            className={cn(
              "flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-sm hover:bg-accent",
              c.id === selectedClusterId && "bg-accent",
            )}
          >
            <ReachabilityDot clusterId={c.id} />
            <span className="truncate">{c.name}</span>
          </button>
        </li>
      ))}
    </ul>
  );
}

function ReachabilityDot({ clusterId }: { clusterId: string }) {
  const { status, error, data } = useQuery(reachabilityQuery(clusterId));
  return (
    <span
      title={reachabilityLabel(status, error, data)}
      className={cn(
        "size-2 shrink-0 rounded-full",
        status === "pending" && "animate-pulse bg-muted-foreground",
        status === "success" && "bg-green-500",
        status === "error" && "bg-red-500",
      )}
    />
  );
}

function ClusterTabs() {
  const selectedClusterId = useUIStore((s) => s.selectedClusterId);
  const activeTab = useUIStore((s) => s.activeTab);
  const selectTab = useUIStore((s) => s.selectTab);
  const { data } = useQuery(configQuery);
  const cluster = data?.clusters?.find((c) => c.id === selectedClusterId);

  if (!cluster) {
    return (
      <Empty className="h-full border-0">
        <EmptyHeader>
          <EmptyTitle>Select a cluster</EmptyTitle>
          <EmptyDescription>Port Forwards, Logs and Shell live here.</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  return (
    <>
      <ClusterHeader cluster={cluster} />
      <Tabs value={activeTab} onValueChange={(tab) => selectTab(tab as typeof activeTab)} className="min-h-0 flex-1">
        <TabsList className="m-2">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="forwards">Port Forwards</TabsTrigger>
          <TabsTrigger value="logs">Logs</TabsTrigger>
          <TabsTrigger value="shell">Shell</TabsTrigger>
        </TabsList>
        <TabsContent value="overview" className="overflow-auto">
          <ClusterOverview key={cluster.id} cluster={cluster} />
        </TabsContent>
        <TabsContent value="forwards" className="overflow-auto">
          <PortForwards key={cluster.id} cluster={cluster} />
        </TabsContent>
        <TabsContent value="logs" className="flex min-h-0 flex-col">
          <Logs key={cluster.id} cluster={cluster} />
        </TabsContent>
        <TabsContent value="shell" className="flex min-h-0 flex-col">
          <PodShell key={cluster.id} cluster={cluster} />
        </TabsContent>
      </Tabs>
    </>
  );
}

function ClusterHeader({ cluster }: { cluster: Cluster }) {
  const queryClient = useQueryClient();
  const { data: version, status, error } = useQuery(reachabilityQuery(cluster.id));
  const { data } = useQuery(configQuery);
  const routeStatus = useUIStore((s) => s.routeStatuses[cluster.route]);
  const options = [{ value: "", label: "Direct" }, ...(data?.routes ?? []).map((r) => ({ value: r.id, label: r.name }))];
  const setRoute = useMutation({
    mutationFn: (routeId: string) => RouteService.SetClusterRoute(cluster.id, routeId),
    onSuccess: () => queryClient.invalidateQueries(),
  });
  return (
    <div className="flex h-12 items-center gap-3 border-b px-4">
      <span className="text-sm font-medium">{cluster.name}</span>
      <span className="truncate text-xs text-muted-foreground">{cluster.kubeconfig}</span>
      <Select value={cluster.route} items={options} onValueChange={(id) => setRoute.mutate(id ?? "")}>
        <SelectTrigger size="sm" className="ml-auto" title={statusLabel(routeStatus)}>
          {cluster.route && <StateDot status={routeStatus} />}
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((o) => (
            <SelectItem key={o.value} value={o.value}>
              {o.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <span className={cn("text-xs", status === "error" ? "text-destructive" : "text-muted-foreground")}>
        {reachabilityLabel(status, error, version)}
      </span>
    </div>
  );
}
