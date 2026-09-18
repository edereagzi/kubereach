import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowsClockwiseIcon, CubeIcon, FolderOpenIcon, WarningIcon } from "@phosphor-icons/react";
import { ClusterService } from "@bindings/internal/bindings";
import type { Cluster } from "@bindings/internal/service";
import { ClusterOverview } from "@/components/cluster-overview";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
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
        <ClusterList />
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
  const { data } = useQuery(configQuery);
  const cluster = data?.clusters?.find((c) => c.id === selectedClusterId);

  if (!cluster) {
    return (
      <Empty className="h-full border-0">
        <EmptyHeader>
          <EmptyTitle>Select a cluster</EmptyTitle>
          <EmptyDescription>Port Forwards, Logs, Shell and Expose live here.</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  return (
    <>
      <ClusterHeader cluster={cluster} />
      <Tabs defaultValue="overview" className="min-h-0 flex-1">
        <TabsList className="m-2">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="forwards">Port Forwards</TabsTrigger>
          <TabsTrigger value="logs">Logs</TabsTrigger>
          <TabsTrigger value="shell">Shell</TabsTrigger>
          <TabsTrigger value="expose">Expose Mode</TabsTrigger>
        </TabsList>
        <TabsContent value="overview" className="overflow-auto">
          <ClusterOverview key={cluster.id} cluster={cluster} />
        </TabsContent>
      </Tabs>
    </>
  );
}

function ClusterHeader({ cluster }: { cluster: Cluster }) {
  const { data: version, status, error } = useQuery(reachabilityQuery(cluster.id));
  return (
    <div className="flex h-12 items-center gap-3 border-b px-4">
      <span className="text-sm font-medium">{cluster.name}</span>
      <span className="truncate text-xs text-muted-foreground">{cluster.kubeconfig}</span>
      <span className={cn("ml-auto text-xs", status === "error" ? "text-destructive" : "text-muted-foreground")}>
        {reachabilityLabel(status, error, version)}
      </span>
    </div>
  );
}
