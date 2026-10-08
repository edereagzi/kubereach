import { useEffect, useRef, type ReactNode } from "react";
import { useMutation } from "@tanstack/react-query";
import { TerminalIcon } from "@phosphor-icons/react";
import { Clipboard } from "@wailsio/runtime";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { ShellService, TerminalService } from "@bindings/internal/bindings";
import { State, type Cluster, type ShellOutput, type ShellStatus, type SessionTail, type ShellTarget, type TerminalOutput, type TerminalStatus } from "@bindings/internal/service";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import { errorText } from "@/queries";
import { sessionEnded, useUIStore } from "@/store";
import { useTheme } from "@/theme";
import { platform } from "@/lib/commands";
import { keyOwner, pressOf } from "@/lib/key-owner";
import { cn } from "@/lib/utils";

// Surfaces follow the app background; the ANSI palette stays xterm's default in both modes.
const xtermTheme = (dark: boolean) =>
  dark ? { background: "#13161c" } : { background: "#ffffff", foreground: "#0a0a0a", cursor: "#0a0a0a", selectionBackground: "#0a0a0a33" };

// xterm measures its cell once on open, so the app's mono face must already be loaded by then.
const fontFamily = "'Geist Mono Variable', monospace";
void document.fonts.load(`12px ${fontFamily}`);

// As in iTerm's natural text editing, on macOS: Cmd kills the line (Ctrl+U) or goes to its start (Ctrl+A) or end (Ctrl+E),
// and Option moves a word back (Esc b) or forward (Esc f), where xterm would send an arrow the shell does not bind.
const cmdKeys: Record<string, string> = { Backspace: "\x15", ArrowLeft: "\x01", ArrowRight: "\x05" };
const optionKeys: Record<string, string> = { ArrowLeft: "\x1bb", ArrowRight: "\x1bf" };

// The keys the Terminal acts on itself, not the shell or the browser.
function terminalAction(t: Terminal, e: KeyboardEvent): (() => void) | undefined {
  if (platform === "mac") {
    if (e.metaKey && e.key === "k") return () => t.clear();
    const send = e.metaKey ? cmdKeys[e.key] : e.altKey && !e.ctrlKey && !e.shiftKey ? optionKeys[e.key] : undefined;
    return send ? () => t.input(send) : undefined;
  }
  if (!e.ctrlKey || !e.shiftKey || e.altKey) return;
  const key = e.key.toLowerCase();
  // ConPTY on Windows draws at the rows it remembers, so the shell also clears (Ctrl+L) and ConPTY forgets the old rows.
  if (key === "k") {
    return () => {
      t.clear();
      if (platform === "windows") t.input("\x0c");
    };
  }
  if (key === "c") {
    return () => {
      if (t.hasSelection()) void Clipboard.SetText(t.getSelection());
    };
  }
  if (key === "v") return () => void Clipboard.Text().then((text) => t.paste(text));
}

// Terminals live outside React so output arriving before the view mounts is kept; xterm buffers writes until open().
const terminals = new Map<string, Terminal>();
useTheme.subscribe((s) => terminals.forEach((t) => (t.options.theme = xtermTheme(s.dark))));
const terminalFor = (id: string) => {
  let term = terminals.get(id);
  if (!term) {
    term = new Terminal({ fontFamily, fontSize: 12, lineHeight: 1.25, cursorBlink: true, cursorStyle: "bar", scrollback: 5000, theme: xtermTheme(useTheme.getState().dark) });
    // Cmd+K (Ctrl+Shift+K elsewhere) clears the view, as in a macOS terminal; the shell is not told. On macOS the browser
    // copies and pastes. The keys the Terminal keeps do not reach the app, and the app's keys do not reach the shell.
    const t = term;
    t.attachCustomKeyEventHandler((e) => {
      if (keyOwner(pressOf(e), platform, "terminal", "anywhere") === "app") return false;
      e.stopPropagation();
      const action = terminalAction(t, e);
      if (!action) return true;
      e.preventDefault();
      if (e.type === "keydown") action();
      return false;
    });
    terminals.set(id, term);
  }
  return term;
};

// Shells and Terminals both draw into an xterm, told apart only by where their keys and size go.
type OpenSessions = { shellSessions: Record<string, ShellStatus>; terminalSessions: Record<string, TerminalStatus> };
const isOpen = (s: OpenSessions, id: string) => !!(s.shellSessions[id] || s.terminalSessions[id]);
type SessionIO = { Write: (id: string, data: string) => Promise<void>; Resize: (id: string, cols: number, rows: number) => Promise<void> };

// A terminal goes with its session, however the session left: closed here or with its Cluster.
useUIStore.subscribe((s, prev) => {
  if (s.shellSessions === prev.shellSessions && s.terminalSessions === prev.terminalSessions) return;
  terminals.forEach((term, id) => {
    if (isOpen(s, id)) return;
    term.dispose();
    terminals.delete(id);
  });
});

// Binding calls can overtake each other, so one write is in flight per session and keys typed meanwhile follow it together.
const queuedInput = new Map<string, string>();
const sendInput = (io: SessionIO, id: string, data: string) => {
  const queued = queuedInput.get(id);
  if (queued !== undefined) {
    queuedInput.set(id, queued + data);
    return;
  }
  queuedInput.set(id, "");
  void io.Write(id, data).finally(() => {
    const next = queuedInput.get(id);
    queuedInput.delete(id);
    if (next) sendInput(io, id, next);
  });
};

