import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDownIcon, MagnifyingGlassIcon, XIcon } from "@phosphor-icons/react";
import { LogService } from "@bindings/internal/bindings";
import { LogSourceKind, type Cluster, type LogLine, type LogStatus } from "@bindings/internal/service";
import { statusLabel, StateDot } from "@/components/routes";
import { KindBadge, logKind, TargetPicker, useTargets, type Kind, type Target } from "@/components/targets";
import { OpenShell } from "@/components/terminal";
import { Button } from "@/components/ui/button";
import { ComboboxTrigger } from "@/components/ui/combobox";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupInput } from "@/components/ui/input-group";
import { Toggle } from "@/components/ui/toggle";
import { podsQuery } from "@/queries";
import { useUIStore } from "@/store";
import { cn, isZeroTime } from "@/lib/utils";

const sourceKind: Record<string, Kind> = { deployment: "deploy", statefulset: "sts", daemonset: "ds", pod: "pod" };
// One stable colour per pod name, so a pod keeps its colour while others join and leave.
const podColors = ["text-sky-600", "text-emerald-600", "text-amber-600", "text-rose-600", "text-violet-600", "text-teal-600", "text-orange-600", "text-fuchsia-600"];
const podColor = (pod: string) => podColors[[...pod].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 0) % podColors.length];
const timeFormat = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3, hour12: false });
const formatTime = (iso: string) => (isZeroTime(iso) ? "" : timeFormat.format(new Date(iso)));
const lineText = (l: LogLine, showPod: boolean) => [formatTime(l.time), showPod && l.pod, l.container, l.text].filter((s) => s !== false).join("  ");
// Structured lines colour by their level field; the key names cover the common loggers.
const level = (l: LogLine) => (l.fields?.level ?? l.fields?.lvl ?? l.fields?.severity ?? "").toLowerCase();
const levelClass = (l: LogLine) => {
  const v = level(l);
  if (v.startsWith("err") || v === "fatal" || v === "panic" || v === "critical") return "text-red-600 dark:text-red-400";
  if (v.startsWith("warn")) return "text-amber-600 dark:text-amber-400";
  return "";
};

export const streamFor = (streams: Record<string, LogStatus>, cluster: Cluster) =>
  Object.values(streams).find((st) => st.source.clusterId === cluster.id);

