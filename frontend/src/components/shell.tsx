import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowsClockwiseIcon, CubeIcon, DotsThreeIcon, PlusIcon, WarningIcon } from "@phosphor-icons/react";
import { Events } from "@wailsio/runtime";
import { ClusterService, ConfigService, RouteService } from "@bindings/internal/bindings";
import type { Cluster } from "@bindings/internal/service";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { ClusterOverview } from "@/components/cluster-overview";
import { ClusterNodes, NodeProblems } from "@/components/nodes";
import { ClusterEvents, eventStreamFor } from "@/components/events";
import { forwardsFor, PortForwards } from "@/components/forwards";
import { Logs, streamFor } from "@/components/logs";
import { PodShell } from "@/components/terminal";
import { RouteList, statusLabel, StateDot } from "@/components/routes";
import { SidebarFooter } from "@/components/sidebar-footer";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { configQuery, reachabilityLabel, reachabilityQuery, errorText } from "@/queries";
import { State } from "@bindings/internal/service";
import { useUIStore } from "@/store";
import { cn } from "@/lib/utils";

export function Shell() {
  const queryClient = useQueryClient();
  const importKubeconfig = useMutation({
    mutationFn: () => ClusterService.Import(),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["config"] }),
  });
  // Dropped export files queue up for the import dialog; everything else is imported as a kubeconfig in one go.
  const pushImportPreviews = useUIStore((s) => s.pushImportPreviews);
  const importDropped = useMutation({
    mutationFn: async (paths: string[]) => {
      const previews = await Promise.all(paths.map((p) => ConfigService.InspectPath(p)));
      pushImportPreviews(previews.filter((p) => !p.kubeconfig));
      const kubeconfigs = previews.filter((p) => p.kubeconfig).map((p) => p.path);
      return kubeconfigs.length > 0 ? ClusterService.ImportPaths(kubeconfigs) : null;
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["config"] }),
  });
  useEffect(() => Events.On("files:dropped", ({ data }) => importDropped.mutate(data)), [importDropped.mutate]);
  const importError = importKubeconfig.error ?? importDropped.error;

  return (
    <div className="flex h-screen bg-background text-foreground">
      <aside className="flex w-60 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground">
        <div className="flex h-10 items-center gap-0.5 px-3 pt-1 text-xs font-medium text-muted-foreground">
          <span className="flex-1 pl-1">Clusters</span>
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
            title="Add cluster from kubeconfig"
            disabled={importKubeconfig.isPending}
            onClick={() => importKubeconfig.mutate()}
          >
            <PlusIcon />
          </Button>
        </div>
        {importError && <p className="px-4 py-1 text-xs text-destructive">{errorText(importError)}</p>}
        <div className="min-h-0 flex-1 overflow-auto">
          <ClusterList />
          <RouteList />
        </div>
        <SidebarFooter />
      </aside>
      <main className="flex min-w-0 flex-1 flex-col">
        <ClusterTabs />
      </main>
    </div>
  );
}

