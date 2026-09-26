import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { MagnifyingGlassIcon } from "@phosphor-icons/react";
import { LogSourceKind, TargetKind, WorkloadKind, type Cluster, type KubeConfigObject, type KubeHPA, type KubeIngress, type KubePVC, type KubeWorkload, type NamedPort, type PodOwner, type ResourceUsage } from "@bindings/internal/service";
import {
  Combobox,
  ComboboxCollection,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxGroup,
  ComboboxInput,
  ComboboxItem,
  ComboboxLabel,
  ComboboxList,
} from "@/components/ui/combobox";
import { configMapsQuery, hpasQuery, ingressesQuery, podsQuery, pvcsQuery, secretsQuery, servicesQuery, workloadsQuery } from "@/queries";
import { cn } from "@/lib/utils";

export type Kind = "svc" | "ing" | "deploy" | "sts" | "ds" | "cron" | "job" | "hpa" | "pvc" | "pod" | "cm" | "secret" | "node";

// One row of anything a tab can act on; label is what the picker searches.
export type Target = {
  value: string;
  label: string;
  kind: Kind;
  namespace: string;
  name: string;
  ports: NamedPort[];
  containers: string[];
  // When the object was created; the Overview shows its age.
  created?: string;
  // Set when the row stands for one container of a pod rather than the pod itself.
  container?: string;
  // Pods only: what is wrong, how often it restarted, and what its containers ask for.
  reason?: string;
  restarts?: number;
  lastRestart?: string;
  requests?: ResourceUsage;
  limits?: ResourceUsage;
  // Pods and Jobs: the value of the row it is listed under, the workload that runs a pod or the CronJob that started a Job.
  owner?: string;
  // Workloads and Jobs: rollout state, the CronJob's schedule, or the Job's run.
  workload?: KubeWorkload;
  // ConfigMaps only: the keys, never the values. Secrets: nothing beyond the name.
  config?: KubeConfigObject;
  // Ingresses only: the hosts; the paths and the pods behind them come with the detail.
  ingress?: KubeIngress;
  // PersistentVolumeClaims only: phase, size and class; the pods that mount it come with the detail.
  pvc?: KubePVC;
  // HorizontalPodAutoscalers only: range, replicas, metrics and conditions; listed under the workload they scale.
  hpa?: KubeHPA;
};

// error is set when the group could not be listed while the rest of the scope could; the Overview shows it beside the list.
export type TargetGroup = { label: string; items: Target[]; error?: unknown };

export const workloadKind: Partial<Record<WorkloadKind, Kind>> = { deployment: "deploy", statefulset: "sts", daemonset: "ds", cronjob: "cron", job: "job" };
export const logKind: Partial<Record<Kind, LogSourceKind>> = {
  deploy: LogSourceKind.LogSourceDeployment,
  sts: LogSourceKind.LogSourceStatefulSet,
  ds: LogSourceKind.LogSourceDaemonSet,
  pod: LogSourceKind.LogSourcePod,
};
export const forwardKind = (kind: Kind) => (kind === "svc" ? TargetKind.TargetService : TargetKind.TargetPod);
export const targetValue = (kind: Kind, namespace: string, name: string) => `${kind}:${namespace}/${name}`;

// rowKind is the row kind of a Kubernetes workload kind, such as Deployment, when the Overview lists that kind.
export const rowKind = (kubeKind: string) => workloadKind[kubeKind.toLowerCase() as WorkloadKind];

function ownerValue(namespace: string, owner?: PodOwner | null) {
  const kind = owner && rowKind(owner.kind);
  return kind ? targetValue(kind, namespace, owner.name) : undefined;
}