// A cluster follows one source at a time, so starting a new one replaces the old.
export function useStartLogs(cluster: Cluster) {
  const selectTab = useUIStore((s) => s.selectTab);
  return useMutation({
    mutationFn: async (target: Target & { previous?: boolean }) => {
      const current = streamFor(useUIStore.getState().logStreams, cluster);
      if (current) await LogService.Stop(current.id);
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
  const loggable = groups.filter((g) => g.label !== "Services");
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
      <ComboboxTrigger render={<Button variant="outline" size="sm" className="max-w-96" />}>
        {stream ? (
          <>
            <StateDot status={stream} />
            <KindBadge kind={kind} />
            <span className="truncate">
              {stream.source.namespace}/{stream.source.name}
            </span>
            {stream.source.kind !== LogSourceKind.LogSourcePod && <span className="text-muted-foreground">· {stream.pods?.length ?? 0} pods</span>}
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
        {error && <p className="px-4 pb-2 text-xs text-destructive">{String(error)}</p>}
        <Empty className="border-0">
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
      {(error || stream.error) && <p className="px-4 pb-2 text-xs text-destructive">{String(error ?? statusLabel(stream))}</p>}
      <LogView stream={stream} view={view} />
    </div>
  );
}

function LogToolbar({ stream, view, patch }: { stream: LogStatus; view: ViewState; patch: (p: Partial<ViewState>) => void }) {
  const clearLogs = useUIStore((s) => s.clearLogs);
  const stop = useMutation({ mutationFn: () => LogService.Stop(stream.id) });
  const containers = stream.containers ?? [];
  return (
    <>
      <Button variant="ghost" size="icon-sm" title="Stop following" disabled={stop.isPending} onClick={() => stop.mutate()}>
        <XIcon />
      </Button>
      {containers.length > 1 &&
        containers.map((c) => {
          const hidden = view.hidden.includes(c);
          return (
            <Toggle
              key={c}
              variant="outline"
              size="sm"
              pressed={!hidden}
              onPressedChange={(on) => patch({ hidden: on ? view.hidden.filter((h) => h !== c) : [...view.hidden, c] })}
              title={hidden ? `Show ${c}` : `Hide ${c}`}
              className={cn("font-mono text-xs", hidden && "text-muted-foreground line-through")}
            >
              {c}
            </Toggle>
          );
        })}
      <InputGroup className="h-7 w-auto min-w-48 flex-1">
        <InputGroupInput
          placeholder="Filter lines, or field=value"
          value={view.query}
          onChange={(e) => patch({ query: e.target.value })}
          className="font-mono text-xs"
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
    return { matcher: null, error: String(e) };
  }
}

function LogView({ stream, view }: { stream: LogStatus; view: ViewState }) {
  const buffer = useUIStore((s) => s.logBuffers[stream.id]);
  const showPod = stream.source.kind !== LogSourceKind.LogSourcePod;
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
      <LogList lines={filtered} total={lines.length} version={version} showPod={showPod} />
    </>
  );
}

// Rows never wrap, so a collapsed row is one fixed line; an expanded row is measured.
function LogList({ lines, total, version, showPod }: { lines: LogLine[]; total: number; version: number; showPod: boolean }) {
  const parentRef = useRef<HTMLDivElement>(null);
  const [following, setFollowing] = useState(true);
  const seen = useRef(0);
  const [expanded, setExpanded] = useState(() => new Set<LogLine>());
  // Only the rows in view exist in the DOM, so Select All is remembered and Copy writes every line itself.
  const allSelected = useRef(false);
  const virtualizer = useVirtualizer({
    count: lines.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 20,
    overscan: 30,
  });

  // Jumping straight to scrollHeight lands exactly at the bottom, so the scroll handler never mistakes it for a scroll up.
  useEffect(() => {
    if (!following) return;
    const el = parentRef.current;
    if (el) el.scrollTop = el.scrollHeight;
    seen.current = lines.length;
  }, [lines.length, version, following]);
  const behind = Math.max(0, lines.length - seen.current);

  return (
    <div className="relative mx-4 mb-4 flex min-h-0 flex-1 flex-col">
      <div
        ref={parentRef}
        tabIndex={0}
        className="min-h-0 flex-1 overflow-auto rounded-md border bg-muted/30 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
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
          e.clipboardData.setData("text/plain", lines.map((l) => lineText(l, showPod)).join("\n"));
        }}
      >
        <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((item) => {
            const line = lines[item.index]!;
            const open = expanded.has(line);
            const fields = line.fields ? Object.entries(line.fields) : [];
            return (
              <div
                key={item.key}
                ref={virtualizer.measureElement}
                data-index={item.index}
                className={cn("absolute left-0 w-max min-w-full", open && "bg-accent")}
                style={{ transform: `translateY(${item.start}px)` }}
              >
                <div
                  className={cn("flex h-5 gap-2 px-2 leading-5 whitespace-pre", fields.length > 0 && "cursor-pointer", levelClass(line))}
                  onClick={() => {
                    if (fields.length === 0) return;
                    // Reading a line means staying on it; new batches must not scroll it away.
                    setFollowing(false);
                    setExpanded((s) => {
                      const next = new Set(s);
                      if (next.has(line)) next.delete(line);
                      else next.add(line);
                      return next;
                    });
                  }}
                >
                  <span className="text-muted-foreground">{formatTime(line.time)}</span>
                  {showPod && <span className={podColor(line.pod)}>{line.pod}</span>}
                  <span className="text-muted-foreground">{line.container}</span>
                  <span>{line.text}</span>
                </div>
                {open && (
                  <div className="grid grid-cols-[max-content_1fr] gap-x-4 border-b px-2 pt-0.5 pb-1.5 leading-5 whitespace-pre text-muted-foreground">
                    {fields.map(([k, v]) => (
                      <div key={k} className="contents">
                        <span className="text-foreground">{k}</span>
                        <span>{v}</span>
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
