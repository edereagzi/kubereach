import { useEffect, useRef, useState } from "react";
import { useIsFetching, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowsLeftRightIcon,
  MagnifyingGlassIcon,
  PathIcon,
  PlusIcon,
  ScrollIcon,
  TerminalWindowIcon,
  WarningIcon,
} from "@phosphor-icons/react";
import { Events } from "@wailsio/runtime";
import { ClusterService, ConfigService } from "@bindings/internal/bindings";
import type { Cluster } from "@bindings/internal/service";
import { forwardsFor } from "@/components/forwards";
import { streamFor } from "@/components/logs";
import { isUp, useRouteProblem } from "@/components/routes";
import { SidebarFooter } from "@/components/sidebar-footer";
import { RefreshButton } from "@/components/refresh-button";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { configQuery, errorText, reachabilityLabel, reachabilityQuery } from "@/queries";
import { openShellCount, useUIStore } from "@/store";
import { cn, modKey } from "@/lib/utils";

export function Sidebar() {
  const queryClient = useQueryClient();
  const [needle, setNeedle] = useState("");
  const filter = useRef<HTMLInputElement>(null);
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
  const fetchingReachability = useIsFetching({ predicate: (q) => q.queryKey[0] === "cluster" && q.queryKey[2] === "reachability" }) > 0;

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        filter.current?.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <aside className="flex w-64 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground">
      <div data-drag className="flex h-13 shrink-0 items-center justify-end gap-0.5 px-2 select-none">
        <RefreshButton
          title="Refresh clusters"
          fetching={fetchingReachability}
          onRefresh={() => queryClient.invalidateQueries({ queryKey: ["cluster"] })}
        />
        <Button variant="ghost" size="icon-sm" title="Add clusters from kubeconfig" disabled={importKubeconfig.isPending} onClick={() => importKubeconfig.mutate()}>
          <PlusIcon />
        </Button>
      </div>
      <div className="px-3 pb-2">
        <InputGroup className="h-7 bg-background/60">
          <InputGroupInput ref={filter} placeholder="Filter clusters" value={needle} onChange={(e) => setNeedle(e.target.value)} onKeyDown={(e) => e.key === "Escape" && setNeedle("")} />
          <InputGroupAddon>
            <MagnifyingGlassIcon />
          </InputGroupAddon>
          <InputGroupAddon align="inline-end">
            <kbd className="font-sans text-[10px] text-muted-foreground">{modKey}K</kbd>
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
          <EmptyDescription>Add clusters from a kubeconfig with +, or drop the file here.</EmptyDescription>
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
          "flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-sm outline-none hover:bg-sidebar-accent focus-visible:ring-2 focus-visible:ring-ring/50",
          selected && "bg-primary/10 font-medium text-primary hover:bg-primary/15",
        )}
      >
        <ReachabilityDot cluster={cluster} />
        <span className="min-w-0 flex-1 truncate">{cluster.name}</span>
        <Activity cluster={cluster} />
        <RouteMark cluster={cluster} />
      </button>
    </li>
  );
}

// What keeps running on a Cluster while another one is looked at.
function Activity({ cluster }: { cluster: Cluster }) {
  const { data } = useQuery(configQuery);
  const forwards = forwardsFor(data?.forwards, cluster).filter((f) => f.enabled).length;
  const following = useUIStore((s) => !!streamFor(s.logStreams, cluster));
  const shells = useUIStore((s) => openShellCount(s, cluster.id));
  if (!forwards && !following && !shells) return null;
  return (
    <span className="flex shrink-0 items-center gap-2 text-[11px] font-normal text-muted-foreground tabular-nums [&_svg]:size-3.5">
      {forwards > 0 && (
        <span className="flex items-center gap-0.5" title={`${forwards} port ${forwards === 1 ? "forward" : "forwards"} on`}>
          <ArrowsLeftRightIcon />
          {forwards}
        </span>
      )}
      {following && (
        <span title="Following logs">
          <ScrollIcon />
        </span>
      )}
      {shells > 0 && (
        <span className="flex items-center gap-0.5" title={`${shells} ${shells === 1 ? "shell" : "shells"} open`}>
          <TerminalWindowIcon />
          {shells}
        </span>
      )}
    </span>
  );
}

// A Cluster behind a Route says so, since that Route being down is the usual reason the Cluster cannot be reached.
function RouteMark({ cluster }: { cluster: Cluster }) {
  const { data } = useQuery(configQuery);
  const status = useUIStore((s) => (cluster.route ? s.routeStatuses[cluster.route] : undefined));
  const problem = useRouteProblem(cluster.route ?? "");
  const route = data?.routes?.find((r) => r.id === cluster.route);
  if (!route) return null;
  const state = isUp(status) ? status?.state : "not connected";
  return (
    <span className="shrink-0 text-muted-foreground [&_svg]:size-3.5" title={`Through ${route.name}, ${state}${problem ? `: ${problem}` : ""}`}>
      <PathIcon />
    </span>
  );
}

// Unreachable is the normal state of a cluster whose Route is down, so it is a hollow ring rather than an error colour.
function ReachabilityDot({ cluster }: { cluster: Cluster }) {
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
