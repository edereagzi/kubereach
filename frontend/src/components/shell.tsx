import { useEffect, useRef, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CaretDownIcon, CaretUpIcon, CubeIcon, DotsThreeIcon, PlusIcon, ScrollIcon, TerminalWindowIcon, WarningIcon, XIcon } from "@phosphor-icons/react";
import { ClusterService, LogService, RouteService, ShellService, TerminalService } from "@bindings/internal/bindings";
import { State, type Cluster, type LogStatus, type ShellStatus, type TerminalStatus } from "@bindings/internal/service";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { ClusterOverview, NamespaceScope } from "@/components/cluster-overview";
import { ClusterNodes, NodeProblems } from "@/components/nodes";
import { ClusterEvents, eventStreamFor } from "@/components/events";
import { forwardsFor, PortForwards } from "@/components/forwards";
import { InspectorSlot } from "@/components/inspector";
import { logDetail, LogTab } from "@/components/logs";
import { openTerminal, ShellView, TerminalView } from "@/components/terminal";
import { RouteChip, RouteConnector, RouteDialog, RoutesPage, StateDot, statusLabel, SudoPasswordDialog, useRouteProblem } from "@/components/routes";
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
import { configQuery, errorText, isSudoRequired, reachabilityLabel, reachabilityQuery } from "@/queries";
import { sessionEnded, useUIStore, type MainTab } from "@/store";
import { cn, modKey } from "@/lib/utils";

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
            <EmptyDescription>Its workloads, port forwards, logs, shells and Terminals open here.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      </>
    );
  }

  return (
    <>
      <ClusterHeader key={`header-${cluster.id}`} cluster={cluster} />
      <RouteBanner key={`banner-${cluster.id}`} cluster={cluster} />
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

function EventsLive({ cluster }: { cluster: Cluster }) {
  const stream = useUIStore((s) => eventStreamFor(s.eventStreams, cluster));
  return stream ? <StateDot status={stream} /> : null;
}

const dockMin = 120;
// The view keeps at least this much of the window above the dock.
const viewMin = 220;

// Dock holds a Cluster's log streams, shells and Terminals under whichever view is open, one tab per session, the way an editor keeps its panels.
function Dock({ cluster }: { cluster: Cluster }) {
  const open = useUIStore((s) => s.dockOpen);
  const height = useUIStore((s) => s.dockHeight);
  const setDockOpen = useUIStore((s) => s.setDockOpen);
  const setDockHeight = useUIStore((s) => s.setDockHeight);
  const order = useUIStore((s) => s.dockOrder);
  const logStreams = useUIStore((s) => s.logStreams);
  const shellSessions = useUIStore((s) => s.shellSessions);
  const terminalSessions = useUIStore((s) => s.terminalSessions);
  const newTerminal = useMutation({ mutationFn: () => openTerminal(cluster.id) });
  const picked = useUIStore((s) => s.dockPicks[cluster.id]);
  const tabs = order.flatMap((id): DockSession[] => {
    const stream = logStreams[id];
    if (stream?.source.clusterId === cluster.id) {
      return [
        {
          id,
          status: stream,
          icon: <ScrollIcon />,
          label: `${stream.source.namespace}/${stream.source.name}`,
          detail: logDetail(stream),
          close: () => void LogService.Stop(id),
          view: <LogTab key={id} cluster={cluster} stream={stream} />,
        },
      ];
    }
    const shell = shellSessions[id];
    if (shell?.target.clusterId === cluster.id) {
      const done = sessionEnded(shell);
      return [
        {
          id,
          status: shell,
          icon: <CubeIcon />,
          label: shell.target.pod,
          detail: done ? "ended" : shell.target.container,
          close: () => {
            useUIStore.getState().closeShell(id);
            if (!done) void ShellService.Stop(id);
          },
          view: <ShellView key={id} session={shell} />,
        },
      ];
    }
    const terminal = terminalSessions[id];
    if (terminal?.clusterId === cluster.id) {
      const done = sessionEnded(terminal);
      return [
        {
          id,
          status: terminal,
          icon: <TerminalWindowIcon />,
          label: "Terminal",
          // The namespace kubectl opens on, else the shell running.
          detail: done ? "ended" : terminal.namespace || (terminal.shell.split(/[\\/]/).pop() ?? ""),
          close: () => {
            useUIStore.getState().closeTerminal(id);
            if (!done) void TerminalService.Stop(id);
          },
          view: <TerminalView key={id} session={terminal} />,
        },
      ];
    }
    return [];
  });
  // The picked tab, or the latest when the picked one was closed.
  const active = tabs.find((t) => t.id === picked) ?? tabs.at(-1);

  // The strip scrolls sideways, with a wheel too, and fades out on each side where it goes on.
  const listRef = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState({ left: false, right: false });
  const measure = () => {
    const el = listRef.current;
    if (el) setEdges({ left: el.scrollLeft > 0, right: el.scrollLeft + el.clientWidth < el.scrollWidth - 1 });
  };
  const ids = tabs.map((t) => t.id).join(" ");
  useEffect(() => {
    const el = listRef.current;
    if (!el) return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    measure();
    // A vertical wheel or swipe scrolls the strip sideways and nothing else; React's wheel listener is passive, so this one
    // is added here to keep the page under it from scrolling too. A sideways swipe is left to the browser.
    const onWheel = (e: WheelEvent) => {
      if (Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return;
      e.preventDefault();
      el.scrollLeft += e.deltaY;
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => {
      observer.disconnect();
      el.removeEventListener("wheel", onWheel);
    };
  }, [ids]);
  const fade = 32;
  const mask = `linear-gradient(to right, ${edges.left ? "transparent" : "black"}, black ${fade}px, black calc(100% - ${fade}px), ${edges.right ? "transparent" : "black"})`;

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

  // An empty dock has nothing to show, so it stays a bar.
  const expanded = open && !!active;
  return (
    <section className="relative flex shrink-0 flex-col border-t" style={expanded ? { height: Math.min(height, window.innerHeight - viewMin) } : undefined}>
      {expanded && <div role="separator" aria-orientation="horizontal" className="absolute inset-x-0 -top-1 z-20 h-2 cursor-row-resize" onPointerDown={resize} />}
      <div className="flex h-9 shrink-0 items-stretch gap-2 pr-2 pl-4">
        {tabs.length > 0 && (
          <div
            ref={listRef}
            role="tablist"
            className="flex min-w-0 items-stretch gap-4 overflow-x-auto overscroll-x-contain [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
            style={{ maskImage: mask }}
            onScroll={measure}
          >
            {tabs.map((t) => (
              <DockTab key={t.id} session={t} cluster={cluster} active={open && t.id === active?.id} />
            ))}
          </div>
        )}
        {/* Beside tabs it adds one; alone on the bar, with no tab to be taken for, it shows what it opens, 8px in like the bar's other end. */}
        <Button variant="ghost" size="icon-xs" className={cn("my-auto", tabs.length === 0 && "-ml-2")} title={`Open a Terminal on ${cluster.name}`} disabled={newTerminal.isPending} onClick={() => newTerminal.mutate()}>
          {tabs.length > 0 ? <PlusIcon /> : <TerminalWindowIcon className="size-4" />}
        </Button>
        {newTerminal.error && (
          <span className="my-auto min-w-0 truncate text-xs text-destructive" title={errorText(newTerminal.error)}>
            {errorText(newTerminal.error)}
          </span>
        )}
        <span className="flex-1" />
        {/* The panel's own control stands apart from the tabs; an empty panel has nothing to show or hide. */}
        {tabs.length > 0 && (
          <>
            <span className="my-auto h-4 w-px shrink-0 bg-border" />
            <Button variant="ghost" size="icon-xs" className="my-auto" title={`${open ? "Hide" : "Show"} panel (${modKey}J)`} onClick={() => setDockOpen(!open)}>
              {open ? <CaretDownIcon /> : <CaretUpIcon />}
            </Button>
          </>
        )}
      </div>
      {expanded && active.view}
    </section>
  );
}

type DockSession = { id: string; status: LogStatus | ShellStatus | TerminalStatus; icon: ReactNode; label: string; detail: string; close: () => void; view: ReactNode };

// DockTab names its session's kind with the icon, its state with the dot on the icon, and its source with the label.
function DockTab({ session, cluster, active }: { session: DockSession; cluster: Cluster; active: boolean }) {
  const openDock = useUIStore((s) => s.openDock);
  const { id, status, icon, label, detail, close } = session;
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (active) ref.current?.scrollIntoView({ behavior: "smooth", block: "nearest", inline: "nearest" });
  }, [active]);
  return (
    <div
      ref={ref}
      className={cn(
        "group relative flex shrink-0 items-center gap-1 text-sm text-muted-foreground hover:text-foreground",
        active && "text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground",
      )}
    >
      <button
        type="button"
        role="tab"
        aria-selected={active}
        title={`${label}${detail ? ` · ${detail}` : ""}: ${statusLabel(status)}`}
        className="flex items-center gap-1.5 whitespace-nowrap outline-none focus-visible:underline"
        onClick={() => openDock(cluster.id, id)}
      >
        <span className="relative flex [&_svg]:size-4">
          {icon}
          <span className="absolute -right-0.5 -bottom-0.5 flex rounded-full ring-2 ring-background">
            <StateDot status={status} />
          </span>
        </span>
        <span className="max-w-64 truncate">{label}</span>
        {detail && <span className="text-xs text-muted-foreground">{detail}</span>}
      </button>
      <Button variant="ghost" size="icon-xs" title="Close" className={cn("size-5", !active && "opacity-0 group-hover:opacity-100 focus-visible:opacity-100")} onClick={close}>
        <XIcon />
      </Button>
    </div>
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
  if (!route) return null;
  if (status?.state === State.StateConnected) return cluster.remote ? <SudoBanner cluster={cluster} /> : null;
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

// SudoBanner asks again for the sudo password a remote Cluster's kubeconfig needs, as it is kept only for the session.
function SudoBanner({ cluster }: { cluster: Cluster }) {
  const queryClient = useQueryClient();
  const { error } = useQuery(reachabilityQuery(cluster.id));
  const [asking, setAsking] = useState(false);
  if (!isSudoRequired(error)) return null;
  return (
    <div className="flex h-10 shrink-0 items-center gap-2.5 border-y bg-muted/50 px-4 text-sm">
      <WarningIcon className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate">{errorText(error)}</span>
      <Button size="xs" onClick={() => setAsking(true)}>
        Enter password
      </Button>
      {asking && (
        <SudoPasswordDialog
          routeId={cluster.route}
          error={error}
          retry={() => queryClient.invalidateQueries({ queryKey: ["cluster", cluster.id] })}
          onClose={() => setAsking(false)}
        />
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
      const { shellSessions, closeShell, terminalSessions, closeTerminal } = useUIStore.getState();
      for (const s of Object.values(shellSessions)) if (s.target.clusterId === cluster.id) closeShell(s.id);
      for (const t of Object.values(terminalSessions)) if (t.clusterId === cluster.id) closeTerminal(t.id);
      selectCluster(null);
      queryClient.invalidateQueries();
    },
  });
  return (
    <div data-drag className="flex h-13 shrink-0 items-center gap-3 px-4 select-none">
      <div className="flex min-w-0 flex-1 items-baseline gap-3">
        <span className="truncate text-base font-semibold">{cluster.name}</span>
        {cluster.context !== cluster.name && (
          <span className="truncate font-mono text-xs text-muted-foreground" title={cluster.remote ? `${cluster.remote} on the SSH server` : cluster.kubeconfig}>
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
          {/* A Cluster read from an SSH server is bound to the Route that reaches that server. */}
          {!cluster.remote && (
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
          )}
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
        description="Kubereach forgets this cluster and deletes its port forwards; open logs, shells and terminals close. The cluster and your kubeconfig stay as they are."
        confirm="Remove cluster"
        destructive
        action={remove}
      />
    </div>
  );
}
