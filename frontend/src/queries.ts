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

// main.go marshals service.ErrForbidden as {code: "forbidden"} on the error's cause.
export const isForbidden = (error: unknown) =>
  (error as { cause?: { code?: string } } | null)?.cause?.code === "forbidden";

export function reachabilityLabel(status: "pending" | "error" | "success", error: unknown, version?: string) {
  if (status === "pending") return "Checking…";
  if (status === "error") return String(error);
  return version ? `API reachable, ${version}` : "API reachable";
}
