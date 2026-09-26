import { useQuery } from "@tanstack/react-query";
import type { Cluster, KubePVC } from "@bindings/internal/service";
import { Events, PodList, ReasonBadge, Section } from "@/components/pod-detail";
import type { Target } from "@/components/targets";
import { RefreshButton } from "@/components/refresh-button";
import { Inspector, InspectorDescription, InspectorHeader, InspectorTitle } from "@/components/inspector";
import { DetailTabs } from "@/components/yaml-view";
import { pvcQuery, errorText } from "@/queries";
import { cn } from "@/lib/utils";

// pvcReason is what a row's badge says: nothing once bound, "unbound" while no volume backs the claim, "lost" once its volume is gone.
export const pvcReason = (c: KubePVC) => (c.phase === "Bound" ? undefined : c.phase === "Lost" ? "lost" : "unbound");

// pvcLabel is the row's figure: the volume's size and class, or what an unbound claim asks for.
export const pvcLabel = (c: KubePVC) => [c.phase === "Bound" ? c.size : `wants ${c.size}`, c.storageClass].filter(Boolean).join(" · ");

export function PVCDetail({ cluster, target, pvc, onClose }: { cluster: Cluster; target: Target; pvc: KubePVC; onClose: () => void }) {
  const q = useQuery(pvcQuery(cluster.id, target.namespace, target.name));
  const d = q.data;
  const c = d?.pvc ?? pvc;
  return (
    <Inspector onClose={onClose}>
      <InspectorHeader>
        <InspectorTitle className="flex items-center gap-2 pr-8">
          <span className="truncate">
            {c.namespace}/{c.name}
          </span>
          <ReasonBadge reason={pvcReason(c)} />
          <RefreshButton fetching={q.isFetching} onRefresh={() => q.refetch()} />
        </InspectorTitle>
        <InspectorDescription>{["PersistentVolumeClaim", pvcLabel(c)].join(" · ")}</InspectorDescription>
      </InspectorHeader>
      {q.error && <p className="text-xs text-destructive">{errorText(q.error)}</p>}
      <DetailTabs cluster={cluster} kind="pvc" namespace={c.namespace} name={c.name}>
        <Section title="Claim">
          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5 text-xs">
            <dt className="text-muted-foreground">Phase</dt>
            <dd className={cn(pvcReason(c) && "text-destructive")}>{c.phase || "Pending"}</dd>
            <dt className="text-muted-foreground">{c.phase === "Bound" ? "Capacity" : "Asks for"}</dt>
            <dd className="font-mono">{c.size}</dd>
            <dt className="text-muted-foreground">Storage class</dt>
            <dd className="font-mono">{c.storageClass || "—"}</dd>
            <dt className="text-muted-foreground">Volume</dt>
            <dd className="font-mono">{c.volume || "—"}</dd>
            <dt className="text-muted-foreground">Access</dt>
            <dd className="font-mono">{c.accessModes?.join(", ") || "—"}</dd>
          </dl>
        </Section>
        {d && (
          <Section title="Mounted by">
            {d.podsError ? (
              <p className="text-xs text-destructive">{d.podsError}</p>
            ) : d.pods?.length ? (
              <PodList cluster={cluster} pods={d.pods} />
            ) : (
              <p className="text-xs text-muted-foreground">No pod mounts this claim.</p>
            )}
          </Section>
        )}
        {d && (
          <Section title="Events">
            {d.eventsError ? <p className="text-xs text-destructive">{d.eventsError}</p> : <Events events={d.events ?? []} />}
          </Section>
        )}
      </DetailTabs>
    </Inspector>
  );
}
