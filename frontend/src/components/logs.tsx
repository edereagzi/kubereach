import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { EraserIcon, PauseIcon, PlayIcon } from "@phosphor-icons/react";
import { LogService } from "@bindings/internal/bindings";
import { LogSourceKind, type Cluster, type LogLine, type LogSource } from "@bindings/internal/service";
import { statusLabel, StateDot } from "@/components/routes";
import { OpenShell } from "@/components/terminal";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Toggle } from "@/components/ui/toggle";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { podsQuery, workloadsQuery } from "@/queries";
import { useUIStore } from "@/store";

type Source = Omit<LogSource, "clusterId">;
const sourceKey = (s: Source) => `${s.kind}:${s.namespace}/${s.name}`;
const kindLabel: Partial<Record<LogSourceKind, string>> = { deployment: "Deployment", statefulset: "StatefulSet", daemonset: "DaemonSet" };
// One stable colour per pod name, so a pod keeps its colour while others join and leave.
const podColors = ["text-sky-600", "text-emerald-600", "text-amber-600", "text-rose-600", "text-violet-600", "text-teal-600", "text-orange-600", "text-fuchsia-600"];
const podColor = (pod: string) => podColors[[...pod].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 0) % podColors.length];
const timeFormat = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3, hour12: false });
const formatTime = (iso: string) => (iso.startsWith("0001") ? "" : timeFormat.format(new Date(iso)));
const lineText = (l: LogLine, showPod: boolean) => [formatTime(l.time), showPod && l.pod, l.container, l.text].filter((s) => s !== false).join("  ");

