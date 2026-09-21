import type { ComponentProps, ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowsClockwiseIcon } from "@phosphor-icons/react";
import type { Cluster, ContainerDiagnosis, ContainerState, KubeEvent } from "@bindings/internal/service";
import { useStartLogs } from "@/components/logs";
import type { Target } from "@/components/targets";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { podQuery } from "@/queries";
import { cn, isZeroTime } from "@/lib/utils";

// States that are normal passage or a deliberate choice rather than a fault; a complete rollout has nothing to say on a row.
const calmReasons = new Set(["ContainerCreating", "PodInitializing", "Pending", "Terminating", "Completed", "progressing", "suspended"]);

// Shaped like KindBadge so the two sit on one row as one system; red only when the reason is a fault.
export function ReasonBadge({ reason, className, ...props }: { reason?: string } & ComponentProps<typeof Badge>) {
  if (!reason || reason === "complete") return null;
  return (
    <Badge
      variant={calmReasons.has(reason) ? "secondary" : "destructive"}
      className={cn("h-[18px] rounded px-1.5 font-mono text-[11px]", className)}
      {...props}
    >
      {reason}
    </Badge>
  );
}

const units: [Intl.RelativeTimeFormatUnit, number][] = [["day", 86400], ["hour", 3600], ["minute", 60], ["second", 1]];
const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
// ago renders "3 minutes ago" for an ISO stamp, or nothing for Go's zero time.
export function ago(iso: string) {
  if (isZeroTime(iso)) return "";
  const seconds = (new Date(iso).getTime() - Date.now()) / 1000;
  const [unit, size] = units.find(([, s]) => Math.abs(seconds) >= s) ?? ["second", 1];
  return relative.format(Math.round(seconds / size), unit);
}

const stateLabel = (st: ContainerState) => {
  if (!st.status) return "not started";
  const parts = [st.status, st.reason];
  if (st.status === "running") parts.push(`since ${ago(st.startedAt)}`);
  if (st.status === "terminated") parts.push(`exit ${st.exitCode}`, `ended ${ago(st.finishedAt)}`);
  return parts.filter(Boolean).join(" · ");
};

export const restartsLabel = (n: number) => `${n} restart${n === 1 ? "" : "s"}`;

const resources = (r?: { [_ in string]?: string } | null) =>
  Object.entries(r ?? {})
    .map(([k, v]) => `${k} ${v}`)
    .join(", ") || "—";

export function PodDetail({ cluster, target, onClose }: { cluster: Cluster; target: Target; onClose: () => void }) {
  const pod = useQuery(podQuery(cluster.id, target.namespace, target.name));
  const startLogs = useStartLogs(cluster);
  const d = pod.data;
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="flex max-h-[calc(100vh-4rem)] flex-col sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 pr-8">
            <span className="truncate">
              {target.namespace}/{target.name}
            </span>
            {d && <ReasonBadge reason={d.reason} />}
            <Button variant="ghost" size="icon-sm" title="Refresh" disabled={pod.isFetching} onClick={() => pod.refetch()}>
              <ArrowsClockwiseIcon className={cn(pod.isFetching && "animate-spin")} />
            </Button>
          </DialogTitle>
          <DialogDescription>
            {d ? [d.phase, d.node && `on ${d.node}`, d.events?.[0] && `last event ${ago(d.events[0].time)}`].filter(Boolean).join(" · ") : "Loading…"}
          </DialogDescription>
        </DialogHeader>
        {pod.error && <p className="text-xs text-destructive">{String(pod.error)}</p>}
        {d && (
          <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-auto">
            <Section title="Containers">
              {d.containers?.map((c) => (
                <Container
                  key={c.name}
                  container={c}
                  onPrevious={() => startLogs.mutate({ ...target, container: c.name, previous: true }, { onSuccess: onClose })}
                  pending={startLogs.isPending}
                />
              ))}
            </Section>
            <Section title="Conditions">
              <dl className="grid grid-cols-[max-content_max-content_1fr] gap-x-4 gap-y-0.5 text-xs">
                {d.conditions?.map((c) => (
                  <div key={c.type} className="contents">
                    <dt className="font-medium">{c.type}</dt>
                    <dd className={cn("font-mono", c.status !== "True" && "text-destructive")}>{c.status}</dd>
                    <dd className="truncate text-muted-foreground" title={c.message}>
                      {[c.reason, c.message].filter(Boolean).join(": ")}
                    </dd>
                  </div>
                ))}
              </dl>
            </Section>
            <Section title="Events">
              {d.eventsError ? <p className="text-xs text-destructive">{d.eventsError}</p> : <Events events={d.events ?? []} />}
            </Section>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-1.5">
      <h4 className="text-xs font-medium text-muted-foreground">{title}</h4>
      {children}
    </section>
  );
}

function Container({ container: c, onPrevious, pending }: { container: ContainerDiagnosis; onPrevious: () => void; pending: boolean }) {
  const oom = c.state.reason === "OOMKilled" || c.lastState?.reason === "OOMKilled";
  return (
    <div className="rounded-md border px-3 py-2 text-xs">
      <div className="flex items-center gap-2">
        <span className="font-mono font-medium">{c.name}</span>
        {c.init && <Badge variant="outline">init</Badge>}
        <span className="min-w-0 truncate text-muted-foreground" title={c.image}>
          {c.image}
        </span>
        <span className="ml-auto shrink-0 text-muted-foreground">
          {restartsLabel(c.restarts)}
          {c.restarts > 0 && c.lastState && `, last ${ago(c.lastState.finishedAt)}`}
        </span>
      </div>
      <dl className="mt-1.5 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5">
        <dt className="text-muted-foreground">State</dt>
        <dd>
          {stateLabel(c.state)}
          {c.state.message && <span className="text-muted-foreground"> — {c.state.message}</span>}
        </dd>
        {c.lastState && (
          <>
            <dt className="text-muted-foreground">Previous run</dt>
            <dd className="flex items-center gap-2">
              <span>{stateLabel(c.lastState)}</span>
              <Button variant="outline" size="xs" className="h-5 text-xs" disabled={pending} onClick={onPrevious}>
                Previous logs
              </Button>
            </dd>
          </>
        )}
        <dt className="text-muted-foreground">Resources</dt>
        <dd>
          requests {resources(c.requests)} · limits {resources(c.limits)}
          {oom && <span className="text-destructive">{c.limits?.memory ? " · killed at the memory limit" : " · killed by the node, no memory limit set"}</span>}
        </dd>
      </dl>
    </div>
  );
}

export function Events({ events }: { events: KubeEvent[] }) {
  if (events.length === 0) return <p className="text-xs text-muted-foreground">No recent events.</p>;
  return (
    <ul className="flex flex-col gap-1 text-xs">
      {events.map((e, i) => (
        <li key={i} className="grid grid-cols-[max-content_max-content_1fr] gap-x-3">
          <span className="text-muted-foreground" title={new Date(e.time).toLocaleString()}>
            {ago(e.time)}
          </span>
          <span className={cn("font-mono", e.type === "Warning" && "text-destructive")}>
            {e.reason}
            {e.count > 1 && <span className="text-muted-foreground"> ×{e.count}</span>}
          </span>
          <span className="whitespace-pre-wrap">{e.message}</span>
        </li>
      ))}
    </ul>
  );
}
