import { useEffect, useRef } from "react";
import { useMutation } from "@tanstack/react-query";
import { PlusIcon, TerminalIcon, XIcon } from "@phosphor-icons/react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { ShellService } from "@bindings/internal/bindings";
import { State, type Cluster, type ShellOutput, type ShellStatus, type ShellTarget } from "@bindings/internal/service";
import { statusLabel, StateDot } from "@/components/routes";
import { TargetPicker, useTargets } from "@/components/targets";
import { Button } from "@/components/ui/button";
import { ComboboxTrigger } from "@/components/ui/combobox";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import { useUIStore } from "@/store";
import { useTheme } from "@/theme";
import { cn } from "@/lib/utils";

// Surfaces follow the app background; the ANSI palette stays xterm's default in both modes.
const xtermTheme = (dark: boolean) =>
  dark ? { background: "#13161c" } : { background: "#ffffff", foreground: "#0a0a0a", cursor: "#0a0a0a", selectionBackground: "#0a0a0a33" };

// Terminals live outside React so output arriving before the view mounts is kept; xterm buffers writes until open().
const terminals = new Map<string, Terminal>();
useTheme.subscribe((s) => terminals.forEach((t) => (t.options.theme = xtermTheme(s.dark))));
const terminalFor = (id: string) => {
  let term = terminals.get(id);
  if (!term) {
    term = new Terminal({ fontSize: 12, cursorBlink: true, scrollback: 5000, theme: xtermTheme(useTheme.getState().dark) });
    terminals.set(id, term);
  }
  return term;
};

// Output for a session that was closed is dropped, so a late chunk never revives its terminal.
export function writeShellOutput({ sessionId, data }: ShellOutput) {
  if (data && useUIStore.getState().shellSessions[sessionId]) {
    terminalFor(sessionId).write(Uint8Array.from(atob(data), (c) => c.charCodeAt(0)));
  }
}

const ended = (s: ShellStatus) => s.state === State.StateStopped || s.state === State.StateError;

export type ShellPod = { namespace: string; name: string; containers: string[] };

function useStartShell(clusterId: string) {
  const selectTab = useUIStore((s) => s.selectTab);
  return useMutation({
    mutationFn: (target: Omit<ShellTarget, "clusterId">) => ShellService.Start({ ...target, clusterId }, 80, 24),
    onSuccess: () => selectTab("shell"),
  });
}

