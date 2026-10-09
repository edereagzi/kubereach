import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  FileIcon,
  HardDrivesIcon,
  MagnifyingGlassIcon,
  PlusIcon,
  WarningIcon,
} from "@phosphor-icons/react";
import { Events } from "@wailsio/runtime";
import { ClusterService, ConfigService } from "@bindings/internal/bindings";
import { State, type Cluster, type RemoteKubeconfig, type RouteStatus } from "@bindings/internal/service";
import { isUp, RouteDialog, serverChain, StateDot, SudoPasswordDialog, useRouteProblem } from "@/components/routes";
import { SidebarFooter } from "@/components/sidebar-footer";
import { RefreshButton } from "@/components/refresh-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { configQuery, errorText, isSudoRequired, reachabilityLabel, reachabilityQuery } from "@/queries";
import { useUIStore } from "@/store";
import { keyLabel, useCommand } from "@/lib/commands";
import { cn } from "@/lib/utils";

export function Sidebar() {
  const queryClient = useQueryClient();
  const [needle, setNeedle] = useState("");
  const invalidateConfig = () => queryClient.invalidateQueries({ queryKey: ["config"] });
  const importKubeconfig = useMutation({ mutationFn: () => ClusterService.Import(), onSettled: invalidateConfig });
  // Dropped export files queue up for the import dialog; everything else is imported as a kubeconfig in one go.
  const pushImportPreviews = useUIStore((s) => s.pushImportPreviews);
  const importDropped = useMutation({
    mutationFn: async (paths: string[]) => {
      const previews = await Promise.all(paths.map((p) => ConfigService.InspectPath(p)));
      pushImportPreviews(previews.filter((p) => !p.kubeconfig));
      const kubeconfigs = previews.filter((p) => p.kubeconfig).map((p) => p.path);
      return kubeconfigs.length > 0 ? ClusterService.ImportPaths(kubeconfigs) : null;
    },
    onSettled: invalidateConfig,
  });
  useEffect(() => Events.On("files:dropped", ({ data }) => importDropped.mutate(data)), [importDropped.mutate]);
  const importError = importKubeconfig.error ?? importDropped.error;
  const [importingRemote, setImportingRemote] = useState(false);

  // Log streams, Shells and Terminals are not queries, so they stay open.
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["cluster"] });
  useCommand("refresh", refresh);
  // In the order of the list without its filter; 9 is the last, as in browser tabs.
  const selectCluster = useUIStore((s) => s.selectCluster);
  const clusters = useQuery(configQuery).data?.clusters ?? [];
  useCommand("select-cluster", (e) => {
    const n = Number(e.code.at(-1));
    const cluster = n === 9 ? clusters.at(-1) : clusters[n - 1];
    if (cluster) selectCluster(cluster.id);
  });

  return (
    <aside className="flex w-64 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground">
      <div data-drag className="flex h-13 shrink-0 items-center justify-end gap-0.5 px-2 select-none">
        {/* One slow Cluster must not hold the button; each row's dot pulses while its own check runs. */}
        <RefreshButton
          title={`Refresh clusters (${keyLabel("refresh")})`}
          fetching={false}
          onRefresh={refresh}
        />
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" title="Add clusters" disabled={importKubeconfig.isPending} />}>
            <PlusIcon />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-56">
            <DropdownMenuItem onClick={() => importKubeconfig.mutate()}>
              <FileIcon /> From a kubeconfig file…
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => setImportingRemote(true)}>
              <HardDrivesIcon /> From an SSH server…
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {importingRemote && <RemoteImportDialog onClose={() => setImportingRemote(false)} />}
      <div className="px-3 pb-2">
        <InputGroup className="h-7 bg-background/60">
          <InputGroupInput placeholder="Filter clusters" value={needle} onChange={(e) => setNeedle(e.target.value)} onKeyDown={(e) => e.key === "Escape" && setNeedle("")} />
          <InputGroupAddon>
            <MagnifyingGlassIcon />
          </InputGroupAddon>
        </InputGroup>
      </div>
      {importError && <p className="px-4 pb-2 text-xs text-destructive">{errorText(importError)}</p>}
      <h2 className="px-4 pt-1 pb-1 text-[11px] font-semibold text-muted-foreground">Clusters</h2>
      <div className="min-h-0 flex-1 overflow-auto pb-2">
        <ClusterList needle={needle} />
      </div>
      <SidebarFooter />
    </aside>
  );
}

