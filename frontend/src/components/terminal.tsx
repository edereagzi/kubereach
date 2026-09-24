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
import { errorText } from "@/queries";
import { useUIStore } from "@/store";
import { useTheme } from "@/theme";
import { cn } from "@/lib/utils";

// Surfaces follow the app background; the ANSI palette stays xterm's default in both modes.
const xtermTheme = (dark: boolean) =>
  dark ? { background: "#13161c" } : { background: "#ffffff", foreground: "#0a0a0a", cursor: "#0a0a0a", selectionBackground: "#0a0a0a33" };

// xterm measures its cell once on open, so the app's mono face must already be loaded by then.
const fontFamily = "'Geist Mono Variable', monospace";
void document.fonts.load(`12px ${fontFamily}`);

// Terminals live outside React so output arriving before the view mounts is kept; xterm buffers writes until open().
const terminals = new Map<string, Terminal>();
useTheme.subscribe((s) => terminals.forEach((t) => (t.options.theme = xtermTheme(s.dark))));
const terminalFor = (id: string) => {
  let term = terminals.get(id);
  if (!term) {
    term = new Terminal({ fontFamily, fontSize: 12, lineHeight: 1.25, cursorBlink: true, scrollback: 5000, theme: xtermTheme(useTheme.getState().dark) });
    terminals.set(id, term);
  }
  return term;
};

// A terminal goes with its session, however the session left: closed here or with its Cluster.
useUIStore.subscribe((s, prev) => {
  if (s.shellSessions === prev.shellSessions) return;
  terminals.forEach((term, id) => {
    if (s.shellSessions[id]) return;
    term.dispose();
    terminals.delete(id);
  });
});

// Binding calls can overtake each other, so one write is in flight per session and keys typed meanwhile follow it together.
const queuedInput = new Map<string, string>();
const sendInput = (id: string, data: string) => {
  const queued = queuedInput.get(id);
  if (queued !== undefined) {
    queuedInput.set(id, queued + data);
    return;
  }
  queuedInput.set(id, "");
  void ShellService.Write(id, data).finally(() => {
    const next = queuedInput.get(id);
    queuedInput.delete(id);
    if (next) sendInput(id, next);
  });
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
  onStarted,
}: {
  cluster: Cluster;
  pods: ShellPod[];
  variant?: "outline" | "ghost";
  size?: "sm" | "xs";
  className?: string;
  onStarted?: () => void;
}) {
  const start = useStartShell(cluster.id);
  const items = pods.flatMap((p) =>
    (p.containers.length > 1 ? p.containers : [""]).map((container) => ({
      value: `${p.namespace}/${p.name}/${container}`,
      // One pod's list only needs its containers; the pod is already on the row or in the toolbar.
      label: container && pods.length === 1 ? container : container ? `${p.namespace}/${p.name} · ${container}` : `${p.namespace}/${p.name}`,
      target: { namespace: p.namespace, pod: p.name, container },
    })),
  );
  if (items.length === 1) {
    return (
      <Button variant={variant} size={size} className={className} title={`Shell into ${items[0]!.label}`} disabled={start.isPending} onClick={() => start.mutate(items[0]!.target, { onSuccess: onStarted })}>
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
        if (item) start.mutate(item.target, { onSuccess: onStarted });
      }}
    >
      <SelectTrigger
        size="sm"
        className={cn(
          "font-medium data-placeholder:text-foreground",
          size === "xs" && "gap-1 px-2 py-0 text-xs data-[size=sm]:h-6",
          variant === "ghost" && "border-transparent bg-transparent dark:bg-transparent",
          className,
        )}
        title="Open a shell into a pod"
        disabled={items.length === 0 || start.isPending}
      >
        {variant === "outline" && <TerminalIcon />}
        Shell
      </SelectTrigger>
      <SelectContent alignItemWithTrigger={false} align="end">
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
  const activeShellId = useUIStore((s) => s.activeShellIds[cluster.id]);
  const { selectShell, closeShell } = useUIStore.getState();
  const active = sessions.find((s) => s.id === activeShellId);
  const start = useStartShell(cluster.id);
  const close = (session: ShellStatus) => {
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
      <ComboboxTrigger render={<Button variant="ghost" size="sm" className="text-muted-foreground" />} disabled={start.isPending}>
        <PlusIcon /> New shell
      </ComboboxTrigger>
    </TargetPicker>
  );
  const error = listError ?? start.error;

  if (!active) {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex items-center gap-2 px-4 py-2.5">{picker}</div>
        {error && <p className="px-4 pb-2 text-xs text-destructive">{errorText(error)}</p>}
        <Empty className="justify-start border-0 pt-12">
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
      <div className="flex h-11 items-center gap-1 border-b px-4">
        {sessions.map((s) => {
          const on = s.id === activeShellId;
          return (
            <div
              key={s.id}
              className={cn(
                "group flex h-7 items-center gap-2 rounded-md pr-1 pl-2.5 text-sm",
                on ? "bg-muted text-foreground" : "text-muted-foreground hover:bg-muted/50 hover:text-foreground",
              )}
            >
              <button type="button" className="flex items-center gap-2 outline-none focus-visible:underline" title={statusLabel(s)} onClick={() => selectShell(s.id)}>
                <StateDot status={s} />
                {s.target.pod}
                <span className="text-xs text-muted-foreground">{ended(s) ? "ended" : s.target.container}</span>
              </button>
              <Button variant="ghost" size="icon-xs" title="Close" className={cn(!on && "opacity-0 group-hover:opacity-100 focus-visible:opacity-100")} onClick={() => close(s)}>
                <XIcon />
              </Button>
            </div>
          );
        })}
        {picker}
        {error && <span className="truncate text-xs text-destructive">{errorText(error)}</span>}
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
      if (!doneRef.current) sendInput(session.id, d);
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
    <div className="relative min-h-0 flex-1 overflow-hidden py-2 pl-4 pr-1">
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
