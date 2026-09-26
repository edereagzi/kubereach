import { Fragment } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Cluster, HPAMetric, KubeHPA } from "@bindings/internal/service";
import { Events, ReasonBadge, Section } from "@/components/pod-detail";
import { rowKind, type Target } from "@/components/targets";
import { RefreshButton } from "@/components/refresh-button";
import { Inspector, InspectorDescription, InspectorHeader, InspectorTitle } from "@/components/inspector";
import { DetailTabs } from "@/components/yaml-view";
import { hpaQuery, errorText } from "@/queries";
import { useUIStore } from "@/store";
import { cn } from "@/lib/utils";

// hpaLabel is the row's figure: the replicas it runs within its range, and where it is heading while a rescale is under way.
// An autoscaler that could not compute a count reports 0 desired, which is no rescale.
export const hpaLabel = (h: KubeHPA) => `${h.current}${h.desired && h.desired !== h.current ? ` → ${h.desired}` : ""} of ${h.min}–${h.max}`;

// A metric it cannot read is what leaves it unable to scale, so it says so rather than showing nothing.
const metricLabel = (m: HPAMetric) => `${m.current || "unknown"} / ${m.target}`;
export const metricsTitle = (h: KubeHPA) => (h.metrics ?? []).map((m) => `${m.name} ${metricLabel(m)}`).join("\n") || undefined;

export function HPADetail({ cluster, target, hpa, onClose }: { cluster: Cluster; target: Target; hpa: KubeHPA; onClose: () => void }) {
  const q = useQuery(hpaQuery(cluster.id, target.namespace, target.name));
  const requestInspect = useUIStore((s) => s.requestInspect);
  const d = q.data;
  const h = d?.hpa ?? hpa;
  const kind = rowKind(h.targetKind);
  return (
    <Inspector onClose={onClose}>
      <InspectorHeader>
        <InspectorTitle className="flex items-center gap-2 pr-8">
          <span className="truncate">
            {h.namespace}/{h.name}
          </span>
          <ReasonBadge reason={h.problem} />
          <RefreshButton fetching={q.isFetching} onRefresh={() => q.refetch()} />
        </InspectorTitle>
        <InspectorDescription>{["HorizontalPodAutoscaler", hpaLabel(h)].join(" · ")}</InspectorDescription>
      </InspectorHeader>
      {q.error && <p className="text-xs text-destructive">{errorText(q.error)}</p>}
      <DetailTabs cluster={cluster} kind="hpa" namespace={h.namespace} name={h.name}>
        <Section title="Scaling">
          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5 text-xs">
            <dt className="text-muted-foreground">Scales</dt>
            <dd>
              {kind ? (
                <button
                  type="button"
                  className="hover:underline"
                  title={`Open the ${h.targetKind}`}
                  onClick={() => requestInspect({ clusterId: cluster.id, kind, namespace: h.namespace, name: h.targetName })}
                >
                  {h.targetKind}/{h.targetName}
                </button>
              ) : (
                `${h.targetKind}/${h.targetName}`
              )}
            </dd>
            <dt className="text-muted-foreground">Replicas</dt>
            <dd className="font-mono">
              {h.current} current · {!!h.desired && `${h.desired} desired · `}
              {h.min} min · {h.max} max
            </dd>
          </dl>
        </Section>
        {!!h.metrics?.length && (
          <Section title="Metrics">
            <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5 text-xs">
              {h.metrics.map((m, i) => (
                <Fragment key={i}>
                  <dt className="text-muted-foreground">{m.name}</dt>
                  <dd className={cn("font-mono", !m.current && "text-destructive")} title="Current / target">
                    {metricLabel(m)}
                  </dd>
                </Fragment>
              ))}
            </dl>
          </Section>
        )}
        {!!h.conditions?.length && (
          <Section title="Conditions">
            <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5 text-xs">
              {h.conditions.map((c) => (
                <Fragment key={c.type}>
                  <dt className="text-muted-foreground">{c.type}</dt>
                  <dd className={cn(h.problem && c.reason === h.problem && "text-destructive")}>
                    {c.status} · {c.reason}
                    {c.message && <span className="text-muted-foreground"> — {c.message}</span>}
                  </dd>
                </Fragment>
              ))}
            </dl>
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
