import { useEffect, useRef } from "react";
import { useMutation } from "@tanstack/react-query";
import { TerminalIcon } from "@phosphor-icons/react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { ShellService } from "@bindings/internal/bindings";
import { State, type Cluster, type ShellOutput, type ShellStatus, type ShellTarget } from "@bindings/internal/service";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import { shellEnded, useUIStore } from "@/store";
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

export type ShellPod = { namespace: string; name: string; containers: string[] };

// A container that already has a live shell gets its tab selected rather than a second session.
function useStartShell(clusterId: string) {
  return useMutation({
    mutationFn: async (target: Omit<ShellTarget, "clusterId">) => {
      const { shellSessions, openDock, setShellStatus } = useUIStore.getState();
      const open = Object.values(shellSessions).find(
        (s) => s.target.clusterId === clusterId && s.target.namespace === target.namespace && s.target.pod === target.pod && s.target.container === target.container && !shellEnded(s),
      );
      if (open) return openDock(clusterId, open.id);
      // The session is its tab at once, before its first event.
      const status = await ShellService.Start({ ...target, clusterId }, 80, 24);
      if (!useUIStore.getState().shellSessions[status.id]) setShellStatus(status);
      openDock(clusterId, status.id);
    },
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

export function ShellView({ session }: { session: ShellStatus }) {
  const ref = useRef<HTMLDivElement>(null);
  const start = useStartShell(session.target.clusterId);
  const done = shellEnded(session);
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
          <button type="button" className="ml-3 underline underline-offset-2" onClick={() => start.mutate(session.target, { onSuccess: () => useUIStore.getState().closeShell(session.id) })}>
            Open again
          </button>
        </div>
      )}
    </div>
  );
}