export function Logs({ cluster }: { cluster: Cluster }) {
  const pods = useQuery(podsQuery(cluster.id));
  const workloads = useQuery(workloadsQuery(cluster.id));
  const stream = useUIStore((s) => Object.values(s.logStreams).find((st) => st.source.clusterId === cluster.id));
  const start = useMutation({
    mutationFn: async (source: Source) => {
      if (stream) await LogService.Stop(stream.id);
      await LogService.Start({ clusterId: cluster.id, ...source });
    },
  });
  const workloadItems = (workloads.data ?? []).map((w) => ({ source: w, value: sourceKey(w), label: `${kindLabel[w.kind]} ${w.namespace}/${w.name}` }));
  const podItems = (pods.data ?? []).map((p) => {
    const source = { kind: LogSourceKind.LogSourcePod, namespace: p.namespace, name: p.name };
    return { source, value: sourceKey(source), label: `${p.namespace}/${p.name}` };
  });
  const items = [...workloadItems, ...podItems];
  const error = pods.error ?? workloads.error ?? start.error;
  // The followed pods, with their containers from the pod list; a pod not listed yet falls back to the stream's union.
  const shellPods = (stream?.pods ?? []).map((name) => ({
    namespace: stream!.source.namespace,
    name,
    containers: pods.data?.find((p) => p.namespace === stream!.source.namespace && p.name === name)?.containers ?? stream!.containers ?? [],
  }));

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 p-4">
      <div className="flex items-center gap-2">
        <Select
          value={stream ? sourceKey(stream.source) : ""}
          items={items}
          onValueChange={(key) => {
            const item = items.find((i) => i.value === key);
            if (item) start.mutate(item.source);
          }}
        >
          <SelectTrigger className="w-96">
            <SelectValue placeholder="Workload or pod" />
          </SelectTrigger>
          <SelectContent>
            {([
              ["Workloads", workloadItems],
              ["Pods", podItems],
            ] as const).map(([label, group]) => (
              <SelectGroup key={label}>
                <SelectLabel>{label}</SelectLabel>
                {group.map((i) => (
                  <SelectItem key={i.value} value={i.value}>
                    {i.label}
                  </SelectItem>
                ))}
              </SelectGroup>
            ))}
          </SelectContent>
        </Select>
        {stream && <StateDot status={stream} />}
        {stream && <OpenShell cluster={cluster} pods={shellPods} />}
        {(error || stream?.error) && (
          <span className="truncate text-xs text-destructive">{String(error ?? statusLabel(stream))}</span>
        )}
      </div>
      {stream ? (
        <LogView key={stream.id} streamId={stream.id} containers={stream.containers ?? []} pods={stream.pods ?? []} showPod={stream.source.kind !== LogSourceKind.LogSourcePod} />
      ) : (
        <Empty className="border-0">
          <EmptyHeader>
            <EmptyTitle>No source selected</EmptyTitle>
            <EmptyDescription>Pick a workload to follow all of its pods, or a single pod.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
    </div>
  );
}

function LogView({ streamId, containers, pods, showPod }: { streamId: string; containers: string[]; pods: string[]; showPod: boolean }) {
  const buffer = useUIStore((s) => s.logBuffers[streamId]);
  const clearLogs = useUIStore((s) => s.clearLogs);
  // Hidden rather than visible, so a container that joins later is shown by default.
  const [hidden, setHidden] = useState<string[]>([]);
  const visible = containers.filter((c) => !hidden.includes(c));
  const [needle, setNeedle] = useState("");
  const [field, setField] = useState("");
  const [useRegex, setUseRegex] = useState(false);
  // Pausing freezes a snapshot; lines keep arriving in the store and appear on resume.
  const [paused, setPaused] = useState<LogLine[] | null>(null);
  const live = buffer?.lines ?? [];
  const lines = paused ?? live;

  const { matcher, regexError } = useMemo(() => {
    if (!needle && !field) return { matcher: null, regexError: null };
    const subject = field ? (l: LogLine) => l.fields?.[field] : (l: LogLine) => l.text;
    if (!needle) return { matcher: (l: LogLine) => subject(l) !== undefined, regexError: null };
    if (!useRegex) {
      const lower = needle.toLowerCase();
      return { matcher: (l: LogLine) => subject(l)?.toLowerCase().includes(lower) ?? false, regexError: null };
    }
    try {
      const re = new RegExp(needle, "i");
      return {
        matcher: (l: LogLine) => {
          const v = subject(l);
          return v !== undefined && re.test(v);
        },
        regexError: null,
      };
    } catch (e) {
      return { matcher: null, regexError: String(e) };
    }
  }, [needle, field, useRegex]);

  // The buffer is appended in place, so version is its change signal.
  const version = buffer?.version ?? 0;
  // ponytail: full rescan per batch; incremental filtering if 50k lines with a regex ever lags.
  const filtered = useMemo(() => {
    if (!matcher && hidden.length === 0) return lines;
    return lines.filter((l) => !hidden.includes(l.container) && (!matcher || matcher(l)));
  }, [lines, version, matcher, hidden]);

  return (
    <>
      <div className="flex items-center gap-2">
        <ToggleGroup multiple value={visible} onValueChange={(v) => setHidden(containers.filter((c) => !(v as string[]).includes(c)))} variant="outline" size="sm">
          {containers.map((c) => (
            <ToggleGroupItem key={c} value={c} title={hidden.includes(c) ? `Show ${c}` : `Hide ${c}`}>
              {c}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        <Input className="w-28" placeholder="Field" title="JSON field to filter on" value={field} onChange={(e) => setField(e.target.value)} />
        <Input
          className="w-72"
          placeholder={useRegex ? "Regular expression" : "Filter"}
          value={needle}
          onChange={(e) => setNeedle(e.target.value)}
          aria-invalid={!!regexError}
        />
        <Toggle variant="outline" size="sm" pressed={useRegex} onPressedChange={setUseRegex} title="Regular expression">
          .*
        </Toggle>
        <Toggle variant="outline" size="sm" pressed={!!paused} onPressedChange={(p) => setPaused(p ? live.slice() : null)} title={paused ? "Resume" : "Pause"}>
          {paused ? <PlayIcon /> : <PauseIcon />}
        </Toggle>
        <Button variant="outline" size="sm" title="Clear" onClick={() => { if (paused) setPaused([]); clearLogs(streamId); }}>
          <EraserIcon />
        </Button>
        <span className="ml-auto text-xs text-muted-foreground">
          {showPod && `${pods.length} pods, `}
          {filtered.length === lines.length ? lines.length : `${filtered.length} of ${lines.length}`} lines
        </span>
      </div>
      {regexError && <p className="text-xs text-destructive">{regexError}</p>}
      <LogList lines={filtered} version={version} follow={!paused} showPod={showPod} />
    </>
  );
}

// Rows are one fixed line each and never wrap, so scrolling to the end is exact and the list stays cheap at 50k lines.
function LogList({ lines, version, follow, showPod }: { lines: LogLine[]; version: number; follow: boolean; showPod: boolean }) {
  const parentRef = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  // Only the rows in view exist in the DOM, so Select All is remembered and Copy writes every line itself.
  const allSelected = useRef(false);
  const virtualizer = useVirtualizer({
    count: lines.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 20,
    overscan: 30,
  });

  useEffect(() => {
    if (follow && atBottom.current && lines.length > 0) virtualizer.scrollToIndex(lines.length - 1, { align: "end" });
  }, [lines.length, version, follow, virtualizer]);

  return (
    <div
      ref={parentRef}
      tabIndex={0}
      className="min-h-0 flex-1 overflow-auto rounded-md border bg-muted/30 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      onScroll={(e) => {
        const el = e.currentTarget;
        atBottom.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 4;
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
          return (
            <div
              key={item.key}
              className="absolute left-0 flex h-5 w-max min-w-full gap-2 px-2 leading-5 whitespace-pre"
              style={{ transform: `translateY(${item.start}px)` }}
            >
              <span className="text-muted-foreground">{formatTime(line.time)}</span>
              {showPod && <span className={podColor(line.pod)}>{line.pod}</span>}
              <span className="text-muted-foreground">{line.container}</span>
              <span title={line.fields ? Object.entries(line.fields).map(([k, v]) => `${k}: ${v}`).join("\n") : undefined}>{line.text}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
