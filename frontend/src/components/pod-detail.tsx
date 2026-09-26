import { Fragment, type ComponentProps, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Cluster, ContainerDiagnosis, ContainerState, KubeEvent, PodCondition, ResourceUsage, WorkloadKind } from "@bindings/internal/service";
import { DeletePodAction } from "@/components/actions";
import { useStartLogs } from "@/components/logs";
import { portsLabel, workloadKind, type Target } from "@/components/targets";
import { TargetVerbs } from "@/components/target-verbs";
import { Badge } from "@/components/ui/badge";
import { RefreshButton } from "@/components/refresh-button";
import { Button } from "@/components/ui/button";
import { Inspector, InspectorDescription, InspectorHeader, InspectorTitle } from "@/components/inspector";
import { DetailTabs } from "@/components/yaml-view";
import { podMetricsQuery, podQuery, podUsageKey, errorText } from "@/queries";
import { useUIStore } from "@/store";
import { cn, isZeroTime } from "@/lib/utils";

// States that are normal passage or a deliberate choice rather than a fault; a complete rollout has nothing to say on a row.
const calmReasons = new Set(["ContainerCreating", "PodInitializing", "Pending", "Terminating", "Completed", "progressing", "suspended"]);

// Shaped like KindBadge so the two sit on one row as one system; red only when the reason is a fault.
export function ReasonBadge({ reason, className, ...props }: { reason?: string } & ComponentProps<typeof Badge>) {
  if (!reason || reason === "complete") return null;
  return (
    <Badge
      variant={calmReasons.has(reason) ? "secondary" : "destructive"}
      className={cn("h-[18px] max-w-full rounded px-1.5 font-mono text-[11px]", calmReasons.has(reason) && "bg-foreground/8", className)}
      title={reason}
      {...props}
    >
      <span className="truncate">{reason}</span>
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

// since renders a compact age such as "57s" or "3h" for columns of times, where "ago" would be said on every line.
export function since(iso: string) {
  if (isZeroTime(iso)) return "";
  const seconds = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  const [unit, size] = ([["d", 86400], ["h", 3600], ["m", 60], ["s", 1]] as const).find(([, s]) => seconds >= s) ?? ["s", 1];
  return `${Math.floor(seconds / size)}${unit}`;
}

const stateLabel = (st: ContainerState) => {
  if (!st.status) return "not started";
  const parts = [st.status, st.reason];
  if (st.status === "running") parts.push(`since ${ago(st.startedAt)}`);
  if (st.status === "terminated") parts.push(`exit ${st.exitCode}`, `ended ${ago(st.finishedAt)}`);
  return parts.filter(Boolean).join(" · ");
};

// A restart weeks ago is history rather than a fault, so a row says when the last one was.
export const restartsLabel = (n: number, last?: string) => [`${n} restart${n === 1 ? "" : "s"}`, n > 0 && last && ago(last)].filter(Boolean).join(", ");

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
  const meter = (name: string, short: string, label: (n: number) => string, used: number, limit = 0, request = 0) => {
    const pct = limit ? Math.round((used / limit) * 100) : null;
    const hot = pct !== null && pct >= pressureThreshold;
    return (
      <span
        className={cn("flex items-center gap-1.5", hot ? "text-destructive" : "text-muted-foreground")}
        title={`${name} ${label(used)} used, ${request ? `${label(request)} requested` : "no request"}, ${limit ? `${label(limit)} limit` : "no limit"}`}
      >
        <span>{short}</span>
        {pct !== null && (
          <span className="h-1.5 w-10 overflow-hidden rounded-full bg-muted">
            <span className={cn("block h-full rounded-full", hot ? "bg-destructive" : "bg-foreground/40")} style={{ width: `${Math.min(pct, 100)}%` }} />
          </span>
        )}
        <span className={cn(pct !== null && "w-10 text-right")}>{pct !== null ? `${pct}%` : label(used)}</span>
      </span>
    );
  };
  return (
    <span className="flex shrink-0 gap-3 font-mono text-xs tabular-nums">
      {meter("cpu", "cpu", cpuLabel, usage.cpu, limits?.cpu, requests?.cpu)}
      {meter("memory", "mem", memoryLabel, usage.memory, limits?.memory, requests?.memory)}
    </span>
  );
}

const resources = (r?: { [_ in string]?: string } | null) =>
  Object.entries(r ?? {})
    .map(([k, v]) => `${k} ${v}`)
    .join(", ") || "—";

export function PodDetail({ cluster, target, onForward, onClose }: { cluster: Cluster; target: Target; onForward: () => void; onClose: () => void }) {
  const pod = useQuery(podQuery(cluster.id, target.namespace, target.name));
  const metrics = useQuery(podMetricsQuery(cluster.id));
  const usage = metrics.data?.get(podUsageKey(target.namespace, target.name));
  const startLogs = useStartLogs(cluster);
  const requestInspect = useUIStore((s) => s.requestInspect);
  const selectTab = useUIStore((s) => s.selectTab);
  const d = pod.data;
  // WorkloadKind values are the lowercased Kubernetes kinds, so an owner the Overview lists has a Kind here; a bare Job does not.
  const owner = d?.owner;
  const ownerKind = owner && workloadKind[owner.kind.toLowerCase() as WorkloadKind];
  // The node's detail lives on the Nodes tab and shares this panel, so leaving for it closes the pod.
  const openNode = (node: string) => {
    requestInspect({ clusterId: cluster.id, kind: "node", namespace: "", name: node });
    selectTab("nodes");
    onClose();
  };
  const facts: ReactNode[] = d
    ? [
        d.phase,
        owner &&
          (ownerKind ? (
            <Link title={`Open the ${owner.kind}`} onClick={() => requestInspect({ clusterId: cluster.id, kind: ownerKind, namespace: target.namespace, name: owner.name })}>
              {owner.kind} {owner.name}
            </Link>
          ) : (
            `${owner.kind} ${owner.name}`
          )),
        d.node && (
          <>
            on{" "}
            <Link title="Open the node" onClick={() => openNode(d.node)}>
              {d.node}
            </Link>
          </>
        ),
        d.ip,
        <span title={isZeroTime(d.startedAt) ? "Not started yet" : `Started ${new Date(d.startedAt).toLocaleString()}`}>age {since(d.created)}</span>,
        target.ports.length > 0 && portsLabel(target.ports),
        d.events?.[0] && `last event ${ago(d.events[0].time)}`,
      ].filter(Boolean)
    : [];
  return (
    <Inspector onClose={onClose}>
      <InspectorHeader>
        <InspectorTitle className="flex items-center gap-2 pr-8">
          <span className="truncate">
            {target.namespace}/{target.name}
          </span>
          {d && <ReasonBadge reason={d.reason} />}
          <RefreshButton fetching={pod.isFetching} onRefresh={() => pod.refetch()} />
        </InspectorTitle>
        <InspectorDescription className="flex flex-wrap items-center gap-x-3">
          <span>
            {d
              ? facts.map((f, i) => (
                  <Fragment key={i}>
                    {i > 0 && " · "}
                    {f}
                  </Fragment>
                ))
              : "Loading…"}
          </span>
          {usage && <UsageMeters usage={usage.usage} limits={target.limits} requests={target.requests} />}
        </InspectorDescription>
        <div className="flex flex-wrap gap-1.5">
          <TargetVerbs cluster={cluster} target={target} onForward={onForward} onLeave={onClose} />
          <span className="ml-auto">
            <DeletePodAction cluster={cluster} target={target} onDone={onClose} />
          </span>
        </div>
      </InspectorHeader>
      {pod.error && <p className="text-xs text-destructive">{errorText(pod.error)}</p>}
      <DetailTabs cluster={cluster} kind="pod" namespace={target.namespace} name={target.name}>
        {d && (
          <>
            <Section title="Containers">
              {d.containers?.map((c) => (
                <Container
                  key={c.name}
                  container={c}
                  usage={usage?.containers?.[c.name]}
                  onPrevious={() => startLogs.mutate({ ...target, container: c.name, previous: true })}
                  pending={startLogs.isPending}
                />
              ))}
            </Section>
            <Section title="Conditions">
              <Conditions conditions={d.conditions ?? []} />
            </Section>
            {d.labels && Object.keys(d.labels).length > 0 && (
              <Section title="Labels">
                <Labels labels={d.labels} />
              </Section>
            )}
            <Section title="Events">
              {d.eventsError ? <p className="text-xs text-destructive">{d.eventsError}</p> : <Events events={d.events ?? []} />}
            </Section>
          </>
        )}
      </DetailTabs>
    </Inspector>
  );
}

// A link inside a line of muted text, told apart by its colour.
function Link({ className, ...props }: ComponentProps<"button">) {
  return <button type="button" className={cn("text-foreground hover:underline", className)} {...props} />;
}

// Labels are looked up rather than read, so they fold behind their keys.
function Labels({ labels }: { labels: { [_ in string]?: string } }) {
  const entries = Object.entries(labels);
  return (
    <details className="text-xs">
      <summary className="cursor-pointer truncate text-muted-foreground select-none hover:text-foreground">{entries.map(([k]) => k).join(", ")}</summary>
      <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5 pt-1.5 font-mono">
        {entries.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-muted-foreground">{k}</dt>
            <dd className="min-w-0 break-all">{v}</dd>
          </div>
        ))}
      </dl>
    </details>
  );
}

