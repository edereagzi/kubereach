import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Cluster, KubeNode, NodePod, ResourceUsage } from "@bindings/internal/service";
import { cpuLabel, Events, memoryLabel, ReasonBadge, Section } from "@/components/pod-detail";
import { RefreshButton } from "@/components/refresh-button";
import { Inspector, InspectorDescription, InspectorHeader, InspectorTitle, useInspectorWalk } from "@/components/inspector";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { DetailTabs } from "@/components/yaml-view";
import { isForbidden, nodeMetricsQuery, nodeQuery, nodesQuery, errorText } from "@/queries";
import { useUIStore } from "@/store";
import { cn } from "@/lib/utils";

const percent = (n: number, of: number) => (of > 0 ? Math.round((n / of) * 100) : 0);
const podsLabel = (n: number) => `${n} pod${n === 1 ? "" : "s"}`;

// Meter is one resource of a node: what its pods reserved, and what they are using of it. The two are drawn on one bar
// because the gap between them is the question — a node reserved solid but idle is not the same as a node actually full.
// A number nothing measured is a dash, never a zero; what is wrong with a node is the badge's job, so the bar stays neutral.
// Used is the fill and requested is a tick on it, not a second fill: a node using more than it reserved would otherwise
// paint over the reservation and hide it, which is the case worth seeing.
function Meter({ name, label, used, requested, allocatable, named = false }: { name: string; label: (n: number) => string; used?: number; requested?: number; allocatable: number; named?: boolean }) {
  const [usedPct, requestedPct] = [percent(used ?? 0, allocatable), percent(requested ?? 0, allocatable)];
  // The tick is two pixels wide on a bar of sixty-four, so at the extremes it is held just inside the track;
  // clipped in half against the edge it reads as a rendering fault rather than as a reservation.
  const tickAt = Math.min(Math.max(requestedPct, 2), 98);
  const said = (value: number | undefined, what: string) => (value === undefined ? `${what} unknown` : `${label(value)} ${what}`);
  return (
    <span
      className="flex items-center gap-2 font-mono text-xs tabular-nums text-muted-foreground"
      title={`${name}: ${said(used, "used")}, ${said(requested, "requested")} of ${label(allocatable)} allocatable`}
    >
      {/* The table names these in its column headers; on their own, as in the node detail, they have to name themselves. */}
      {named && <span className="shrink-0">{name === "memory" ? "mem" : name}</span>}
      <span className="relative h-1.5 w-16 shrink-0 overflow-hidden rounded-full bg-muted">
        {used !== undefined && <span className="absolute inset-y-0 left-0 rounded-full bg-foreground/55" style={{ width: `${Math.min(usedPct, 100)}%` }} />}
        {requested !== undefined && (
          <span className="absolute inset-y-0 w-0.5 -translate-x-1/2 bg-foreground/90" style={{ left: `${tickAt}%` }} />
        )}
      </span>
      {/* In the table the pair is a column and holds a width wide enough for "100% / 100%"; named, it sits in a
          sentence, where that width would strand it a centimetre from its own bar. */}
      <span className={cn("shrink-0 whitespace-nowrap", named ? "" : "w-24 text-right")}>
        {used === undefined ? "—" : `${usedPct}%`}
        <span className="opacity-60"> / {requested === undefined ? "—" : `${requestedPct}%`}</span>
      </span>
    </span>
  );
}

// A node under pressure must be findable from anywhere, and a node belongs to no namespace, so it cannot join the
// Overview's Problems filter. The tab carries the count instead, the way the Logs and Shell tabs say what is running on them.
export function NodeProblems({ cluster }: { cluster: Cluster }) {
  const { data } = useQuery(nodesQuery(cluster.id));
  const problems = (data ?? []).filter((n) => n.problem).length;
  if (problems === 0) return null;
  return (
    <span className="rounded-full bg-destructive/15 px-1.5 text-[11px] leading-4 text-destructive tabular-nums" title="Nodes that are not Ready or are under pressure">
      {problems}
    </span>
  );
}

