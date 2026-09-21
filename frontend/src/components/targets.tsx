import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { MagnifyingGlassIcon } from "@phosphor-icons/react";
import { LogSourceKind, TargetKind, type Cluster, type NamedPort } from "@bindings/internal/service";
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
import { podsQuery, servicesQuery, workloadsQuery } from "@/queries";
import { cn } from "@/lib/utils";

export type Kind = "svc" | "deploy" | "sts" | "ds" | "pod";

// One row of anything a tab can act on; label is what the picker searches.
export type Target = {
  value: string;
  label: string;
  kind: Kind;
  namespace: string;
  name: string;
  ports: NamedPort[];
  containers: string[];
  // Set when the row stands for one container of a pod rather than the pod itself.
  container?: string;
};

export type TargetGroup = { label: string; items: Target[] };

const workloadKind: Partial<Record<LogSourceKind, Kind>> = { deployment: "deploy", statefulset: "sts", daemonset: "ds" };
export const logKind: Partial<Record<Kind, LogSourceKind>> = {
  deploy: LogSourceKind.LogSourceDeployment,
  sts: LogSourceKind.LogSourceStatefulSet,
  ds: LogSourceKind.LogSourceDaemonSet,
  pod: LogSourceKind.LogSourcePod,
};
export const forwardKind = (kind: Kind) => (kind === "svc" ? TargetKind.TargetService : TargetKind.TargetPod);
export const targetValue = (kind: Kind, namespace: string, name: string) => `${kind}:${namespace}/${name}`;

export function useTargets(cluster: Cluster) {
  const services = useQuery(servicesQuery(cluster.id));
  const workloads = useQuery(workloadsQuery(cluster.id));
  const pods = useQuery(podsQuery(cluster.id));
  const make = (kind: Kind, namespace: string, name: string, ports: NamedPort[] = [], containers: string[] = []): Target => ({
    value: targetValue(kind, namespace, name),
    label: `${namespace}/${name}`,
    kind,
    namespace,
    name,
    ports,
    containers,
  });
  const groups: TargetGroup[] = [
    { label: "Services", items: (services.data ?? []).map((s) => make("svc", s.namespace, s.name, s.ports ?? [])) },
    { label: "Workloads", items: (workloads.data ?? []).map((w) => make(workloadKind[w.kind] ?? "deploy", w.namespace, w.name)) },
    { label: "Pods", items: (pods.data ?? []).map((p) => make("pod", p.namespace, p.name, p.ports ?? [], p.containers ?? [])) },
  ];
  return { groups, error: services.error ?? workloads.error ?? pods.error, pending: services.isPending || workloads.isPending || pods.isPending };
}

export function KindBadge({ kind, className }: { kind: Kind; className?: string }) {
  return (
    <span className={cn("inline-flex h-[18px] w-11 shrink-0 items-center justify-center rounded bg-muted font-mono text-[11px] font-medium text-muted-foreground", className)}>
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
      <ComboboxContent className={cn(!inline && "w-96")}>
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
                    <TargetRow target={t} meta={t.kind === "pod" ? t.containers.join(", ") : portsLabel(t.ports)} />
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
