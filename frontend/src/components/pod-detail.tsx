import type { ComponentProps, ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowsClockwiseIcon } from "@phosphor-icons/react";
import type { Cluster, ContainerDiagnosis, ContainerState, KubeEvent, ResourceUsage } from "@bindings/internal/service";
import { useStartLogs } from "@/components/logs";
import { portsLabel, type Target } from "@/components/targets";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DetailTabs } from "@/components/yaml-view";
import { podMetricsQuery, podQuery, podUsageKey } from "@/queries";
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

export const cpuLabel = (m: number) => `${m}m`;
export const memoryLabel = (b: number) => (b >= 1 << 30 ? `${(b / (1 << 30)).toFixed(1)}Gi` : `${Math.round(b / (1 << 20))}Mi`);

// A pod this close to a limit is about to be throttled or killed, so it reads as a fault on a row and counts as a problem.
const pressureThreshold = 90;

// usagePressure is the resource nearest its limit once it is close enough to matter, as "mem 94%"; nothing while there is room, or without a limit.
export function usagePressure(usage?: ResourceUsage, limits?: ResourceUsage) {
  if (!usage) return undefined;
  const worst = [
    { name: "cpu", pct: limits?.cpu ? Math.round((usage.cpu / limits.cpu) * 100) : 0 },
    { name: "mem", pct: limits?.memory ? Math.round((usage.memory / limits.memory) * 100) : 0 },
  ].reduce((a, b) => (b.pct > a.pct ? b : a));
  return worst.pct >= pressureThreshold ? `${worst.name} ${worst.pct}%` : undefined;
}

// UsageMeters answers "is it starving": one short bar and a percentage per resource, red once a limit is nearly reached.
// A pod without a limit has no ratio and shows the bare usage instead; the tooltip carries the absolute numbers.
export function UsageMeters({ usage, limits, requests }: { usage: ResourceUsage; limits?: ResourceUsage; requests?: ResourceUsage }) {
  const meter = (name: string, label: (n: number) => string, used: number, limit = 0, request = 0) => {
    const pct = limit ? Math.round((used / limit) * 100) : null;
    const hot = pct !== null && pct >= pressureThreshold;
    return (
      <span
        className={cn("flex items-center gap-1.5", hot ? "text-destructive" : "text-muted-foreground")}
        title={`${name} ${label(used)} used, ${request ? `${label(request)} requested` : "no request"}, ${limit ? `${label(limit)} limit` : "no limit"}`}
      >
        {pct !== null && (
          <span className="h-1.5 w-10 overflow-hidden rounded-full bg-muted">
            <span className={cn("block h-full rounded-full", hot ? "bg-destructive" : "bg-foreground/40")} style={{ width: `${Math.min(pct, 100)}%` }} />
          </span>
        )}
        <span className="w-10 text-right">{pct !== null ? `${pct}%` : label(used)}</span>
      </span>
    );
  };
  return (
    <span className="flex shrink-0 gap-3 font-mono text-xs tabular-nums">
      {meter("cpu", cpuLabel, usage.cpu, limits?.cpu, requests?.cpu)}
      {meter("memory", memoryLabel, usage.memory, limits?.memory, requests?.memory)}
    </span>
  );
}

const resources = (r?: { [_ in string]?: string } | null) =>
  Object.entries(r ?? {})
    .map(([k, v]) => `${k} ${v}`)
    .join(", ") || "—";

export function PodDetail({ cluster, target, onClose }: { cluster: Cluster; target: Target; onClose: () => void }) {
  const pod = useQuery(podQuery(cluster.id, target.namespace, target.name));
  const metrics = useQuery(podMetricsQuery(cluster.id));
  const usage = metrics.data?.get(podUsageKey(target.namespace, target.name));
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
          <DialogDescription className="flex flex-wrap items-center gap-x-3">
            {d
              ? [d.phase, d.node && `on ${d.node}`, target.ports.length > 0 && portsLabel(target.ports), d.events?.[0] && `last event ${ago(d.events[0].time)}`]
                  .filter(Boolean)
                  .join(" · ")
              : "Loading…"}
            {usage && <UsageMeters usage={usage.usage} limits={target.limits} requests={target.requests} />}
          </DialogDescription>
        </DialogHeader>
        {pod.error && <p className="text-xs text-destructive">{String(pod.error)}</p>}
        <DetailTabs cluster={cluster} kind="pod" namespace={target.namespace} name={target.name}>
          {d && (
            <>
              <Section title="Containers">
                {d.containers?.map((c) => (
                  <Container
                    key={c.name}
                    container={c}
                    usage={usage?.containers?.[c.name]}
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
            </>
          )}
        </DetailTabs>
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

function Container({ container: c, usage, onPrevious, pending }: { container: ContainerDiagnosis; usage?: ResourceUsage; onPrevious: () => void; pending: boolean }) {
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
          {usage && `using cpu ${cpuLabel(usage.cpu)}, memory ${memoryLabel(usage.memory)} · `}
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
