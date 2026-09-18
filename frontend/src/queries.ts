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

// main.go marshals typed errors as {code, ...} on the error's cause.
type ErrorCause = { code?: string; target?: string };
const errorCause = (error: unknown) => (error as { cause?: ErrorCause } | null)?.cause;
export const isForbidden = (error: unknown) => errorCause(error)?.code === "forbidden";

// credentialRequired returns which session secret a Connect call is missing, if any.
export function credentialRequired(error: unknown): { code: "passphrase" | "password"; target: string } | null {
  const cause = errorCause(error);
  if (cause?.code === "passphrase" || cause?.code === "password") return { code: cause.code, target: cause.target ?? "" };
  return null;
}

export function reachabilityLabel(status: "pending" | "error" | "success", error: unknown, version?: string) {
  if (status === "pending") return "Checking…";
  if (status === "error") return String(error);
  return version ? `API reachable, ${version}` : "API reachable";
}