// Output for a session that was closed is dropped, so a late chunk never revives its terminal.
export function writeSessionOutput({ sessionId, data }: ShellOutput | TerminalOutput) {
  const queued = restoring.get(sessionId);
  if (queued) {
    if (data) queued.push(data);
    return;
  }
  if (data && isOpen(useUIStore.getState(), sessionId)) {
    terminalFor(sessionId).write(Uint8Array.from(atob(data), (c) => c.charCodeAt(0)));
  }
}

// A reloaded window lost what its sessions showed, so each one's latest output is drawn first and live output
// arriving meanwhile waits behind it.
// ponytail: a chunk sent between the status and the tail being read shows twice; offsets would drop it.
const restoring = new Map<string, string[]>();
export function restoreSession(id: string, tail: Promise<SessionTail>) {
  restoring.set(id, []);
  void tail
    .catch(() => null)
    .then((t) => {
      const queued = restoring.get(id) ?? [];
      restoring.delete(id);
      // Drawn at the size it was written for, since a shell pads lines to the width; the view fits it after.
      if (t?.cols && t.rows && isOpen(useUIStore.getState(), id)) terminalFor(id).resize(t.cols, t.rows);
      for (const data of [t?.data ?? "", ...queued]) writeSessionOutput({ sessionId: id, data });
    });
}

export type ShellPod = { namespace: string; name: string; containers: string[] };

// A container that already has a live shell gets its tab selected rather than a second session.
function useStartShell(clusterId: string) {
  return useMutation({
    mutationFn: async (target: Omit<ShellTarget, "clusterId">) => {
      const { shellSessions, openDock, setShellStatus } = useUIStore.getState();
      const open = Object.values(shellSessions).find(
        (s) => s.target.clusterId === clusterId && s.target.namespace === target.namespace && s.target.pod === target.pod && s.target.container === target.container && !sessionEnded(s),
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
  const error = start.error && (
    <span className="my-auto min-w-0 truncate text-xs text-destructive" title={errorText(start.error)}>
      {errorText(start.error)}
    </span>
  );
  if (items.length === 1) {
    return (
      <>
        <Button variant={variant} size={size} className={className} title={`Shell into ${items[0]!.label}`} disabled={start.isPending} onClick={() => start.mutate(items[0]!.target, { onSuccess: onStarted })}>
          {variant === "outline" && <TerminalIcon />}
          Shell
        </Button>
        {error}
      </>
    );
  }
  return (
    <>
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
      {error}
    </>
  );
}

export function ShellView({ session }: { session: ShellStatus }) {
  const start = useStartShell(session.target.clusterId);
  const done = sessionEnded(session);
  return (
    <XtermView id={session.id} done={done} io={ShellService}>
      {done && (
        <Ended
          error={start.error ? errorText(start.error) : session.state === State.StateError ? session.error : undefined}
          reopen={() => start.mutate(session.target, { onSuccess: () => useUIStore.getState().closeShell(session.id) })}
        />
      )}
    </XtermView>
  );
}

// A Terminal is its tab at once, like a Shell, and runs until its shell exits or the tab is closed.
export async function openTerminal(clusterId: string) {
  const status = await TerminalService.Start(clusterId, 80, 24);
  const { terminalSessions, setTerminalStatus, openDock } = useUIStore.getState();
  // Its start event usually came first, and a shell that exited at once has already sent its end.
  if (!terminalSessions[status.id]) setTerminalStatus(status);
  openDock(clusterId, status.id);
}

export function TerminalView({ session }: { session: TerminalStatus }) {
  const reopen = useMutation({ mutationFn: () => openTerminal(session.clusterId), onSuccess: () => useUIStore.getState().closeTerminal(session.id) });
  const done = sessionEnded(session);
  return (
    <XtermView id={session.id} done={done} io={TerminalService}>
      {done && <Ended reopen={() => reopen.mutate()} />}
    </XtermView>
  );
}

function Ended({ error, reopen }: { error?: string; reopen: () => void }) {
  return (
    <div className={cn("absolute inset-x-0 bottom-0 px-3 py-1 text-xs", error ? "bg-destructive text-white" : "bg-muted text-muted-foreground")}>
      {error ? `Failed: ${error}` : "Session ended"}
      <button type="button" className="ml-3 underline underline-offset-2" onClick={reopen}>
        Open again
      </button>
    </div>
  );
}

// XtermView shows a session's terminal, sending it keys and size until it is done.
function XtermView({ id, done, io, children }: { id: string; done: boolean; io: SessionIO; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const doneRef = useRef(done);
  doneRef.current = done;

  useEffect(() => {
    const term = terminalFor(id);
    const fit = new FitAddon();
    term.loadAddon(fit);
    // A terminal opens once; on a later mount its element is moved under the new host.
    if (term.element) ref.current!.appendChild(term.element);
    else term.open(ref.current!);
    const data = term.onData((d) => {
      if (!doneRef.current) sendInput(io, id, d);
    });
    const resize = term.onResize(({ cols, rows }) => void io.Resize(id, cols, rows));
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
  }, [id, io]);

  return (
    <div className="relative min-h-0 flex-1 overflow-hidden py-2 pl-4 pr-1">
      <div ref={ref} className="h-full" />
      {children}
    </div>
  );
}
