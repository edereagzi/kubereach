import { useEffect, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CaretDownIcon, CaretUpIcon, DotsThreeIcon, ScrollIcon, TerminalWindowIcon, WarningIcon } from "@phosphor-icons/react";
import { ClusterService, RouteService } from "@bindings/internal/bindings";
import { State, type Cluster } from "@bindings/internal/service";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { ClusterOverview, NamespaceScope } from "@/components/cluster-overview";
import { ClusterNodes, NodeProblems } from "@/components/nodes";
import { ClusterEvents, eventStreamFor } from "@/components/events";
import { forwardsFor, PortForwards } from "@/components/forwards";
import { InspectorSlot } from "@/components/inspector";
import { Logs, streamFor } from "@/components/logs";
import { PodShell } from "@/components/terminal";
import { RouteChip, RouteConnector, RouteDialog, RoutesPage, StateDot, useRouteProblem } from "@/components/routes";
import { Sidebar } from "@/components/sidebar";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { configQuery, errorText, reachabilityLabel, reachabilityQuery } from "@/queries";
import { openShellCount, useUIStore, type DockTab, type MainTab } from "@/store";
import { cn } from "@/lib/utils";

export function Shell() {
  const routesOpen = useUIStore((s) => s.routesOpen);
  return (
    <div className="flex h-screen bg-background text-foreground">
      <Sidebar />
      <main className="flex min-w-0 flex-1 flex-col">{routesOpen ? <RoutesPage /> : <ClusterTabs />}</main>
      <RouteConnector />
    </div>
  );
}

function ClusterTabs() {
  const selectedClusterId = useUIStore((s) => s.selectedClusterId);
  const activeTab = useUIStore((s) => s.activeTab);
  const selectTab = useUIStore((s) => s.selectTab);
  const { data } = useQuery(configQuery);
  const [slot, setSlot] = useState<HTMLElement | null>(null);
  const cluster = data?.clusters?.find((c) => c.id === selectedClusterId);
  const routeState = useUIStore((s) => (cluster?.route ? (s.routeStatuses[cluster.route]?.state ?? State.StateIdle) : State.StateConnected));
  // Behind a Route that is down every view would only repeat the RouteBanner, so they stay empty until it connects;
  // mounted while connecting, they would list before the SSH link is up and fail. Reconnecting keeps them, so a blip
  // does not throw away a filter or an open detail.
  const unreachable =
    routeState === State.StateIdle || routeState === State.StateConnecting || routeState === State.StateStopped || routeState === State.StateError || routeState === State.$zero;

  if (!cluster) {
    return (
      <>
        <div data-drag className="h-13 shrink-0" />
        <Empty className="border-0">
          <EmptyHeader>
            <EmptyTitle>Select a cluster</EmptyTitle>
            <EmptyDescription>Its workloads, port forwards, logs and shells open here.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      </>
    );
  }

  return (
    <>
      <ClusterHeader cluster={cluster} />
      <RouteBanner cluster={cluster} />
      <Tabs value={activeTab} onValueChange={(tab) => selectTab(tab as MainTab)} className="min-h-0 flex-1 gap-0">
        <TabsList variant="line" className="h-9 w-full shrink-0 justify-start gap-5 border-b px-4">
          <TabsTrigger value="overview" className="flex-none px-0">
            Overview
          </TabsTrigger>
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
        </TabsList>
        <div className="flex min-h-0 flex-1">
          <InspectorSlot.Provider value={slot}>
            <TabsContent value="overview" className="flex min-h-0 min-w-0 flex-1 flex-col">
              {!unreachable && <ClusterOverview key={cluster.id} cluster={cluster} />}
            </TabsContent>
            <TabsContent value="nodes" className="flex min-h-0 min-w-0 flex-1 flex-col">
              {!unreachable && <ClusterNodes key={cluster.id} cluster={cluster} />}
            </TabsContent>
            <TabsContent value="events" className="flex min-h-0 min-w-0 flex-1 flex-col">
              {!unreachable && <ClusterEvents key={cluster.id} cluster={cluster} />}
            </TabsContent>
            <TabsContent value="forwards" className="min-w-0 flex-1 overflow-auto">
              <PortForwards key={cluster.id} cluster={cluster} />
            </TabsContent>
          </InspectorSlot.Provider>
          <div
            ref={setSlot}
            className="w-0 shrink-0 overflow-hidden bg-background has-[[data-slot=inspector]]:w-1/2 has-[[data-slot=inspector]]:max-w-[44rem] has-[[data-slot=inspector]]:border-l [&>*]:h-full"
          />
        </div>
      </Tabs>
      <Dock cluster={cluster} />
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
  const open = useUIStore((s) => openShellCount(s, cluster.id));
  return <TabCount value={open} />;
}

const dockMin = 120;
// The view keeps at least this much of the window above the dock.
const viewMin = 220;

// Dock holds a Cluster's log stream and shells under whichever view is open, the way an editor keeps its terminal.
function Dock({ cluster }: { cluster: Cluster }) {
  const tab = useUIStore((s) => s.dockTab);
  const open = useUIStore((s) => s.dockOpen);
  const height = useUIStore((s) => s.dockHeight);
  const setDockOpen = useUIStore((s) => s.setDockOpen);
  const setDockHeight = useUIStore((s) => s.setDockHeight);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "j" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setDockOpen(!useUIStore.getState().dockOpen);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [setDockOpen]);

  const resize = (e: React.PointerEvent) => {
    e.preventDefault();
    const from = e.clientY;
    const move = (ev: PointerEvent) => setDockHeight(Math.min(Math.max(height + from - ev.clientY, dockMin), window.innerHeight - viewMin));
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };

  return (
    <section className="relative flex shrink-0 flex-col border-t" style={open ? { height: Math.min(height, window.innerHeight - viewMin) } : undefined}>
      {open && <div role="separator" aria-orientation="horizontal" className="absolute inset-x-0 -top-1 z-20 h-2 cursor-row-resize" onPointerDown={resize} />}
      <div className="flex h-9 shrink-0 items-stretch gap-5 px-4">
        <DockButton value="logs">
          <ScrollIcon />
          Logs
          <LogsLive cluster={cluster} />
        </DockButton>
        <DockButton value="shell">
          <TerminalWindowIcon />
          Shell
          <ShellCount cluster={cluster} />
        </DockButton>
        <Button variant="ghost" size="icon-xs" className="my-auto ml-auto" title={open ? "Hide panel (⌘J)" : "Show panel (⌘J)"} onClick={() => setDockOpen(!open)}>
          {open ? <CaretDownIcon /> : <CaretUpIcon />}
        </Button>
      </div>
      {open && (tab === "logs" ? <Logs key={cluster.id} cluster={cluster} /> : <PodShell key={cluster.id} cluster={cluster} />)}
    </section>
  );
}