function ClusterList() {
  const { data, error } = useQuery(configQuery);
  const selectedClusterId = useUIStore((s) => s.selectedClusterId);
  const selectCluster = useUIStore((s) => s.selectCluster);
  const clusters = data?.clusters ?? [];

  if (error) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <WarningIcon />
          </EmptyMedia>
          <EmptyTitle>Configuration could not be loaded</EmptyTitle>
          <EmptyDescription>{errorText(error)}</EmptyDescription>
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
    <ul className="flex flex-col gap-px px-2">
      {clusters.map((c) => (
        <li key={c.id}>
          <button
            type="button"
            onClick={() => selectCluster(c.id)}
            className={cn(
              "flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-sm hover:bg-sidebar-accent",
              c.id === selectedClusterId && "bg-primary/10 font-medium text-primary hover:bg-primary/15",
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

// Unreachable is the normal state of a cluster whose Route is down, so it is a hollow ring rather than an error colour.
function ReachabilityDot({ clusterId }: { clusterId: string }) {
  const { status, error, data } = useQuery(reachabilityQuery(clusterId));
  return (
    <span
      title={reachabilityLabel(status, error, data)}
      className={cn(
        "size-2 shrink-0 rounded-full",
        status === "pending" && "animate-pulse bg-muted-foreground",
        status === "success" && "bg-green-500",
        status === "error" && "border-[1.5px] border-muted-foreground/70",
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
      <Tabs value={activeTab} onValueChange={(tab) => selectTab(tab as typeof activeTab)} className="min-h-0 flex-1 gap-0">
        <TabsList variant="line" className="h-9 w-full justify-start gap-5 border-b px-4">
          <TabsTrigger value="overview" className="flex-none px-0">Overview</TabsTrigger>
          <TabsTrigger value="nodes" className="flex-none px-0">
            Nodes
            <NodeProblems cluster={cluster} />
          </TabsTrigger>
          <TabsTrigger value="events" className="flex-none px-0">
            Events
            <EventsLive cluster={cluster} />
          </TabsTrigger>
          <TabsTrigger value="forwards" className="flex-none px-0">
            Port forwards
            <TabCount value={forwardsFor(data?.forwards, cluster).length} />
          </TabsTrigger>
          <TabsTrigger value="logs" className="flex-none px-0">
            Logs
            <LogsLive cluster={cluster} />
          </TabsTrigger>
          <TabsTrigger value="shell" className="flex-none px-0">
            Shell
            <ShellCount cluster={cluster} />
          </TabsTrigger>
        </TabsList>
        <TabsContent value="overview" className="flex min-h-0 flex-col">
          <ClusterOverview key={cluster.id} cluster={cluster} />
        </TabsContent>
        <TabsContent value="nodes" className="flex min-h-0 flex-col">
          <ClusterNodes key={cluster.id} cluster={cluster} />
        </TabsContent>
        <TabsContent value="events" className="flex min-h-0 flex-col">
          <ClusterEvents key={cluster.id} cluster={cluster} />
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

// Counts and the live dot let a tab say what is running on it before it is opened.
function TabCount({ value }: { value: number }) {
  return value > 0 ? (
    <span className="rounded-full bg-muted px-1.5 text-[11px] leading-4 text-muted-foreground tabular-nums">{value}</span>
  ) : null;
}

function LogsLive({ cluster }: { cluster: Cluster }) {
  const stream = useUIStore((s) => streamFor(s.logStreams, cluster));
  return stream ? <StateDot status={stream} /> : null;
}

function EventsLive({ cluster }: { cluster: Cluster }) {
  const stream = useUIStore((s) => eventStreamFor(s.eventStreams, cluster));
  return stream ? <StateDot status={stream} /> : null;
}

function ShellCount({ cluster }: { cluster: Cluster }) {
  const open = useUIStore(
    (s) => Object.values(s.shellSessions).filter((x) => x.target.clusterId === cluster.id && x.state !== State.StateStopped && x.state !== State.StateError).length,
  );
  return <TabCount value={open} />;
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
  const selectCluster = useUIStore((s) => s.selectCluster);
  const [removing, setRemoving] = useState(false);
  const remove = useMutation({
    mutationFn: () => ClusterService.Delete(cluster.id),
    onSuccess: () => {
      const { shellSessions, closeShell } = useUIStore.getState();
      for (const s of Object.values(shellSessions)) if (s.target.clusterId === cluster.id) closeShell(s.id);
      selectCluster(null);
      queryClient.invalidateQueries();
    },
  });
  return (
    <div className="flex h-14 items-center gap-3 px-4">
      <div className="flex min-w-0 flex-1 items-baseline gap-3">
        <span className="truncate text-base font-semibold">{cluster.name}</span>
        {cluster.context !== cluster.name && (
          <span className="truncate font-mono text-xs text-muted-foreground" title={cluster.kubeconfig}>
            {cluster.context}
          </span>
        )}
      </div>
      <Select value={cluster.route} items={options} onValueChange={(id) => setRoute.mutate(id ?? "")}>
        <SelectTrigger size="sm" title={statusLabel(routeStatus)}>
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
      <span
        title={reachabilityLabel(status, error, version)}
        className={cn(
          "inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs",
          status === "error" ? "text-muted-foreground" : "text-foreground",
        )}
      >
        <span
          className={cn(
            "size-2 rounded-full",
            status === "pending" && "animate-pulse bg-muted-foreground",
            status === "success" && "bg-green-500",
            status === "error" && "border-[1.5px] border-muted-foreground/70",
          )}
        />
        {status === "pending" ? "Checking…" : status === "error" ? "Unreachable" : version || "Reachable"}
      </span>
      <DropdownMenu>
        <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" title="More" />}>
          <DotsThreeIcon weight="bold" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            variant="destructive"
            onClick={() => {
              remove.reset();
              setRemoving(true);
            }}
          >
            Remove cluster…
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ConfirmDialog
        open={removing}
        onOpenChange={setRemoving}
        title={`Remove ${cluster.name}?`}
        description="Kubereach forgets this cluster and deletes its port forwards; open logs and shells close. The cluster and your kubeconfig stay as they are."
        confirm="Remove cluster"
        destructive
        action={remove}
      />
    </div>
  );
}
