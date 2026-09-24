import { Fragment, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDownIcon, CaretDownIcon, CaretRightIcon, MagnifyingGlassIcon, TextAlignLeftIcon, XIcon } from "@phosphor-icons/react";
import { LogService } from "@bindings/internal/bindings";
import { LogSourceKind, type Cluster, type LogLine, type LogStatus } from "@bindings/internal/service";
import { CopyButton } from "@/components/copy-button";
import { statusLabel, StateDot } from "@/components/routes";
import { KindBadge, logKind, TargetPicker, useTargets, type Kind, type Target } from "@/components/targets";
import { OpenShell } from "@/components/terminal";
import { Button } from "@/components/ui/button";
import { ComboboxTrigger } from "@/components/ui/combobox";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupInput } from "@/components/ui/input-group";
import { podsQuery, errorText } from "@/queries";
import { useUIStore } from "@/store";
import { cn, isZeroTime } from "@/lib/utils";

const sourceKind: Record<string, Kind> = { deployment: "deploy", statefulset: "sts", daemonset: "ds", pod: "pod" };
// One stable colour per pod name, so a pod keeps its colour while others join and leave.
const podColors = ["text-sky-600", "text-emerald-600", "text-amber-600", "text-rose-600", "text-violet-600", "text-teal-600", "text-orange-600", "text-fuchsia-600"];
const podColor = (pod: string) => podColors[[...pod].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 0) % podColors.length];
const timeFormat = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3, hour12: false });
const formatTime = (iso: string) => (isZeroTime(iso) ? "" : timeFormat.format(new Date(iso)));
// A pod of a workload is told apart by its random suffix; the full name is in the title.
const podSuffix = (pod: string) => pod.slice(pod.lastIndexOf("-") + 1);
// Structured lines colour by their level field; the key names cover the common loggers, the numbers are pino's.
const pinoLevels: Record<string, string> = { "10": "trace", "20": "debug", "30": "info", "40": "warn", "50": "error", "60": "fatal" };
const level = (l: LogLine) => {
  const v = (l.fields?.level ?? l.fields?.lvl ?? l.fields?.severity ?? "").toLowerCase();
  return pinoLevels[v] ?? v;
};
const levelClass = (v: string) => {
  if (v.startsWith("err") || v === "fatal" || v === "panic" || v === "critical") return "text-red-600 dark:text-red-400";
  if (v.startsWith("warn")) return "text-amber-600 dark:text-amber-400";
  if (v === "debug" || v === "trace") return "text-muted-foreground/70";
  return "text-muted-foreground";
};
// Fields the row already shows elsewhere, or that every container repeats, stay in the expanded view only.
const shownElsewhere = new Set(["level", "lvl", "severity", "msg", "message", "time", "ts", "timestamp", "pid"]);
const inlineFields = (l: LogLine) =>
  Object.entries(l.fields ?? {}).filter(([k, v]) => !shownElsewhere.has(k) && !(k === "hostname" && v === l.pod));

type Columns = { showPod: boolean; showContainer: boolean };
type Segment = { text: string; className?: string; title?: string };
// A row is drawn as plain text with real spaces between its parts, so a selection copies exactly what is on screen.
const segments = (l: LogLine, { showPod, showContainer }: Columns): Segment[] => {
  const out: Segment[] = [{ text: formatTime(l.time), className: "text-muted-foreground/70" }];
  if (showPod) out.push({ text: podSuffix(l.pod), className: podColor(l.pod), title: l.pod });
  if (showContainer) out.push({ text: l.container, className: "text-muted-foreground" });
  if (!l.fields) return [...out, { text: l.text }];
  const lvl = level(l);
  if (lvl) out.push({ text: lvl.slice(0, 5).padEnd(5), className: levelClass(lvl) });
  const msg = l.fields.msg ?? l.fields.message;
  if (msg !== undefined) out.push({ text: msg, className: /^(err|warn|fatal|panic|critical)/.test(lvl) ? levelClass(lvl) : undefined });
  for (const [k, v] of inlineFields(l)) out.push({ text: `${k}=${v}`, className: "text-muted-foreground" });
  return out;
};
const lineText = (l: LogLine, cols: Columns) => segments(l, cols).map((s) => s.text).join("  ");
// A row draws its first shownChars characters, so one huge line cannot stretch the view; Show all and Copy reach the rest.
const shownChars = 2000;
const clip = (segs: Segment[]): { segs: Segment[]; hidden: number } => {
  const total = segs.reduce((n, s) => n + s.text.length, 0);
  if (total <= shownChars) return { segs, hidden: 0 };
  let room = shownChars;
  const out: Segment[] = [];
  for (const s of segs) {
    if (room <= 0) break;
    out.push(s.text.length > room ? { ...s, text: s.text.slice(0, room) } : s);
    room -= s.text.length;
  }
  return { segs: out, hidden: total - shownChars };
};
const compact = new Intl.NumberFormat(undefined, { notation: "compact" });
// Rows are keyed by the line itself, so their measured heights stay with them when the buffer drops its oldest lines.
const lineKeys = new WeakMap<LogLine, number>();
let nextLineKey = 0;
const lineKey = (l: LogLine) => {
  let k = lineKeys.get(l);
  if (k === undefined) lineKeys.set(l, (k = nextLineKey++));
  return k;
};

