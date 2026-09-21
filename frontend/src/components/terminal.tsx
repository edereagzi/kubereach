import { useEffect, useRef } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { TerminalIcon, XIcon } from "@phosphor-icons/react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { ShellService } from "@bindings/internal/bindings";
import { State, type Cluster, type ShellOutput, type ShellStatus, type ShellTarget } from "@bindings/internal/service";
import { statusLabel, StateDot } from "@/components/routes";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import { podsQuery } from "@/queries";
import { useUIStore } from "@/store";
import { cn } from "@/lib/utils";

// Terminals live outside React so output arriving before the view mounts is kept; xterm buffers writes until open().
const terminals = new Map<string, Terminal>();
const terminalFor = (id: string) => {
  let term = terminals.get(id);
  if (!term) {
    term = new Terminal({ fontSize: 12, cursorBlink: true, scrollback: 5000 });
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

// OpenShell starts a session with one click, or after a pick when there are several pods or containers to choose from.
export function OpenShell({ cluster, pods }: { cluster: Cluster; pods: ShellPod[] }) {
  const selectTab = useUIStore((s) => s.selectTab);
  const start = useMutation({
    mutationFn: (target: Omit<ShellTarget, "clusterId">) => ShellService.Start({ clusterId: cluster.id, ...target }, 80, 24),
    onSuccess: () => selectTab("shell"),
  });
  const items = pods.flatMap((p) =>
    (p.containers.length > 1 ? p.containers : [""]).map((container) => ({
      value: `${p.namespace}/${p.name}/${container}`,
      label: container ? `${p.namespace}/${p.name} · ${container}` : `${p.namespace}/${p.name}`,
      target: { namespace: p.namespace, pod: p.name, container },
    })),
  );
  if (items.length === 1) {
    return (
      <Button variant="outline" size="sm" title={`Shell into ${items[0]!.label}`} disabled={start.isPending} onClick={() => start.mutate(items[0]!.target)}>
        <TerminalIcon />
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
      <SelectTrigger size="sm" title="Open a shell into a pod" disabled={items.length === 0 || start.isPending}>
        <TerminalIcon />
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
  const pods = useQuery(podsQuery(cluster.id));
  const sessions = Object.values(useUIStore((s) => s.shellSessions)).filter((st) => st.target.clusterId === cluster.id);
  const activeShellId = useUIStore((s) => s.activeShellId);
  const { selectShell, closeShell } = useUIStore.getState();
  const active = sessions.find((s) => s.id === activeShellId);
  const close = (session: ShellStatus) => {
    terminals.get(session.id)?.dispose();
    terminals.delete(session.id);
    closeShell(session.id);
    if (!ended(session)) void ShellService.Stop(session.id);
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 p-2">
      <div className="flex items-center gap-1">
        {sessions.map((s) => (
          <div key={s.id} className={cn("flex items-center gap-1 rounded-md border pl-2 text-sm", s.id === activeShellId && "bg-accent")}>
            <button type="button" className="flex items-center gap-2 py-1" title={statusLabel(s)} onClick={() => selectShell(s.id)}>
              <StateDot status={s} />
              {s.target.pod}
              <span className="text-xs text-muted-foreground">{s.target.container}</span>
            </button>
            <Button variant="ghost" size="icon-sm" title="Close" onClick={() => close(s)}>
              <XIcon />
            </Button>
          </div>
        ))}
        <OpenShell cluster={cluster} pods={(pods.data ?? []).map((p) => ({ namespace: p.namespace, name: p.name, containers: p.containers ?? [] }))} />
        {pods.error && <span className="truncate text-xs text-destructive">{String(pods.error)}</span>}
      </div>
      {active ? (
        <TerminalView key={active.id} session={active} />
      ) : (
        <Empty className="border-0">
          <EmptyHeader>
            <EmptyTitle>No shell open</EmptyTitle>
            <EmptyDescription>Pick a pod to open a terminal into it.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
    </div>
  );
}

function TerminalView({ session }: { session: ShellStatus }) {
  const ref = useRef<HTMLDivElement>(null);
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
    <div className="relative min-h-0 flex-1 overflow-hidden rounded-md bg-black p-1">
      <div ref={ref} className="h-full" />
      {done && (
        <div className={cn("absolute inset-x-0 bottom-0 px-3 py-1 text-xs", session.state === State.StateError ? "bg-destructive text-white" : "bg-muted text-muted-foreground")}>
          {session.state === State.StateError ? `Failed: ${session.error}` : "Session ended"}
        </div>
      )}
    </div>
  );
}