// overview adds Ingresses, Jobs, HorizontalPodAutoscalers, PersistentVolumeClaims, ConfigMaps and Secrets, which only the Overview lists; a role that cannot read one still gets the rest.
export function useTargets(cluster: Cluster, overview = false) {
  const services = useQuery(servicesQuery(cluster.id));
  const workloads = useQuery(workloadsQuery(cluster.id));
  const pods = useQuery(podsQuery(cluster.id));
  const ingresses = useQuery({ ...ingressesQuery(cluster.id), enabled: overview });
  const hpas = useQuery({ ...hpasQuery(cluster.id), enabled: overview });
  const pvcs = useQuery({ ...pvcsQuery(cluster.id), enabled: overview });
  const configMaps = useQuery({ ...configMapsQuery(cluster.id), enabled: overview });
  const secrets = useQuery({ ...secretsQuery(cluster.id), enabled: overview });
  const make = (kind: Kind, { namespace, name, created }: { namespace: string; name: string; created: string }, ports: NamedPort[] = [], containers: string[] = []): Target => ({
    value: targetValue(kind, namespace, name),
    label: `${namespace}/${name}`,
    kind,
    namespace,
    name,
    ports,
    containers,
    created,
  });
  // Listed the way a request travels and an incident is traced: in at the Ingress, out at the pod.
  const groups: TargetGroup[] = [
    { label: "Services", items: (services.data ?? []).map((s) => make("svc", s, s.ports ?? [])) },
    { label: "Workloads", items: (workloads.data ?? []).filter((w) => !w.job).map((w) => ({ ...make(workloadKind[w.kind] ?? "deploy", w), workload: w })) },
    ...(overview
      ? [
          { label: "Jobs", items: (workloads.data ?? []).filter((w) => w.job).map((w) => ({ ...make("job", w), workload: w, owner: w.job?.cronJob ? targetValue("cron", w.namespace, w.job.cronJob) : undefined })) },
          { label: "Autoscalers", items: (hpas.data ?? []).map((h) => ({ ...make("hpa", h), hpa: h, owner: ownerValue(h.namespace, { kind: h.targetKind, name: h.targetName }) })), error: hpas.error },
          { label: "Volume claims", items: (pvcs.data ?? []).map((c) => ({ ...make("pvc", c), pvc: c })), error: pvcs.error },
        ]
      : []),
    { label: "Pods", items: (pods.data ?? []).map((p) => ({ ...make("pod", p, p.ports ?? [], p.containers ?? []), reason: p.reason, restarts: p.restarts, lastRestart: p.lastRestart, requests: p.requests, limits: p.limits, owner: ownerValue(p.namespace, p.owner) })) },
  ];
  if (overview) {
    groups.unshift({ label: "Ingresses", items: (ingresses.data ?? []).map((i) => ({ ...make("ing", i), ingress: i })), error: ingresses.error });
    groups.push(
      { label: "ConfigMaps", items: (configMaps.data ?? []).map((c) => ({ ...make("cm", c), config: c })), error: configMaps.error },
      { label: "Secrets", items: (secrets.data ?? []).map((c) => ({ ...make("secret", c), config: c })), error: secrets.error },
    );
  }
  return {
    groups,
    error: services.error ?? workloads.error ?? pods.error,
    pending: services.isPending || workloads.isPending || pods.isPending || (overview && (ingresses.isPending || hpas.isPending || pvcs.isPending || configMaps.isPending || secrets.isPending)),
    fetching: [services, workloads, pods, ingresses, hpas, pvcs, configMaps, secrets].some((q) => q.isFetching),
  };
}

// kind is a Kind, or any short name for one the app has no row for (an event's ReplicaSet, say).
// The fill is translucent so the badge still shows on a hovered row, which is painted in the muted colour itself.
export function KindBadge({ kind, className, title }: { kind: Kind | string; className?: string; title?: string }) {
  return (
    <span
      title={title}
      className={cn("inline-flex h-[18px] w-12 shrink-0 items-center justify-center truncate rounded bg-foreground/8 px-1 font-mono text-[11px] font-medium text-muted-foreground", className)}
    >
      {kind}
    </span>
  );
}

export function TargetRow({ target, meta }: { target: Target; meta?: ReactNode }) {
  return (
    <>
      <KindBadge kind={target.kind} />
      <span className="truncate">{target.label}</span>
      {meta && <span className="ml-auto truncate pl-2 font-mono text-[11px] text-muted-foreground">{meta}</span>}
    </>
  );
}

export const portsLabel = (ports: NamedPort[]) => ports.map((p) => (p.name ? `${p.port} ${p.name}` : String(p.port))).join(" · ");

// TargetPicker is the one searchable list every tab starts from; the trigger is whatever the tab shows when nothing is being picked.
export function TargetPicker({
  groups,
  value,
  onPick,
  placeholder = "Search services, workloads and pods",
  inline = false,
  children,
}: {
  groups: TargetGroup[];
  value?: Target | null;
  onPick: (target: Target) => void;
  placeholder?: string;
  // Inline puts the search box in the page (children) instead of inside the popup.
  inline?: boolean;
  children: ReactNode;
}) {
  return (
    <Combobox
      items={groups.filter((g) => g.items.length > 0)}
      value={value ?? null}
      onValueChange={(t: Target | null) => t && onPick(t)}
      isItemEqualToValue={(a, b) => a?.value === b?.value}
    >
      {children}
      <ComboboxContent className={cn(!inline && "w-[40rem] min-w-(--anchor-width)")}>
        {!inline && (
          <ComboboxInput showTrigger={false} placeholder={placeholder} autoFocus>
            <MagnifyingGlassIcon className="order-first ml-2 size-4 text-muted-foreground" />
          </ComboboxInput>
        )}
        <ComboboxEmpty>Nothing matches.</ComboboxEmpty>
        <ComboboxList>
          {(group: TargetGroup) => (
            <ComboboxGroup key={group.label} items={group.items}>
              <ComboboxLabel>{group.label}</ComboboxLabel>
              <ComboboxCollection>
                {(t: Target) => (
                  <ComboboxItem key={t.value} value={t}>
                    <TargetRow target={t} meta={t.container ? undefined : t.kind === "pod" ? t.containers.join(", ") : portsLabel(t.ports)} />
                  </ComboboxItem>
                )}
              </ComboboxCollection>
            </ComboboxGroup>
          )}
        </ComboboxList>
      </ComboboxContent>
    </Combobox>
  );
}