export const streamFor = (streams: Record<string, LogStatus>, cluster: Cluster) =>
  Object.values(streams).find((st) => st.source.clusterId === cluster.id);

export function useStartLogs(cluster: Cluster) {
  const selectTab = useUIStore((s) => s.selectTab);
  return useMutation({
    mutationFn: async (target: Target & { previous?: boolean }) => {
      await LogService.Start({
        clusterId: cluster.id,
        kind: logKind[target.kind] ?? LogSourceKind.LogSourcePod,
        namespace: target.namespace,
        name: target.name,
        container: target.container,
        previous: target.previous,
      });
    },
    onSuccess: () => selectTab("logs"),
  });
}

export function Logs({ cluster }: { cluster: Cluster }) {
  const pods = useQuery(podsQuery(cluster.id));
  const { groups, error: listError } = useTargets(cluster);
  const stream = useUIStore((s) => streamFor(s.logStreams, cluster));
  const start = useStartLogs(cluster);
  const loggable = groups.filter((g) => g.label !== "Services").map((g) => ({ ...g, items: g.items.filter((t) => logKind[t.kind]) }));
  const kind = stream ? sourceKind[stream.source.kind] ?? "pod" : "pod";
  const current = stream ? loggable.flatMap((g) => g.items).find((t) => t.kind === kind && t.namespace === stream.source.namespace && t.name === stream.source.name) ?? null : null;
  const error = listError ?? start.error;
  // The followed pods, with their containers from the pod list; a pod not listed yet falls back to the stream's union.
  const shellPods = (stream?.pods ?? []).map((name) => ({
    namespace: stream!.source.namespace,
    name,
    containers: pods.data?.find((p) => p.namespace === stream!.source.namespace && p.name === name)?.containers ?? stream!.containers ?? [],
  }));

  const picker = (
    <TargetPicker groups={loggable} value={current} onPick={(t) => start.mutate(t)} placeholder="Search workloads and pods">
      <ComboboxTrigger render={<Button variant="outline" size="sm" className="max-w-96 min-w-48 shrink" />}>
        {stream ? (
          <>
            <StateDot status={stream} />
            <KindBadge kind={kind} />
            <span className="truncate">
              {stream.source.namespace}/{stream.source.name}
            </span>
            {stream.source.kind !== LogSourceKind.LogSourcePod && (
              <span className="text-muted-foreground">
                · {stream.pods?.length ?? 0} {stream.pods?.length === 1 ? "pod" : "pods"}
              </span>
            )}
            {stream.source.container && <span className="text-muted-foreground">· {stream.source.container}</span>}
            {stream.source.previous && <span className="text-muted-foreground">· previous run</span>}
          </>
        ) : (
          "Pick a workload or pod"
        )}
      </ComboboxTrigger>
    </TargetPicker>
  );

  if (!stream) {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex items-center gap-2 px-4 py-2.5">{picker}</div>
        {error && <p className="px-4 pb-2 text-xs text-destructive">{errorText(error)}</p>}
        <Empty className="justify-start border-0 pt-12">
          <EmptyHeader>
            <EmptyTitle>Nothing followed yet</EmptyTitle>
            <EmptyDescription>Pick a workload to follow all of its pods, or a single pod.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      </div>
    );
  }
  return (
    <StreamPanel key={stream.id} stream={stream} picker={picker} shell={<OpenShell cluster={cluster} pods={shellPods} />} error={error} />
  );
}

