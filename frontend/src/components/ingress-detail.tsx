import { useQuery } from "@tanstack/react-query";
import { Browser } from "@wailsio/runtime";
import { ArrowRightIcon } from "@phosphor-icons/react";
import type { Cluster, IngressPath } from "@bindings/internal/service";
import { DeleteAction } from "@/components/actions";
import { ReasonBadge, Section } from "@/components/pod-detail";
import type { Target } from "@/components/targets";
import { RefreshButton } from "@/components/refresh-button";
import { Inspector, InspectorDescription, InspectorHeader, InspectorName, InspectorTitle } from "@/components/inspector";
import { DetailTabs } from "@/components/yaml-view";
import { ingressQuery, errorText } from "@/queries";
import { useUIStore } from "@/store";
import { cn } from "@/lib/utils";

// A row says the first host and counts the rest; every host, path and backend is in this detail.
export function hostsLabel(hosts?: string[] | null) {
  const [first, ...rest] = hosts ?? [];
  if (!first) return "";
  return rest.length ? `${first} +${rest.length} host${rest.length === 1 ? "" : "s"}` : first;
}

// The default backend has neither host nor path: it is what the Ingress falls back to, not a URL to match.
const isFallback = (p: IngressPath) => !p.host && !p.path;
// A path opens in the browser when it is one address: a named host and a plain path, not a wildcard or a regex.
const urlOf = (p: IngressPath) =>
  p.host && !p.host.includes("*") && /^\/[\w\-./~%]*$/.test(p.path || "/") ? `${p.tls ? "https" : "http"}://${p.host}${p.path || "/"}` : null;

// Rules are grouped by host, said once as the heading, and paths that reach the same backend share a row,
// so each Service and its pods are listed once however many paths lead to them.
type Route = { paths: IngressPath[]; backend: IngressPath };
function byHost(paths: IngressPath[]) {
  const hosts = new Map<string, Route[]>();
  for (const p of paths) {
    const host = isFallback(p) ? "Default backend" : p.host || "Any host";
    const routes = hosts.get(host) ?? [];
    const same = routes.find((r) => r.backend.service === p.service && r.backend.port === p.port && r.backend.problem === p.problem);
    if (same) same.paths.push(p);
    else routes.push({ paths: [p], backend: p });
    hosts.set(host, routes);
  }
  return [...hosts];
}

export function IngressDetail({ cluster, target, onClose }: { cluster: Cluster; target: Target; onClose: () => void }) {
  const q = useQuery(ingressQuery(cluster.id, target.namespace, target.name));
  const requestInspect = useUIStore((s) => s.requestInspect);
  const paths = q.data?.paths ?? [];
  return (
    <Inspector onClose={onClose}>
      <InspectorHeader>
        <InspectorTitle className="flex items-center gap-2 pr-8">
          <InspectorName namespace={target.namespace} name={target.name} />
          <RefreshButton fetching={q.isFetching} onRefresh={() => q.refetch()} />
        </InspectorTitle>
        <InspectorDescription>Ingress</InspectorDescription>
        <div className="flex flex-wrap gap-1.5">
          <span className="ml-auto">
            <DeleteAction cluster={cluster} target={target} onDone={onClose} />
          </span>
        </div>
      </InspectorHeader>
      {q.error && <p className="text-xs text-destructive">{errorText(q.error)}</p>}
      <DetailTabs cluster={cluster} kind="ing" namespace={target.namespace} name={target.name}>
        {paths.length === 0 && !q.isPending && <p className="text-xs text-muted-foreground">This Ingress has no rules.</p>}
        {byHost(paths).map(([host, routes]) => (
          <Section key={host} title={host}>
            <ul className="divide-y text-xs">
              {routes.map(({ paths, backend: b }, i) => (
                <li key={i} className="grid grid-cols-[minmax(0,2fr)_auto_minmax(0,3fr)] items-start gap-x-2 py-2">
                  <div className="flex min-w-0 flex-wrap gap-x-3 gap-y-1 font-mono">
                    {paths.map((p) => {
                      const url = urlOf(p);
                      const label = isFallback(p) ? "anything no rule matches" : p.path || "/";
                      return url ? (
                        <button key={label} type="button" title={`Open ${url}`} className="max-w-full truncate text-left underline decoration-muted-foreground/40 underline-offset-3 hover:decoration-foreground" onClick={() => Browser.OpenURL(url)}>
                          {label}
                        </button>
                      ) : (
                        <span key={label} className={cn("max-w-full truncate", isFallback(p) && "font-sans text-muted-foreground")} title={label}>
                          {label}
                        </span>
                      );
                    })}
                  </div>
                  <ArrowRightIcon className="mt-0.5 size-3 text-muted-foreground" />
                  <div className="flex min-w-0 flex-col items-start gap-1">
                    <div className="flex w-full min-w-0 items-center gap-2">
                      <span className="truncate font-mono text-muted-foreground">{b.service ? `${b.service}:${b.port}` : "no backend"}</span>
                      {b.unknown ? <span className="ml-auto shrink-0 text-muted-foreground">{b.problem}</span> : <ReasonBadge reason={b.problem} className="ml-auto shrink-0" />}
                    </div>
                    {b.pods?.map((pod) => (
                      <button
                        key={pod.name}
                        type="button"
                        className="flex max-w-full min-w-0 items-center gap-1.5 text-left font-mono hover:underline underline-offset-2"
                        title={`Why is ${pod.name} in this state?`}
                        onClick={() => requestInspect({ clusterId: cluster.id, kind: "pod", namespace: pod.namespace, name: pod.name })}
                      >
                        <span className={cn("size-1.5 shrink-0 rounded-full", pod.ready ? "bg-green-500" : "bg-destructive")} />
                        <span className="truncate">{pod.name}</span>
                        {!pod.ready && <span className="shrink-0 text-destructive">not ready</span>}
                      </button>
                    ))}
                  </div>
                </li>
              ))}
            </ul>
          </Section>
        ))}
      </DetailTabs>
    </Inspector>
  );
}
