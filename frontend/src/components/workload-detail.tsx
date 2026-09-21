import { useQuery } from "@tanstack/react-query";
import { ArrowsClockwiseIcon } from "@phosphor-icons/react";
import { RolloutState, type Cluster, type KubeWorkload } from "@bindings/internal/service";
import { ago, Events, ReasonBadge, Section } from "@/components/pod-detail";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { workloadQuery } from "@/queries";
import { cn, isZeroTime } from "@/lib/utils";

// workloadReason is what a row's badge says: the rollout state, or that the CronJob is suspended.
export const workloadReason = (w: KubeWorkload) => w.rollout?.state ?? (w.cronJob?.suspend ? "suspended" : undefined);

// workloadLabel is the row's one-line summary: ready over desired, or a CronJob's schedule and last run.
export function workloadLabel(w: KubeWorkload) {
  if (w.rollout) return `${w.rollout.ready}/${w.rollout.desired} ready`;
  if (w.cronJob) return [w.cronJob.schedule, !isZeroTime(w.cronJob.lastScheduled) && `last run ${ago(w.cronJob.lastScheduled)}`].filter(Boolean).join(" · ");
  return "";
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
        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-auto">
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
        </div>
      </DialogContent>
    </Dialog>
  );
}