type ViewState = { hidden: string[]; query: string; regex: boolean };

// StreamPanel owns the view state of one stream; a new stream remounts it clean.
function StreamPanel({ stream, picker, shell, error }: { stream: LogStatus; picker: ReactNode; shell: ReactNode; error: unknown }) {
  const [view, setView] = useState<ViewState>({ hidden: [], query: "", regex: false });
  const patch = (p: Partial<ViewState>) => setView((v) => ({ ...v, ...p }));
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-2 px-4 py-2.5">
        {picker}
        <LogToolbar stream={stream} view={view} patch={patch} />
        {shell}
      </div>
      {(error || stream.error) && <p className="px-4 pb-2 text-xs text-destructive">{errorText(error ?? statusLabel(stream))}</p>}
      {stream.deleted && (
        <p className="px-4 pb-2 text-xs text-amber-700 dark:text-amber-400">
          {stream.source.kind} {stream.source.name} was deleted. Its pods will be followed again if it is recreated.
        </p>
      )}
      <LogView stream={stream} view={view} />
    </div>
  );
}

function LogToolbar({ stream, view, patch }: { stream: LogStatus; view: ViewState; patch: (p: Partial<ViewState>) => void }) {
  const clearLogs = useUIStore((s) => s.clearLogs);
  const wrap = useUIStore((s) => s.logWrap);
  const toggleWrap = useUIStore((s) => s.toggleLogWrap);
  const stop = useMutation({ mutationFn: () => LogService.Stop(stream.id) });
  const containers = stream.containers ?? [];
  return (
    <>
      <Button variant="ghost" size="icon-sm" title="Stop following" disabled={stop.isPending} onClick={() => stop.mutate()}>
        <XIcon />
      </Button>
      {/* One menu rather than a toggle per container, so a pod with several keeps the toolbar on one line. */}
      {containers.length > 1 && (
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant="outline" size="sm" className="shrink-0" />}>
            {view.hidden.length === 0 ? "All containers" : `${containers.length - view.hidden.length} of ${containers.length} containers`}
            <CaretDownIcon />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            {containers.map((c) => (
              <DropdownMenuCheckboxItem
                key={c}
                className="font-mono text-xs"
                checked={!view.hidden.includes(c)}
                onCheckedChange={(on) => patch({ hidden: on ? view.hidden.filter((h) => h !== c) : [...view.hidden, c] })}
              >
                {c}
              </DropdownMenuCheckboxItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      <InputGroup className="h-7 w-auto min-w-40 flex-1">
        <InputGroupInput
          placeholder="Filter lines, or field=value"
          value={view.query}
          onChange={(e) => patch({ query: e.target.value })}
          className="font-mono text-xs placeholder:font-sans placeholder:text-sm"
        />
        <InputGroupAddon>
          <MagnifyingGlassIcon />
        </InputGroupAddon>
        <InputGroupAddon align="inline-end">
          <InputGroupButton
            size="xs"
            variant={view.regex ? "secondary" : "ghost"}
            title="Regular expression"
            aria-pressed={view.regex}
            className="font-mono"
            onClick={() => patch({ regex: !view.regex })}
          >
            .*
          </InputGroupButton>
        </InputGroupAddon>
      </InputGroup>
      <Button variant={wrap ? "secondary" : "ghost"} size="icon-sm" title="Wrap long lines" aria-pressed={wrap} onClick={toggleWrap}>
        <TextAlignLeftIcon />
      </Button>
      <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={() => clearLogs(stream.id)}>
        Clear
      </Button>
    </>
  );
}

// "field=value" matches a structured field; "field=" alone matches its presence; anything else matches the text.
function compile({ query, regex }: ViewState): { matcher: ((l: LogLine) => boolean) | null; error: string | null } {
  const q = query.trim();
  if (!q) return { matcher: null, error: null };
  const eq = /^([\w.-]+)=(.*)$/.exec(q);
  const subject = eq ? (l: LogLine) => l.fields?.[eq[1]!] : (l: LogLine) => l.text;
  const needle = eq ? eq[2]! : q;
  if (eq && !needle) return { matcher: (l) => subject(l) !== undefined, error: null };
  if (!regex) {
    const lower = needle.toLowerCase();
    return { matcher: (l) => subject(l)?.toLowerCase().includes(lower) ?? false, error: null };
  }
  try {
    const re = new RegExp(needle, "i");
    return {
      matcher: (l) => {
        const v = subject(l);
        return v !== undefined && re.test(v);
      },
      error: null,
    };
  } catch (e) {
    return { matcher: null, error: errorText(e) };
  }
}

function LogView({ stream, view }: { stream: LogStatus; view: ViewState }) {
  const buffer = useUIStore((s) => s.logBuffers[stream.id]);
  const showPod = stream.source.kind !== LogSourceKind.LogSourcePod;
  const showContainer = (stream.containers?.length ?? 0) > 1;
  const lines = buffer?.lines ?? [];
  const { matcher, error } = useMemo(() => compile(view), [view.query, view.regex]);
  // The buffer is appended in place, so version is its change signal.
  const version = buffer?.version ?? 0;
  // ponytail: full rescan per batch; incremental filtering if 50k lines with a regex ever lags.
  const filtered = useMemo(() => {
    if (!matcher && view.hidden.length === 0) return lines;
    return lines.filter((l) => !view.hidden.includes(l.container) && (!matcher || matcher(l)));
  }, [lines, version, matcher, view.hidden]);

  return (
    <>
      {error && <p className="px-4 pb-1 text-xs text-destructive">{error}</p>}
      <LogList lines={filtered} total={lines.length} version={version} showPod={showPod} showContainer={showContainer} />
    </>
  );
}

// Rows never wrap, but a message can carry its own newlines, so every row is measured rather than assumed one line.
function LogList({ lines, total, version, showPod, showContainer }: { lines: LogLine[]; total: number; version: number; showPod: boolean; showContainer: boolean }) {
  const parentRef = useRef<HTMLDivElement>(null);
  const [following, setFollowing] = useState(true);
  const wrap = useUIStore((s) => s.logWrap);
  // Lines are found by identity, not position, because the capped buffer drops its oldest ones.
  const seen = useRef<LogLine | undefined>(undefined);
  const [expanded, setExpanded] = useState(() => new Set<LogLine>());
  const [shownInFull, setShownInFull] = useState(() => new Set<LogLine>());
  // Reading a line means staying on it; new batches must not scroll it away.
  const toggle = (set: (f: (s: Set<LogLine>) => Set<LogLine>) => void, line: LogLine) => {
    setFollowing(false);
    set((s) => {
      const next = new Set(s);
      if (next.has(line)) next.delete(line);
      else next.add(line);
      return next;
    });
  };
  // Only the rows in view exist in the DOM, so Select All is remembered and Copy writes every line itself.
  const allSelected = useRef(false);
  const virtualizer = useVirtualizer({
    count: lines.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 20,
    overscan: 30,
    // The buffer is appended in place, so version is part of the key function's identity.
    getItemKey: useCallback((i: number) => lineKey(lines[i]!), [lines, version]),
    // Keeps the top row where it is when the buffer drops its oldest lines.
    anchorTo: "end",
  });

  // Jumping straight to scrollHeight lands exactly at the bottom, so the scroll handler never mistakes it for a scroll up.
  // Rows grow once measured, so the total height is a change signal too.
  const totalSize = virtualizer.getTotalSize();
  useEffect(() => {
    if (!following) return;
    const el = parentRef.current;
    if (el) el.scrollTop = el.scrollHeight;
    seen.current = lines.at(-1);
  }, [lines.length, version, following, totalSize]);
  const behind = seen.current ? lines.length - 1 - lines.lastIndexOf(seen.current) : lines.length;

  return (
    <div className="relative flex min-h-0 flex-1 flex-col border-t">
      <div
        ref={parentRef}
        tabIndex={0}
        className="min-h-0 flex-1 overflow-auto py-1 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset"
        onScroll={(e) => {
          const el = e.currentTarget;
          const atBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - 4;
          if (atBottom !== following) setFollowing(atBottom);
        }}
        onMouseDown={() => (allSelected.current = false)}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === "a") {
            e.preventDefault();
            window.getSelection()?.selectAllChildren(e.currentTarget);
            allSelected.current = true;
          }
        }}
        onCopy={(e) => {
          if (!allSelected.current) return;
          e.preventDefault();
          e.clipboardData.setData("text/plain", lines.map((l) => lineText(l, { showPod, showContainer })).join("\n"));
        }}
      >
        <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((item) => {
            const line = lines[item.index]!;
            const open = expanded.has(line);
            const full = shownInFull.has(line);
            const all = segments(line, { showPod, showContainer });
            const { segs, hidden } = full ? { segs: all, hidden: 0 } : clip(all);
            const fields = line.fields ? Object.entries(line.fields) : [];
            const width = Math.max(0, ...fields.map(([k]) => k.length));
            return (
              <div
                key={item.key}
                ref={virtualizer.measureElement}
                data-index={item.index}
                className={cn("absolute left-0", wrap ? "w-full" : "w-max min-w-full", open && "bg-accent")}
                style={{ transform: `translateY(${item.start}px)` }}
              >
                <div
                  className={cn("group relative min-h-5 px-4 leading-5 hover:bg-muted/50", wrap ? "whitespace-pre-wrap wrap-anywhere" : "whitespace-pre")}
                >
                  {/* Only the caret opens the fields, so clicking and dragging over lines is left to text selection. */}
                  {fields.length > 0 && (
                    <button
                      type="button"
                      title={open ? "Hide fields" : "Show fields"}
                      aria-expanded={open}
                      className={cn(
                        "absolute top-0 left-0.5 flex h-5 w-3.5 items-center justify-center text-muted-foreground select-none hover:text-foreground focus-visible:opacity-100",
                        !open && "opacity-0 group-hover:opacity-100",
                      )}
                      onClick={() => toggle(setExpanded, line)}
                    >
                      <CaretRightIcon className={cn("size-3 transition-transform", open && "rotate-90")} />
                    </button>
                  )}
                  {segs.map((seg, i) => (
                    <Fragment key={i}>
                      {i > 0 && "  "}
                      <span className={seg.className} title={seg.title}>
                        {seg.text}
                      </span>
                    </Fragment>
                  ))}
                  {(hidden > 0 || full) && (
                    <span className="text-muted-foreground select-none">
                      {hidden > 0 && "…"}
                      {"  "}
                      {line.truncated ? (
                        <span className="text-amber-700 dark:text-amber-400" title="The rest of this line was not received.">
                          truncated
                        </span>
                      ) : (
                        hidden > 0 && `+${compact.format(hidden)} characters`
                      )}
                      {"  "}
                      <button type="button" className="hover:text-foreground hover:underline" onClick={() => toggle(setShownInFull, line)}>
                        {full ? "Show less" : "Show all"}
                      </button>
                      <CopyButton text={line.text} title="Copy the whole line" className="ml-1 size-5 align-top opacity-100" />
                    </span>
                  )}
                </div>
                {open && (
                  <div className="border-b px-4 pt-0.5 pb-1.5 leading-5 whitespace-pre text-muted-foreground">
                    {fields.map(([k, v]) => (
                      <div key={k}>
                        <span className="text-foreground">{k.padEnd(width)}</span>
                        {"  "}
                        {v}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
      <div className="pointer-events-none absolute right-3 bottom-3 flex items-center gap-2 font-sans text-xs">
        {lines.length !== total && (
          <span className="rounded-full border bg-background px-2.5 py-1 text-muted-foreground">
            {lines.length} of {total} lines
          </span>
        )}
        {!following && (
          <Button size="sm" className="pointer-events-auto rounded-full shadow-md" onClick={() => setFollowing(true)}>
            <ArrowDownIcon />
            {behind > 0 ? `${behind} new lines` : "Follow"}
          </Button>
        )}
      </div>
    </div>
  );
}