export function ClusterNodes({ cluster }: { cluster: Cluster }) {
  const nodes = useQuery(nodesQuery(cluster.id));
  const usage = useQuery(nodeMetricsQuery(cluster.id)).data;
  const [inspecting, setInspecting] = useState<KubeNode | null>(null);
  const rows = useRef<KubeNode[]>([]);
  rows.current = nodes.data ?? [];
  useInspectorWalk(rows, inspecting, (n) => n.name, setInspecting);

  if (nodes.error) {
    return (
      <Empty className="justify-start border-0 pt-12">
        <EmptyHeader>
          <EmptyTitle>{isForbidden(nodes.error) ? "Nodes are forbidden for this role" : "Nodes could not be listed"}</EmptyTitle>
          <EmptyDescription>{isForbidden(nodes.error) ? "The rest of the Cluster is unaffected." : errorText(nodes.error)}</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }
  if (nodes.isPending) return null;
  if (nodes.data?.length === 0) {
    return (
      <Empty className="justify-start border-0 pt-12">
        <EmptyHeader>
          <EmptyTitle>No nodes</EmptyTitle>
          <EmptyDescription>This Cluster reports no nodes.</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  return (
    <div className="min-h-0 flex-1 overflow-auto px-4 pb-4">
      {inspecting && <NodeDetail cluster={cluster} node={inspecting} onClose={() => setInspecting(null)} />}
      <Table className="min-w-[52rem]">
        <TableHeader>
          <TableRow>
            <TableHead>Node</TableHead>
            <TableHead>Roles</TableHead>
            <TableHead>Version</TableHead>
            <TableHead title="Used and requested, against the node's allocatable CPU">CPU used / requested</TableHead>
            <TableHead title="Used and requested, against the node's allocatable memory">Memory used / requested</TableHead>
            <TableHead className="text-right" title="Running on the node, of the pods it accepts">Pods</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(nodes.data ?? []).map((n) => {
            const used = usage?.get(n.name);
            return (
              <TableRow
                key={n.name}
                data-row={n.name}
                className={cn("hover:bg-accent", n.name === inspecting?.name && "bg-accent shadow-[inset_2px_0_0_var(--primary)]")}
              >
                <TableCell className="max-w-80">
                  <span className="flex items-center gap-2">
                    <button type="button" className="truncate text-left font-medium hover:underline" title={`What is on ${n.name}?`} onClick={() => setInspecting(n)}>
                      {n.name}
                    </button>
                    <ReasonBadge reason={n.problem} className="cursor-pointer" title="What the node reports about itself" onClick={() => setInspecting(n)} />
                  </span>
                </TableCell>
                <TableCell className="max-w-48 truncate font-mono text-xs text-muted-foreground" title={n.roles?.join(", ")}>
                  {n.roles?.join(", ") || "—"}
                </TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">{n.version}</TableCell>
                <TableCell>
                  <Meter name="cpu" label={cpuLabel} used={used?.cpu} requested={n.unknown ? undefined : n.requested.cpu} allocatable={n.allocatable.cpu} />
                </TableCell>
                <TableCell>
                  <Meter name="memory" label={memoryLabel} used={used?.memory} requested={n.unknown ? undefined : n.requested.memory} allocatable={n.allocatable.memory} />
                </TableCell>
                <TableCell className="text-right font-mono text-xs tabular-nums text-muted-foreground" title={n.unknown ? "The Cluster's pods could not be listed" : undefined}>
                  {n.unknown ? "—" : n.podCapacity ? `${n.pods} / ${n.podCapacity}` : n.pods}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}

function NodeDetail({ cluster, node, onClose }: { cluster: Cluster; node: KubeNode; onClose: () => void }) {
  const q = useQuery(nodeQuery(cluster.id, node.name));
  // The node's own usage is the list's query, so it costs nothing extra here; Refresh has to take it too, or the
  // meters keep answering with the numbers the user pressed Refresh to be rid of.
  const metrics = useQuery(nodeMetricsQuery(cluster.id));
  const usage = metrics.data?.get(node.name);
  const d = q.data;
  const n = d?.node ?? node;
  return (
    <Inspector onClose={onClose}>
      <InspectorHeader>
        <InspectorTitle className="flex items-center gap-2 pr-8">
          <span className="truncate">{n.name}</span>
          <ReasonBadge reason={n.problem} />
          <RefreshButton
            fetching={q.isFetching || metrics.isFetching}
            onRefresh={() => {
              void q.refetch();
              void metrics.refetch();
            }}
          />
        </InspectorTitle>
        <InspectorDescription className="flex flex-wrap items-center gap-x-3">
          {[n.roles?.join(", "), n.version, !n.unknown && podsLabel(n.pods)].filter(Boolean).join(" · ")}
          <Meter named name="cpu" label={cpuLabel} used={usage?.cpu} requested={n.unknown ? undefined : n.requested.cpu} allocatable={n.allocatable.cpu} />
          <Meter named name="memory" label={memoryLabel} used={usage?.memory} requested={n.unknown ? undefined : n.requested.memory} allocatable={n.allocatable.memory} />
        </InspectorDescription>
      </InspectorHeader>
      {q.error && <p className="text-xs text-destructive">{errorText(q.error)}</p>}
      <DetailTabs cluster={cluster} kind="node" namespace="" name={n.name}>
        {d && (
          <>
            <Section title={d.usageAvailable ? "Pods by usage" : "Pods"}>
              {d.podsError ? (
                <p className="text-xs text-destructive">{d.podsError}</p>
              ) : d.pods?.length ? (
                d.pods.map((p) => (
                  <NodePodRow key={`${p.namespace}/${p.name}`} cluster={cluster} pod={p} allocatable={n.allocatable} measured={d.usageAvailable} onClose={onClose} />
                ))
              ) : (
                <p className="text-xs text-muted-foreground">This node runs no pods.</p>
              )}
            </Section>
            <Section title="Conditions">
              <dl className="grid grid-cols-[max-content_max-content_1fr] gap-x-4 gap-y-0.5 text-xs">
                {d.conditions?.map((c) => (
                  <div key={c.type} className="contents">
                    <dt className="font-medium">{c.type}</dt>
                    {/* Ready is the one condition a node should report true; every other one is a fault when it does. */}
                    <dd className={cn("font-mono", (c.type === "Ready") !== (c.status === "True") && "text-destructive")}>{c.status}</dd>
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
        {!d && !q.error && <p className="text-xs text-muted-foreground">Loading…</p>}
      </DetailTabs>
    </Inspector>
  );
}

// A pod's share of the node is what ranked it, so the row shows that share and the absolute numbers behind it.
// Without metrics-server nothing measured the pod, so the row says what it reserved instead of claiming it uses nothing.
function NodePodRow({ cluster, pod, allocatable, measured, onClose }: { cluster: Cluster; pod: NodePod; allocatable: ResourceUsage; measured: boolean; onClose: () => void }) {
  const requestInspect = useUIStore((s) => s.requestInspect);
  const selectTab = useUIStore((s) => s.selectTab);
  const share = Math.max(percent(pod.usage.cpu, allocatable.cpu), percent(pod.usage.memory, allocatable.memory));
  const open = () => {
    requestInspect({ clusterId: cluster.id, kind: "pod", namespace: pod.namespace, name: pod.name });
    selectTab("overview");
    onClose();
  };
  return (
    <div className="grid grid-cols-[1fr_max-content_max-content] items-center gap-3 rounded-md border px-3 py-1.5 text-xs">
      <span className="flex min-w-0 items-center gap-2">
        <button type="button" className="truncate text-left font-mono hover:underline" title={`Why is ${pod.name} in this state?`} onClick={open}>
          {pod.namespace}/{pod.name}
        </button>
        <ReasonBadge reason={pod.reason} className="cursor-pointer" onClick={open} />
      </span>
      <span
        className="font-mono tabular-nums text-muted-foreground"
        title={measured ? `requests cpu ${cpuLabel(pod.requests.cpu)}, memory ${memoryLabel(pod.requests.memory)}` : "This Cluster has no metrics-server, so nothing measured this pod"}
      >
        {measured ? "" : "requests "}cpu {cpuLabel(measured ? pod.usage.cpu : pod.requests.cpu)} · mem {memoryLabel(measured ? pod.usage.memory : pod.requests.memory)}
      </span>
      <span className="w-12 text-right font-mono tabular-nums text-muted-foreground" title="Its largest share of the node">
        {measured ? `${share}%` : "—"}
      </span>
    </div>
  );
}