// A healthy pod's conditions are all True and say nothing, so they fold behind one line; any other status is shown open.
function Conditions({ conditions }: { conditions: PodCondition[] }) {
  const list = (
    <dl className="grid grid-cols-[max-content_max-content_1fr] gap-x-4 gap-y-0.5 text-xs">
      {conditions.map((c) => (
        <div key={c.type} className="contents">
          <dt className="font-medium">{c.type}</dt>
          <dd className={cn(c.status !== "True" && "text-destructive")}>{c.status}</dd>
          <dd className="truncate text-muted-foreground" title={c.message}>
            {[c.reason, c.message].filter(Boolean).join(": ")}
          </dd>
        </div>
      ))}
    </dl>
  );
  if (conditions.some((c) => c.status !== "True")) return list;
  return (
    <details className="text-xs">
      <summary className="cursor-pointer text-muted-foreground select-none hover:text-foreground">All {conditions.length} are True</summary>
      <div className="pt-1.5">{list}</div>
    </details>
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
        <span className="min-w-0 truncate font-mono text-muted-foreground" title={c.image}>
          {c.image}
        </span>
        <span className="ml-auto shrink-0 text-muted-foreground">
          {restartsLabel(c.restarts)}
          {c.restarts > 0 && c.lastState && ` · last ${ago(c.lastState.finishedAt)}`}
        </span>
      </div>
      <dl className="mt-1.5 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5">
        <dt className="text-muted-foreground">State</dt>
        <dd className="min-w-0">
          {stateLabel(c.state)}
          {c.state.message && (
            <p className="line-clamp-2 break-words text-muted-foreground" title={c.state.message}>
              {c.state.message}
            </p>
          )}
        </dd>
        {c.lastState && (
          <>
            <dt className="text-muted-foreground">Previous run</dt>
            <dd className="flex items-baseline gap-3">
              <span>{stateLabel(c.lastState)}</span>
              <Button variant="link" className="h-auto p-0 text-xs" disabled={pending} onClick={onPrevious}>
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
    <ul className="grid grid-cols-[max-content_max-content_1fr] gap-x-3 gap-y-1.5 text-xs">
      {events.map((e, i) => (
        <li key={i} className="contents">
          <span className="text-right text-muted-foreground tabular-nums" title={`${ago(e.time)}, ${new Date(e.time).toLocaleString()}`}>
            {since(e.time)}
          </span>
          <span className={cn("font-medium", e.type === "Warning" && "text-destructive")}>
            {e.reason}
            {e.count > 1 && <span className="font-normal text-muted-foreground tabular-nums"> ×{e.count}</span>}
          </span>
          <span className="min-w-0 break-words whitespace-pre-wrap">{e.message}</span>
        </li>
      ))}
    </ul>
  );
}