function DockButton({ value, children }: { value: DockTab; children: ReactNode }) {
  const active = useUIStore((s) => s.dockOpen && s.dockTab === value);
  const selectTab = useUIStore((s) => s.selectTab);
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={() => selectTab(value)}
      className={cn(
        "relative flex items-center gap-1.5 text-sm text-muted-foreground outline-none [&_svg]:size-4 hover:text-foreground focus-visible:text-foreground",
        active && "text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground",
      )}
    >
      {children}
    </button>
  );
}

// RouteBanner stands where the Cluster's views would fail, with the one action that makes them work.
function RouteBanner({ cluster }: { cluster: Cluster }) {
  const { data } = useQuery(configQuery);
  const status = useUIStore((s) => s.routeStatuses[cluster.route]);
  const requestConnect = useUIStore((s) => s.requestConnect);
  const problem = useRouteProblem(cluster.route);
  const route = data?.routes?.find((r) => r.id === cluster.route);
  if (!cluster.route) return <DirectBanner cluster={cluster} />;
  if (!route || status?.state === State.StateConnected) return null;
  const busy = status?.state === State.StateConnecting || status?.state === State.StateReconnecting;
  return (
    <div className="flex h-10 shrink-0 items-center gap-2.5 border-y bg-muted/50 px-4 text-sm">
      <StateDot status={status} />
      <span className={cn("min-w-0 flex-1 truncate", problem && !busy && "text-destructive")} title={problem}>
        {busy
          ? `Connecting through ${route.name}…`
          : problem
            ? `${route.name} failed: ${problem}`
            : `${cluster.name} is reached through ${route.name}, which is not connected.`}
      </span>
      {!busy && (
        <Button size="xs" onClick={() => requestConnect(route.id)}>
          {problem ? "Retry" : "Connect"}
        </Button>
      )}
    </div>
  );
}

