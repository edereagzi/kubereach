import { queryOptions } from "@tanstack/react-query";
import { ClusterService, ConfigService } from "@bindings/internal/bindings";
import type { NodeMetrics, ObjectKind, PodMetrics, PodUsage, ResourceUsage, WorkloadKind } from "@bindings/internal/service";

export const configQuery = queryOptions({
  queryKey: ["config"],
  queryFn: () => ConfigService.Load(),
});

export const reachabilityQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "reachability"],
    queryFn: () => ClusterService.CheckReachability(clusterId),
    staleTime: Infinity,
    retry: false,
  });

export const namespacesQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "namespaces"],
    queryFn: async () => (await ClusterService.ListNamespaces(clusterId)) ?? [],
    retry: false,
  });

export const servicesQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "services"],
    queryFn: async () => (await ClusterService.ListServices(clusterId)) ?? [],
    retry: false,
  });

export const podsQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "pods"],
    queryFn: async () => (await ClusterService.ListPods(clusterId)) ?? [],
    retry: false,
  });

// metrics-server scrapes kubelets every 15s by default, so asking more often returns the same numbers.
const metricsInterval = 15_000;

// A Cluster without metrics-server yields an empty map, and the usage columns stay out.
export const podUsageKey = (namespace: string, name: string) => `${namespace}/${name}`;
const indexPodMetrics = (m: PodMetrics) => new Map((m.pods ?? []).map((p): [string, PodUsage] => [podUsageKey(p.namespace, p.name), p]));
export const podMetricsQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "pod-metrics"],
    queryFn: () => ClusterService.PodMetrics(clusterId),
    select: indexPodMetrics,
    refetchInterval: metricsInterval,
    retry: false,
  });

export const podQuery = (clusterId: string, namespace: string, name: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "pod", namespace, name],
    queryFn: () => ClusterService.DescribePod(clusterId, namespace, name),
    retry: false,
  });

export const workloadQuery = (clusterId: string, kind: WorkloadKind, namespace: string, name: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "workload", kind, namespace, name],
    queryFn: () => ClusterService.DescribeWorkload(clusterId, kind, namespace, name),
    retry: false,
  });

export const configMapsQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "configmaps"],
    queryFn: async () => (await ClusterService.ListConfigMaps(clusterId)) ?? [],
    retry: false,
  });

export const secretsQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "secrets"],
    queryFn: async () => (await ClusterService.ListSecrets(clusterId)) ?? [],
    retry: false,
  });

// Secret values are held only in this query's cache and dropped as soon as the detail closes.
export const configObjectQuery = (clusterId: string, kind: "cm" | "secret", namespace: string, name: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, kind, namespace, name],
    queryFn: () => (kind === "secret" ? ClusterService.GetSecret : ClusterService.GetConfigMap)(clusterId, namespace, name),
    retry: false,
    gcTime: kind === "secret" ? 0 : undefined,
  });

export const ingressesQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "ingresses"],
    queryFn: async () => (await ClusterService.ListIngresses(clusterId)) ?? [],
    retry: false,
  });

export const ingressQuery = (clusterId: string, namespace: string, name: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "ingress", namespace, name],
    queryFn: () => ClusterService.DescribeIngress(clusterId, namespace, name),
    retry: false,
  });

export const pvcsQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "pvcs"],
    queryFn: async () => (await ClusterService.ListPVCs(clusterId)) ?? [],
    retry: false,
  });

export const pvcQuery = (clusterId: string, namespace: string, name: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "pvc", namespace, name],
    queryFn: () => ClusterService.DescribePVC(clusterId, namespace, name),
    retry: false,
  });

export const nodesQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "nodes"],
    queryFn: async () => (await ClusterService.ListNodes(clusterId)) ?? [],
    retry: false,
  });

// A Cluster without metrics-server yields an empty map, and the used part of every node bar stays out.
const indexNodeMetrics = (m: NodeMetrics) => new Map((m.nodes ?? []).map((n): [string, ResourceUsage] => [n.name, n.usage]));
export const nodeMetricsQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "node-metrics"],
    queryFn: () => ClusterService.NodeMetrics(clusterId),
    select: indexNodeMetrics,
    refetchInterval: metricsInterval,
    retry: false,
  });

export const nodeQuery = (clusterId: string, name: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "node", name],
    queryFn: () => ClusterService.DescribeNode(clusterId, name),
    retry: false,
  });

export const workloadsQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "workloads"],
    queryFn: async () => (await ClusterService.ListWorkloads(clusterId)) ?? [],
    retry: false,
  });

// main.go marshals every error as {message, code, ...} on its cause; the message is the one worded for the user.
type ErrorCause = { message?: string; code?: string; target?: string; port?: number; suggested?: number };
const errorCause = (error: unknown) => (error as { cause?: ErrorCause } | null)?.cause;
export const errorText = (error: unknown) => errorCause(error)?.message ?? (error instanceof Error ? error.message : String(error));
export const isForbidden = (error: unknown) => errorCause(error)?.code === "forbidden";

// credentialRequired returns which session secret a Connect call is missing, if any.
export function credentialRequired(error: unknown): { code: "passphrase" | "password"; target: string } | null {
  const cause = errorCause(error);
  if (cause?.code === "passphrase" || cause?.code === "password") return { code: cause.code, target: cause.target ?? "" };
  return null;
}

// portInUse returns the free port suggested when a forward's local port was busy, if that is why it failed.
export function portInUse(error: unknown): { port: number; suggested: number } | null {
  const cause = errorCause(error);
  if (cause?.code === "port-in-use" && cause.port && cause.suggested) return { port: cause.port, suggested: cause.suggested };
  return null;
}

export function reachabilityLabel(status: "pending" | "error" | "success", error: unknown, version?: string) {
  if (status === "pending") return "Checking…";
  if (status === "error") return errorText(error);
  return version ? `API reachable, ${version}` : "API reachable";
}

// A revealed Secret's YAML is held only while shown; masked YAML caches like any other detail.
export const yamlQuery = (clusterId: string, kind: ObjectKind, namespace: string, name: string, reveal: boolean) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "yaml", kind, namespace, name, reveal],
    queryFn: () => ClusterService.GetYAML(clusterId, kind, namespace, name, reveal),
    retry: false,
    gcTime: reveal ? 0 : undefined,
  });
