import { useQuery } from "@tanstack/react-query";
import { ArrowRightIcon, ArrowsClockwiseIcon } from "@phosphor-icons/react";
import type { Cluster, IngressPath } from "@bindings/internal/service";
import { ReasonBadge, Section } from "@/components/pod-detail";
import type { Target } from "@/components/targets";
import { Button } from "@/components/ui/button";
import { Inspector, InspectorDescription, InspectorHeader, InspectorTitle } from "@/components/inspector";
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

// What a browser would ask for: a rule without a host matches any, a rule without a path matches all of them.
// The default backend has neither — it is what the Ingress falls back to, not a URL to match, so it says so.
const isFallback = (p: IngressPath) => !p.host && !p.path;
const pathLabel = (p: IngressPath) => (isFallback(p) ? "default backend" : `${p.host || "*"}${p.path || "/*"}`);

export function IngressDetail({ cluster, target, onClose }: { cluster: Cluster; target: Target; onClose: () => void }) {
  const q = useQuery(ingressQuery(cluster.id, target.namespace, target.name));
  const requestInspect = useUIStore((s) => s.requestInspect);
  const ing = q.data?.ingress ?? target.ingress;
  const paths = q.data?.paths ?? [];
  return (
    <Inspector onClose={onClose}>
      <InspectorHeader>
        <InspectorTitle className="flex items-center gap-2 pr-8">
          <span className="truncate">
            {target.namespace}/{target.name}
          </span>
          <Button variant="ghost" size="icon-sm" title="Refresh" disabled={q.isFetching} onClick={() => q.refetch()}>
            <ArrowsClockwiseIcon className={cn(q.isFetching && "animate-spin")} />
          </Button>
        </InspectorTitle>
        <InspectorDescription>{["Ingress", ing?.hosts?.join(", ")].filter(Boolean).join(" · ")}</InspectorDescription>
      </InspectorHeader>
      {q.error && <p className="text-xs text-destructive">{errorText(q.error)}</p>}
      <DetailTabs cluster={cluster} kind="ing" namespace={target.namespace} name={target.name}>
        <Section title="Paths">
          {paths.length === 0 && !q.isPending && <p className="text-xs text-muted-foreground">This Ingress has no rules.</p>}
          {paths.map((p, i) => (
            <div key={i} className="rounded-md border px-3 py-2 text-xs">
              <div className="flex items-center gap-2">
                <span className={cn("truncate font-medium", !isFallback(p) && "font-mono")} title={pathLabel(p)}>
                  {pathLabel(p)}
                </span>
                <ArrowRightIcon className="size-3 shrink-0 text-muted-foreground" />
                <span className="truncate font-mono text-muted-foreground">{p.service ? `${p.service}:${p.port}` : "no backend"}</span>
                {p.unknown ? (
                  <span className="ml-auto shrink-0 text-muted-foreground">{p.problem}</span>
                ) : (
                  <ReasonBadge reason={p.problem} className="ml-auto shrink-0" />
                )}
              </div>
              {!!p.pods?.length && (
                <div className="mt-1.5 flex flex-wrap gap-1">
                  {p.pods.map((pod) => (
                    <Button
                      key={pod.name}
                      variant="outline"
                      size="xs"
                      className="h-5 min-w-0 max-w-full font-mono text-xs"
                      title={`Why is ${pod.name} in this state?`}
                      onClick={() => requestInspect({ clusterId: cluster.id, kind: "pod", namespace: pod.namespace, name: pod.name })}
                    >
                      <span className="truncate">{pod.name}</span>
                      {!pod.ready && <span className="shrink-0 text-destructive">· not ready</span>}
                    </Button>
                  ))}
                </div>
              )}
            </div>
          ))}
        </Section>
      </DetailTabs>
    </Inspector>
  );
}
