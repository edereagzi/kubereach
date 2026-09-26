import { Fragment } from "react";
import { useQuery } from "@tanstack/react-query";
import { JobResult, RolloutState, type Cluster, type JobState, type KubeWorkload } from "@bindings/internal/service";
import { hpaLabel } from "@/components/hpa-detail";
import { ago, Events, PodList, ReasonBadge, Section } from "@/components/pod-detail";
import { rowKind, workloadKind, type Target } from "@/components/targets";
import { TargetVerbs } from "@/components/target-verbs";
import { useUIStore } from "@/store";
import { WorkloadActions } from "@/components/actions";
import { Badge } from "@/components/ui/badge";
import { CopyButton } from "@/components/copy-button";
import { RefreshButton } from "@/components/refresh-button";
import { Inspector, InspectorDescription, InspectorHeader, InspectorTitle } from "@/components/inspector";
import { DetailTabs } from "@/components/yaml-view";
import { hpasQuery, workloadQuery, errorText } from "@/queries";
import { cn, isZeroTime } from "@/lib/utils";

// workloadReason is what a row's badge says: the rollout state, why a Job failed, or that a CronJob or Job is suspended.
export const workloadReason = (w: KubeWorkload) =>
  w.rollout?.state ?? (w.job?.result === JobResult.JobFailed ? w.job.reason || "failed" : w.cronJob?.suspend || w.job?.suspend ? "suspended" : undefined);

// workloadLabel is the row's one-line summary: ready over desired, a CronJob's schedule and last run, or a Job's run. The images are in the detail.
export function workloadLabel(w: KubeWorkload) {
  if (w.job) return jobLabel(w.job);
  const state = w.rollout
    ? [`${w.rollout.ready}/${w.rollout.desired} ready`]
    : [w.cronJob?.schedule, w.cronJob && !isZeroTime(w.cronJob.lastScheduled) && `last run ${ago(w.cronJob.lastScheduled)}`];
  return state.filter(Boolean).join(" · ");
}

// took is how long a Job ran, or has run so far, in its two largest units: "42s", "1m 12s", "3h 5m".
const spans = [["d", 86400], ["h", 3600], ["m", 60], ["s", 1]] as const;
function took(j: JobState) {
  const end = isZeroTime(j.finishedAt) ? Date.now() : new Date(j.finishedAt).getTime();
  const s = Math.max(0, Math.round((end - new Date(j.startedAt).getTime()) / 1000));
  const i = spans.findIndex(([, size]) => s >= size);
  if (i < 0) return "0s";
  const [[unit, size], next] = [spans[i], spans[i + 1]];
  const rest = next ? Math.floor((s % size) / next[1]) : 0;
  return `${Math.floor(s / size)}${unit}${rest ? ` ${rest}${next[0]}` : ""}`;
}

// jobLabel is a Job's state as its row says it; completions are counted where more than one is needed, or the run failed.
export function jobLabel(j: JobState) {
  const count = `${j.succeeded}/${j.completions} · `;
  const time = isZeroTime(j.startedAt) ? "" : ` in ${took(j)}`;
  if (j.result === JobResult.JobComplete) return `succeeded${time}`;
  if (j.result === JobResult.JobFailed) return `${count}failed${j.failed ? ` ${j.failed}×` : ""}${time}`;
  const many = j.completions > 1 ? count : "";
  if (j.suspend) return `${many}suspended`;
  return isZeroTime(j.startedAt) ? `${many}not started` : `${many}running ${took(j)}`;
}

export function WorkloadDetail({ cluster, target, workload, onClose }: { cluster: Cluster; target: Target; workload: KubeWorkload; onClose: () => void }) {
  const q = useQuery(workloadQuery(cluster.id, workload.kind, workload.namespace, workload.name));
  const d = q.data;
  const w = d?.workload ?? workload;
  const r = w.rollout;
  const cronJob = w.job?.cronJob;
  const kind = workloadKind[w.kind] ?? "deploy";
  const requestInspect = useUIStore((s) => s.requestInspect);
  // The autoscaler, if one scales it, is why it runs the replicas it does.
  const hpa = useQuery(hpasQuery(cluster.id)).data?.find((h) => h.namespace === w.namespace && h.targetName === w.name && rowKind(h.targetKind) === kind);
  return (
    <Inspector onClose={onClose}>
      <InspectorHeader>
        <InspectorTitle className="flex items-center gap-2 pr-8">
          <span className="truncate">
            {w.namespace}/{w.name}
          </span>
          <ReasonBadge reason={workloadReason(w)} />
          <RefreshButton fetching={q.isFetching} onRefresh={() => q.refetch()} />
        </InspectorTitle>
        <InspectorDescription>{[w.kind, workloadLabel(w), d?.events?.[0] && `last event ${ago(d.events[0].time)}`].filter(Boolean).join(" · ")}</InspectorDescription>
        <div className="flex flex-wrap gap-1.5">
          <TargetVerbs cluster={cluster} target={target} onLeave={onClose} />
          <WorkloadActions cluster={cluster} workload={w} />
        </div>
      </InspectorHeader>
      {q.error && <p className="text-xs text-destructive">{errorText(q.error)}</p>}
      <DetailTabs cluster={cluster} kind={kind} namespace={w.namespace} name={w.name}>
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
              {hpa && (
                <>
                  <dt className="text-muted-foreground">Autoscaler</dt>
                  <dd className="flex items-center gap-2">
                    <button
                      type="button"
                      className="hover:underline"
                      title="Open the HorizontalPodAutoscaler"
                      onClick={() => requestInspect({ clusterId: cluster.id, kind: "hpa", namespace: hpa.namespace, name: hpa.name })}
                    >
                      {hpa.name}
                    </button>
                    <span className="font-mono text-muted-foreground">{hpaLabel(hpa)}</span>
                    <ReasonBadge reason={hpa.problem} />
                  </dd>
                </>
              )}
              <dt className="text-muted-foreground">Revision</dt>
              <dd className="font-mono">{r.revision || "—"}</dd>
            </dl>
          </Section>
        )}
        {w.job && (
          <Section title="Run">
            <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5 text-xs">
              <dt className="text-muted-foreground">State</dt>
              <dd className={cn(w.job.result === JobResult.JobFailed && "text-destructive")}>
                {jobLabel(w.job)}
                {w.job.message && <span className="text-muted-foreground"> — {w.job.message}</span>}
              </dd>
              <dt className="text-muted-foreground">Pods</dt>
              <dd className="font-mono">
                {w.job.completions} needed · {w.job.active} active · {w.job.succeeded} succeeded · {w.job.failed} failed
              </dd>
              <dt className="text-muted-foreground">Started</dt>
              <dd>{isZeroTime(w.job.startedAt) ? "not yet" : `${ago(w.job.startedAt)} (${new Date(w.job.startedAt).toLocaleString()})`}</dd>
              {cronJob && (
                <>
                  <dt className="text-muted-foreground">CronJob</dt>
                  <dd>
                    <button
                      type="button"
                      className="hover:underline"
                      title="Open the CronJob"
                      onClick={() => requestInspect({ clusterId: cluster.id, kind: "cron", namespace: w.namespace, name: cronJob })}
                    >
                      {cronJob}
                    </button>
                  </dd>
                </>
              )}
            </dl>
          </Section>
        )}
        {!!d?.pods?.length && (
          <Section title="Pods">
            <PodList cluster={cluster} pods={d.pods} />
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
    </Inspector>
  );
}
