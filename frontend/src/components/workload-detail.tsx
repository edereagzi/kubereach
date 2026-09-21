import { Fragment } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowsClockwiseIcon } from "@phosphor-icons/react";
import { RolloutState, type Cluster, type KubeWorkload } from "@bindings/internal/service";
import { ago, Events, ReasonBadge, Section } from "@/components/pod-detail";
import { workloadKind } from "@/components/targets";
import { Badge } from "@/components/ui/badge";
import { CopyButton } from "@/components/copy-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DetailTabs } from "@/components/yaml-view";
import { workloadQuery } from "@/queries";
import { cn, isZeroTime } from "@/lib/utils";

// workloadReason is what a row's badge says: the rollout state, or that the CronJob is suspended.
export const workloadReason = (w: KubeWorkload) => w.rollout?.state ?? (w.cronJob?.suspend ? "suspended" : undefined);

// workloadLabel is the row's one-line summary: ready over desired or a CronJob's schedule and last run, then the image tags.
export function workloadLabel(w: KubeWorkload) {
  const state = w.rollout
    ? [`${w.rollout.ready}/${w.rollout.desired} ready`]
    : [w.cronJob?.schedule, w.cronJob && !isZeroTime(w.cronJob.lastScheduled) && `last run ${ago(w.cronJob.lastScheduled)}`];
  return [...state, [...new Set(w.containers?.map((c) => c.tag))].join(", ")].filter(Boolean).join(" · ");
}

export function WorkloadDetail({ cluster, workload, onClose }: { cluster: Cluster; workload: KubeWorkload; onClose: () => void }) {
  const q = useQuery(workloadQuery(cluster.id, workload.kind, workload.namespace, workload.name));
  const d = q.data;
  const w = d?.workload ?? workload;
  const r = w.rollout;
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="flex max-h-[calc(100vh-4rem)] flex-col sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 pr-8">
            <span className="truncate">
              {w.namespace}/{w.name}
            </span>
            <ReasonBadge reason={workloadReason(w)} />
            <Button variant="ghost" size="icon-sm" title="Refresh" disabled={q.isFetching} onClick={() => q.refetch()}>
              <ArrowsClockwiseIcon className={cn(q.isFetching && "animate-spin")} />
            </Button>
          </DialogTitle>
          <DialogDescription>{[w.kind, workloadLabel(w), d?.events?.[0] && `last event ${ago(d.events[0].time)}`].filter(Boolean).join(" · ")}</DialogDescription>
        </DialogHeader>
        {q.error && <p className="text-xs text-destructive">{String(q.error)}</p>}
        <DetailTabs cluster={cluster} kind={workloadKind[w.kind] ?? "deploy"} namespace={w.namespace} name={w.name}>
          {r && (
            <Section title="Rollout">
              <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5 text-xs">
                <dt className="text-muted-foreground">State</dt>
                <dd className={cn(r.state === RolloutState.RolloutStuck && "text-destructive")}>
                  {r.state}
                  {r.message && <span className="text-muted-foreground"> — {r.message}</span>}
                </dd>
                <dt className="text-muted-foreground">Replicas</dt>
                <dd className="font-mono">
                  {r.desired} desired · {r.updated} updated · {r.ready} ready · {r.available} available
                </dd>
                <dt className="text-muted-foreground">Revision</dt>
                <dd className="font-mono">{r.revision || "—"}</dd>
              </dl>
            </Section>
          )}
          {!!w.containers?.length && (
            <Section title="Images">
              <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5 text-xs">
                {w.containers.map((c) => (
                  <Fragment key={c.name}>
                    <dt className="flex items-center gap-2 text-muted-foreground">
                      {c.name}
                      {c.init && <Badge variant="outline">init</Badge>}
                    </dt>
                    <dd className="group flex min-w-0 items-center gap-1 font-mono">
                      <span className="truncate" title={c.image}>
                        {c.image}
                      </span>
                      <CopyButton text={c.image} title="Copy image" />
                    </dd>
                  </Fragment>
                ))}
              </dl>
            </Section>
          )}
          {w.cronJob && (
            <Section title="Schedule">
              <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5 text-xs">
                <dt className="text-muted-foreground">Cron</dt>
                <dd className="font-mono">
                  {w.cronJob.schedule}
                  {w.cronJob.suspend && <span className="text-destructive"> · suspended</span>}
                </dd>
                <dt className="text-muted-foreground">Last run</dt>
                <dd>{isZeroTime(w.cronJob.lastScheduled) ? "never" : `${ago(w.cronJob.lastScheduled)} (${new Date(w.cronJob.lastScheduled).toLocaleString()})`}</dd>
              </dl>
            </Section>
          )}
          {d && (
            <Section title="Events">
              {d.eventsError ? <p className="text-xs text-destructive">{d.eventsError}</p> : <Events events={d.events ?? []} />}
            </Section>
          )}
        </DetailTabs>
      </DialogContent>
    </Dialog>
  );
}
