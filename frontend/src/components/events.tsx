import { useCallback, useEffect, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { EventService } from "@bindings/internal/bindings";
import { State, type Cluster, type EventStatus, type KubeEvent } from "@bindings/internal/service";
import { statusLabel, StateDot } from "@/components/routes";
import { KindBadge, type Kind } from "@/components/targets";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Toggle } from "@/components/ui/toggle";
import { useUIStore } from "@/store";
import { cn, isZeroTime } from "@/lib/utils";

// Kinds the Overview can open a detail for; anything else is shown by name only.
const targetKind: Record<string, Kind> = { Pod: "pod", Deployment: "deploy", StatefulSet: "sts", DaemonSet: "ds", CronJob: "cron", ConfigMap: "cm", Secret: "secret" };
// kubectl's short names for the kinds that fill an event stream; an unlisted kind is shown lowercased.
const shortKind: Record<string, string> = {
  ...targetKind,
  Service: "svc",
  ReplicaSet: "rs",
  Job: "job",
  Node: "node",
  Ingress: "ing",
  HorizontalPodAutoscaler: "hpa",
  PersistentVolumeClaim: "pvc",
  Endpoints: "ep",
  EndpointSlice: "eps",
  Namespace: "ns",
};
// Events are stamped to the second.
const timeFormat = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
const formatTime = (iso: string) => (isZeroTime(iso) ? "" : timeFormat.format(new Date(iso)));

export const eventStreamFor = (streams: Record<string, EventStatus>, cluster: Cluster) =>
  Object.values(streams).find((st) => st.clusterId === cluster.id);

// A new watch replaces the cluster's old one, so a quick remount must not let the old mount's Stop land after the new Start,
// nor two Starts race; every Start and Stop goes through this one queue in call order.
let watchQueue: Promise<unknown> = Promise.resolve();
const queued = <T,>(run: () => Promise<T>) => {
  const next = watchQueue.then(run);
  watchQueue = next.catch(() => {});
  return next;
};
const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

// The watch runs while the tab is open: it starts on mount and stops on unmount.
export function ClusterEvents({ cluster }: { cluster: Cluster }) {
  const [error, setError] = useState<unknown>(null);
  const [warningsOnly, setWarningsOnly] = useState(false);
  const stream = useUIStore((s) => eventStreamFor(s.eventStreams, cluster));
  const buffer = useUIStore((s) => (stream ? s.eventBuffers[stream.id] : undefined));
  useEffect(() => {
    const started = queued(() => EventService.Start(cluster.id)).catch((e) => {
      setError(e);
      return null;
    });
    return () => {
      void queued(async () => {
        const st = await started;
        if (st) await EventService.Stop(st.id);
      });
    };
  }, [cluster.id]);

  const events = buffer?.events ?? [];
  const warnings = events.filter((e) => e.type === "Warning");
  const shown = warningsOnly ? warnings : events;
  const problem = error ?? (stream?.error && statusLabel(stream));
  const opening = !error && stream?.state !== State.StateConnected && stream?.state !== State.StateError;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-2 px-4 py-2.5">
        <StateDot status={stream} />
        <span className="text-sm text-muted-foreground">
          {plural(events.length, "event")} · {plural(warnings.length, "warning")}
        </span>
        <Toggle variant="outline" size="sm" pressed={warningsOnly} onPressedChange={setWarningsOnly} className={cn(warningsOnly && "text-destructive")}>
          Warnings only
        </Toggle>
      </div>
      {problem && <p className="px-4 pb-2 text-xs text-destructive">{String(problem)}</p>}
      {shown.length > 0 ? (
        <EventList cluster={cluster} events={shown} />
      ) : (
        <Empty className="justify-start border-0 pt-12">
          <EmptyHeader>
            <EmptyTitle>{opening ? "Opening the event watch" : warningsOnly ? "No warnings" : "No events"}</EmptyTitle>
            <EmptyDescription>
              {opening
                ? "Recent events arrive as soon as the cluster answers."
                : warningsOnly
                  ? "None of the recent events in this scope is a Warning."
                  : "Events appear here as they happen in this scope."}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
    </div>
  );
}

// Rows carry the whole message, so they are measured rather than fixed.
function EventList({ cluster, events }: { cluster: Cluster; events: KubeEvent[] }) {
  const parentRef = useRef<HTMLDivElement>(null);
  const requestInspect = useUIStore((s) => s.requestInspect);
  const selectTab = useUIStore((s) => s.selectTab);
  const virtualizer = useVirtualizer({
    count: events.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 28,
    overscan: 20,
    // New events land on top, so a size measured by index would belong to another row after the next batch.
    getItemKey: useCallback((i: number) => events[i]!.id, [events]),
  });
  return (
    <div ref={parentRef} className="min-h-0 flex-1 overflow-auto border-t py-1 text-xs">
      <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
        {virtualizer.getVirtualItems().map((item) => {
          const e = events[item.index]!;
          const kind = targetKind[e.kind];
          const warning = e.type === "Warning";
          const object = `${e.namespace}/${e.name}`;
          return (
            <div
              key={e.id}
              ref={virtualizer.measureElement}
              data-index={item.index}
              className="absolute left-0 grid w-full grid-cols-[max-content_48px_minmax(160px,280px)_max-content_minmax(0,1fr)] items-start gap-3 px-4 py-1.5 leading-4 hover:bg-muted/50"
              style={{ transform: `translateY(${item.start}px)` }}
            >
              <span className="font-mono text-muted-foreground" title={new Date(e.time).toLocaleString()}>
                {formatTime(e.time)}
              </span>
              <KindBadge kind={shortKind[e.kind] ?? e.kind.toLowerCase()} title={e.kind} />
              {kind ? (
                <button
                  type="button"
                  className="truncate text-left hover:underline"
                  title={`Open ${object} in the Overview`}
                  onClick={() => {
                    requestInspect({ clusterId: cluster.id, kind, namespace: e.namespace, name: e.name });
                    selectTab("overview");
                  }}
                >
                  {object}
                </button>
              ) : (
                <span className="truncate" title={object}>
                  {object}
                </span>
              )}
              <span className={cn("font-mono", warning && "text-destructive")}>
                {e.reason}
                {e.count > 1 && <span className="text-muted-foreground"> ×{e.count}</span>}
              </span>
              <span className="whitespace-pre-wrap">{e.message}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