// OpenShell starts a session with one click, or after a pick when there are several pods or containers to choose from.
export function OpenShell({
  cluster,
  pods,
  variant = "outline",
  size = "sm",
  className,
}: {
  cluster: Cluster;
  pods: ShellPod[];
  variant?: "outline" | "ghost";
  size?: "sm" | "xs";
  className?: string;
}) {
  const start = useStartShell(cluster.id);
  const items = pods.flatMap((p) =>
    (p.containers.length > 1 ? p.containers : [""]).map((container) => ({
      value: `${p.namespace}/${p.name}/${container}`,
      label: container ? `${p.namespace}/${p.name} · ${container}` : `${p.namespace}/${p.name}`,
      target: { namespace: p.namespace, pod: p.name, container },
    })),
  );
  if (items.length === 1) {
    return (
      <Button variant={variant} size={size} className={className} title={`Shell into ${items[0]!.label}`} disabled={start.isPending} onClick={() => start.mutate(items[0]!.target)}>
        {variant === "outline" && <TerminalIcon />}
        Shell
      </Button>
    );
  }
  return (
    <Select
      value=""
      items={items}
      onValueChange={(key) => {
        const item = items.find((i) => i.value === key);
        if (item) start.mutate(item.target);
      }}
    >
      <SelectTrigger
        size="sm"
        className={cn(size === "xs" && "h-6 px-2 text-xs", variant === "ghost" && "border-transparent bg-transparent dark:bg-transparent", className)}
        title="Open a shell into a pod"
        disabled={items.length === 0 || start.isPending}
      >
        {variant === "outline" && <TerminalIcon />}
        Shell
      </SelectTrigger>
      <SelectContent>
        {items.map((i) => (
          <SelectItem key={i.value} value={i.value}>
            {i.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

export function PodShell({ cluster }: { cluster: Cluster }) {
  const { groups, error: listError } = useTargets(cluster);
  const sessions = Object.values(useUIStore((s) => s.shellSessions)).filter((st) => st.target.clusterId === cluster.id);
  const activeShellId = useUIStore((s) => s.activeShellId);
  const { selectShell, closeShell } = useUIStore.getState();
  const active = sessions.find((s) => s.id === activeShellId);
  const start = useStartShell(cluster.id);
  const close = (session: ShellStatus) => {
    terminals.get(session.id)?.dispose();
    terminals.delete(session.id);
    closeShell(session.id);
    if (!ended(session)) void ShellService.Stop(session.id);
  };
  // A pod with several containers is listed once per container, so the pick already says which one.
  const shellGroups = groups
    .filter((g) => g.label === "Pods")
    .map((g) => ({
      ...g,
      items: g.items.flatMap((t) =>
        t.containers.length > 1 ? t.containers.map((c) => ({ ...t, value: `${t.value}/${c}`, label: `${t.label} · ${c}`, containers: [c], container: c })) : [t],
      ),
    }));
  const picker = (
    <TargetPicker groups={shellGroups} placeholder="Search pods" onPick={(t) => start.mutate({ namespace: t.namespace, pod: t.name, container: t.container ?? "" })}>
      <ComboboxTrigger render={<Button variant="ghost" size="sm" className="mb-1 text-muted-foreground" />} disabled={start.isPending}>
        <PlusIcon /> New shell
      </ComboboxTrigger>
    </TargetPicker>
  );
  const error = listError ?? start.error;

  if (!active) {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex items-center gap-2 px-4 py-2.5">{picker}</div>
        {error && <p className="px-4 pb-2 text-xs text-destructive">{String(error)}</p>}
        <Empty className="border-0">
          <EmptyHeader>
            <EmptyTitle>No shell open</EmptyTitle>
            <EmptyDescription>Pick a pod, or one container of a pod, to open a terminal into it.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-end gap-0.5 px-4 pt-2.5">
        {sessions.map((s) => {
          const on = s.id === activeShellId;
          return (
            <div
              key={s.id}
              className={cn(
                "-mb-px flex h-8 items-center gap-2 rounded-t-md border border-b-0 pr-1 pl-2.5 text-sm",
                on ? "relative z-10 bg-background text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              <button type="button" className="flex items-center gap-2 outline-none focus-visible:underline" title={statusLabel(s)} onClick={() => selectShell(s.id)}>
                <StateDot status={s} />
                {s.target.pod}
                <span className="text-xs text-muted-foreground">{ended(s) ? "ended" : s.target.container}</span>
              </button>
              <Button variant="ghost" size="icon-xs" title="Close" onClick={() => close(s)}>
                <XIcon />
              </Button>
            </div>
          );
        })}
        {picker}
        {error && <span className="mb-1 truncate text-xs text-destructive">{String(error)}</span>}
      </div>
      <TerminalView key={active.id} session={active} />
    </div>
  );
}

function TerminalView({ session }: { session: ShellStatus }) {
  const ref = useRef<HTMLDivElement>(null);
  const start = useStartShell(session.target.clusterId);
  const done = ended(session);
  const doneRef = useRef(done);
  doneRef.current = done;

  useEffect(() => {
    const term = terminalFor(session.id);
    const fit = new FitAddon();
    term.loadAddon(fit);
    // A terminal opens once; on a later mount its element is moved under the new host.
    if (term.element) ref.current!.appendChild(term.element);
    else term.open(ref.current!);
    const data = term.onData((d) => {
      if (!doneRef.current) void ShellService.Write(session.id, d);
    });
    const resize = term.onResize(({ cols, rows }) => void ShellService.Resize(session.id, cols, rows));
    const observer = new ResizeObserver(() => fit.fit());
    observer.observe(ref.current!);
    fit.fit();
    term.focus();
    return () => {
      observer.disconnect();
      data.dispose();
      resize.dispose();
      fit.dispose();
    };
  }, [session.id]);

  return (
    <div className="relative mx-4 mb-4 min-h-0 flex-1 overflow-hidden rounded-md rounded-tl-none border p-1">
      <div ref={ref} className="h-full" />
      {done && (
        <div className={cn("absolute inset-x-0 bottom-0 px-3 py-1 text-xs", session.state === State.StateError ? "bg-destructive text-white" : "bg-muted text-muted-foreground")}>
          {session.state === State.StateError ? `Failed: ${session.error}` : "Session ended"}
          <button type="button" className="ml-3 underline underline-offset-2" onClick={() => start.mutate(session.target)}>
            Open again
          </button>
        </div>
      )}
    </div>
  );
}