// Adds a Cluster whose kubeconfig stays on the Route's last SSH server and is read there every time the Route connects.
// Picking a Route connects it; its credential and host key prompts come from RouteConnector as anywhere else.
function RemoteImportDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const { data } = useQuery(configQuery);
  const routes = data?.routes ?? [];
  const [routeId, setRouteId] = useState(routes[0]?.id ?? "");
  const [picked, setPicked] = useState<string | null>(null);
  const [creatingRoute, setCreatingRoute] = useState(false);
  const [askingSudo, setAskingSudo] = useState(true);
  const route = routes.find((r) => r.id === routeId);
  const status = useUIStore((s) => s.routeStatuses[routeId]);
  const requestConnect = useUIStore((s) => s.requestConnect);
  const selectCluster = useUIStore((s) => s.selectCluster);
  const problem = useRouteProblem(routeId);
  const connected = status?.state === State.StateConnected;

  useEffect(() => {
    if (routeId && !isUp(useUIStore.getState().routeStatuses[routeId])) requestConnect(routeId);
  }, [routeId, requestConnect]);

  const found = useQuery({
    queryKey: ["remote-contexts", routeId],
    queryFn: () => ClusterService.RemoteContexts(routeId),
    enabled: connected,
    retry: false,
    gcTime: 0,
  });
  const source = found.data?.source;
  const context = picked ?? found.data?.contexts?.[0];
  const add = useMutation({
    mutationFn: () => ClusterService.ImportRemote(routeId, source!, context!),
    onSuccess: async (cluster) => {
      await queryClient.invalidateQueries({ queryKey: ["config"] });
      selectCluster(cluster.id);
      onClose();
    },
  });
  const host = route?.servers?.at(-1)?.host;

  return (
    <>
      <Dialog open onOpenChange={(open) => !open && onClose()}>
        <DialogContent>
          <form
            className="contents"
            onSubmit={(e) => {
              e.preventDefault();
              add.mutate();
            }}
          >
            <DialogHeader>
              <DialogTitle>Add a cluster from an SSH server</DialogTitle>
              <DialogDescription>
                {route ? (
                  <>
                    Kubereach reads {source ? <span className="font-mono text-foreground">{source}</span> : "the kubeconfig it finds"} on <span className="font-medium text-foreground">{host}</span> each time{" "}
                    {route.name} connects. Nothing is copied to this computer.
                  </>
                ) : (
                  "The cluster is reached through a route, and its kubeconfig is read on the route's last SSH server. Add a route to that server first."
                )}
              </DialogDescription>
            </DialogHeader>
            {routes.length > 0 && (
              <div className="grid gap-1.5">
                <Label>Route</Label>
                <Select
                  value={routeId}
                  items={routes.map((r) => ({ value: r.id, label: r.name }))}
                  onValueChange={(id) => {
                    setRouteId(id ?? "");
                    setPicked(null);
                    setAskingSudo(true);
                  }}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {routes.map((r) => (
                      <SelectItem key={r.id} value={r.id}>
                        {r.name}
                        <span className="truncate text-muted-foreground">{serverChain(r)}</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
            {route && (
              <RemoteSource
                routeName={route.name}
                status={status}
                problem={problem}
                onRetry={() => requestConnect(routeId)}
                found={found}
                context={context}
                onPick={setPicked}
                onSudo={() => setAskingSudo(true)}
              />
            )}
            {add.error && <p className="text-sm text-destructive">{errorText(add.error)}</p>}
            <DialogFooter>
              {routes.length === 0 ? (
                <Button type="button" onClick={() => setCreatingRoute(true)}>
                  <PlusIcon /> New route
                </Button>
              ) : (
                <>
                  <Button type="button" variant="ghost" onClick={onClose}>
                    Cancel
                  </Button>
                  <Button type="submit" disabled={!context || add.isPending}>
                    Add cluster
                  </Button>
                </>
              )}
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      {askingSudo && <SudoPasswordDialog key={routeId} routeId={routeId} error={found.error} retry={found.refetch} onClose={() => setAskingSudo(false)} />}
      {creatingRoute && <RouteDialog route={null} onClose={() => setCreatingRoute(false)} onSaved={(r) => setRouteId(r.id)} />}
    </>
  );
}

// Where the read stands: connecting the Route, finding the kubeconfig, then the context to add.
function RemoteSource({
  routeName,
  status,
  problem,
  onRetry,
  found,
  context,
  onPick,
  onSudo,
}: {
  routeName: string;
  status: RouteStatus | undefined;
  problem: string | undefined;
  onRetry: () => void;
  found: { data?: RemoteKubeconfig; error: unknown };
  context: string | undefined;
  onPick: (context: string) => void;
  onSudo: () => void;
}) {
  if (problem) {
    return (
      <div className="flex items-start gap-3 text-sm">
        <p className="min-w-0 flex-1 text-destructive">{problem}</p>
        <Button type="button" variant="outline" size="xs" onClick={onRetry}>
          Retry
        </Button>
      </div>
    );
  }
  if (status?.state !== State.StateConnected) {
    return (
      <p className="flex items-center gap-2 text-sm text-muted-foreground">
        <StateDot status={status} /> Connecting {routeName}…
      </p>
    );
  }
  if (found.error) {
    return (
      <div className="flex items-start gap-3 text-sm">
        <p className="min-w-0 flex-1 text-destructive">{errorText(found.error)}</p>
        {isSudoRequired(found.error) && (
          <Button type="button" variant="outline" size="xs" onClick={onSudo}>
            Enter password
          </Button>
        )}
      </div>
    );
  }
  if (!found.data) return <p className="animate-pulse text-sm text-muted-foreground">Looking for a kubeconfig…</p>;
  const contexts = found.data.contexts ?? [];
  if (contexts.length === 0) return <p className="text-sm text-muted-foreground">{found.data.source} has no contexts.</p>;
  if (contexts.length === 1) {
    return (
      <p className="text-sm text-muted-foreground">
        Context <span className="font-medium text-foreground">{context}</span>
      </p>
    );
  }
  return (
    <div className="grid gap-1.5">
      <Label>Context</Label>
      <Select value={context} onValueChange={(c) => c && onPick(c)}>
        <SelectTrigger className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {contexts.map((c) => (
            <SelectItem key={c} value={c}>
              {c}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

// Clusters keep the order they were added in, so changing how one is reached never moves it.
function ClusterList({ needle }: { needle: string }) {
  const { data, error } = useQuery(configQuery);
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

  const words = needle.toLowerCase().split(/\s+/).filter(Boolean);
  const shown = clusters.filter((c) => words.every((w) => `${c.name} ${c.context}`.toLowerCase().includes(w)));

  if (clusters.length === 0) {
    return (
      <Empty className="border-0 px-4 py-6">
        <EmptyHeader>
          <EmptyTitle>No clusters</EmptyTitle>
          <EmptyDescription>Add clusters from a kubeconfig or an SSH server with +, or drop a kubeconfig here.</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }
  if (shown.length === 0) return <p className="px-4 py-2 text-xs text-muted-foreground">No cluster matches.</p>;
  return (
    <ul className="grid gap-px px-2">
      {shown.map((c) => (
        <ClusterRow key={c.id} cluster={c} />
      ))}
    </ul>
  );
}

function ClusterRow({ cluster }: { cluster: Cluster }) {
  const selected = useUIStore((s) => s.selectedClusterId === cluster.id && !s.routesOpen);
  const selectCluster = useUIStore((s) => s.selectCluster);
  return (
    <li>
      <button
        type="button"
        onClick={() => selectCluster(cluster.id)}
        className={cn(
          "flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-sm outline-none hover:bg-sidebar-accent focus-visible:ring-1 focus-visible:ring-ring",
          selected && "bg-primary/10 font-medium text-primary hover:bg-primary/15",
        )}
      >
        <ReachabilityDot cluster={cluster} />
        <span className="min-w-0 flex-1 truncate">{cluster.name}</span>
      </button>
    </li>
  );
}

// Unreachable is the normal state of a cluster whose Route is down, so it is a hollow ring rather than an error colour.
export function ReachabilityDot({ cluster }: { cluster: Cluster }) {
  const { status, fetchStatus, error, data } = useQuery(reachabilityQuery(cluster.id));
  return (
    <span
      title={reachabilityLabel(status, error, data)}
      className={cn(
        "size-2 shrink-0 rounded-full",
        fetchStatus === "fetching" && "animate-pulse",
        status === "pending" && "bg-muted-foreground",
        status === "success" && "bg-green-500",
        status === "error" && "border-[1.5px] border-muted-foreground/70",
      )}
    />
  );
}
