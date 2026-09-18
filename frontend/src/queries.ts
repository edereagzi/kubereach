import { queryOptions } from "@tanstack/react-query";
import { ClusterService, ConfigService } from "@bindings/internal/bindings";

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

export const workloadsQuery = (clusterId: string) =>
  queryOptions({
    queryKey: ["cluster", clusterId, "workloads"],
    queryFn: async () => (await ClusterService.ListWorkloads(clusterId)) ?? [],
    retry: false,
  });

// main.go marshals typed errors as {code, ...} on the error's cause.
type ErrorCause = { code?: string; target?: string; port?: number; suggested?: number };
const errorCause = (error: unknown) => (error as { cause?: ErrorCause } | null)?.cause;
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
  if (status === "error") return String(error);
  return version ? `API reachable, ${version}` : "API reachable";
}