// A Cluster that does not answer directly is where a Route first comes up, as one way to reach it.
function DirectBanner({ cluster }: { cluster: Cluster }) {
  const queryClient = useQueryClient();
  const { status, error } = useQuery(reachabilityQuery(cluster.id));
  const { data } = useQuery(configQuery);
  const [creating, setCreating] = useState(false);
  const setRoute = useMutation({
    mutationFn: (routeId: string) => RouteService.SetClusterRoute(cluster.id, routeId),
    onSuccess: () => queryClient.invalidateQueries(),
  });
  if (status !== "error") return null;
  return (
    <div className="flex h-10 shrink-0 items-center gap-2.5 border-y bg-muted/50 px-4 text-sm">
      <WarningIcon className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate" title={errorText(error)}>
        {errorText(error)}
      </span>
      <DropdownMenu>
        <DropdownMenuTrigger render={<Button variant="outline" size="xs" />}>Reach through a route…</DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-52">
          {data?.routes?.map((r) => (
            <DropdownMenuItem key={r.id} onClick={() => setRoute.mutate(r.id)}>
              {r.name}
            </DropdownMenuItem>
          ))}
          {!!data?.routes?.length && <DropdownMenuSeparator />}
          <DropdownMenuItem onClick={() => setCreating(true)}>New route…</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {creating && <RouteDialog route={null} onClose={() => setCreating(false)} onSaved={(r) => setRoute.mutate(r.id)} />}
    </div>
  );
}

function RenameDialog({ cluster, onClose }: { cluster: Cluster; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState(cluster.name);
  const rename = useMutation({
    mutationFn: () => ClusterService.Rename(cluster.id, name),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["config"] });
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <form
          className="contents"
          onSubmit={(e) => {
            e.preventDefault();
            rename.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>Rename {cluster.name}</DialogTitle>
            <DialogDescription>
              The name is only shown in Kubereach; context <span className="font-medium text-foreground">{cluster.context}</span> in your kubeconfig stays as it is. Leave it empty to use the
              context's name.
            </DialogDescription>
          </DialogHeader>
          <Input autoFocus value={name} placeholder={cluster.context} onChange={(e) => setName(e.target.value)} onFocus={(e) => e.target.select()} />
          {rename.error && <p className="text-sm text-destructive">{errorText(rename.error)}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={rename.isPending}>
              Rename
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ClusterHeader({ cluster }: { cluster: Cluster }) {
  const queryClient = useQueryClient();
  const { data: version, status, error } = useQuery(reachabilityQuery(cluster.id));
  const { data } = useQuery(configQuery);
  const setRoute = useMutation({
    mutationFn: (routeId: string) => RouteService.SetClusterRoute(cluster.id, routeId),
    onSuccess: () => queryClient.invalidateQueries(),
  });
  const selectCluster = useUIStore((s) => s.selectCluster);
  const [removing, setRemoving] = useState(false);
  const [renaming, setRenaming] = useState(false);
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
    <div data-drag className="flex h-13 shrink-0 items-center gap-3 px-4 select-none">
      <div className="flex min-w-0 flex-1 items-baseline gap-3">
        <span className="truncate text-base font-semibold">{cluster.name}</span>
        {cluster.context !== cluster.name && (
          <span className="truncate font-mono text-xs text-muted-foreground" title={cluster.kubeconfig}>
            {cluster.context}
          </span>
        )}
        <NamespaceScope cluster={cluster} />
      </div>
      <RouteChip cluster={cluster} />
      <span
        title={reachabilityLabel(status, error, version)}
        className={cn("inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs", status === "error" ? "text-muted-foreground" : "text-foreground")}
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
        <DropdownMenuContent align="end" className="w-64">
          <DropdownMenuSub>
            <DropdownMenuSubTrigger className="whitespace-nowrap">
              Reach through
              <span className="ml-auto min-w-0 truncate pl-4 text-muted-foreground">{data?.routes?.find((r) => r.id === cluster.route)?.name ?? "Direct"}</span>
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent>
              <DropdownMenuRadioGroup value={cluster.route} onValueChange={(id) => setRoute.mutate(id as string)}>
                <DropdownMenuRadioItem value="">Direct</DropdownMenuRadioItem>
                {data?.routes?.map((r) => (
                  <DropdownMenuRadioItem key={r.id} value={r.id}>
                    {r.name}
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
            </DropdownMenuSubContent>
          </DropdownMenuSub>
          <DropdownMenuItem onClick={() => setRenaming(true)}>Rename…</DropdownMenuItem>
          <DropdownMenuSeparator />
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
      {renaming && <RenameDialog cluster={cluster} onClose={() => setRenaming(false)} />}
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
