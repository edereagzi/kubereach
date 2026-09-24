import { Fragment, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowsClockwiseIcon, EyeIcon, EyeSlashIcon } from "@phosphor-icons/react";
import type { Cluster } from "@bindings/internal/service";
import { CopyButton } from "@/components/copy-button";
import type { Target } from "@/components/targets";
import { Button } from "@/components/ui/button";
import { Inspector, InspectorDescription, InspectorHeader, InspectorTitle } from "@/components/inspector";
import { DetailTabs } from "@/components/yaml-view";
import { configObjectQuery, errorText } from "@/queries";
import { cn } from "@/lib/utils";

const keysLabel = (n: number) => `${n} key${n === 1 ? "" : "s"}`;

export function ConfigDetail({ cluster, target, onClose }: { cluster: Cluster; target: Target; onClose: () => void }) {
  const secret = target.kind === "secret";
  const q = useQuery(configObjectQuery(cluster.id, secret ? "secret" : "cm", target.namespace, target.name));
  const [revealed, setRevealed] = useState<string | null>(null);
  const o = q.data ?? target.config;
  const keys = o?.keys ?? [];
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
        <InspectorDescription>{[secret ? "Secret" : "ConfigMap", o?.type, keysLabel(keys.length)].filter(Boolean).join(" · ")}</InspectorDescription>
      </InspectorHeader>
      {q.error && <p className="text-xs text-destructive">{errorText(q.error)}</p>}
      <DetailTabs cluster={cluster} kind={target.kind} namespace={target.namespace} name={target.name}>
        {keys.length === 0 && !q.isPending && <p className="text-xs text-muted-foreground">No keys.</p>}
        <dl className="grid grid-cols-[fit-content(14rem)_1fr] gap-x-4 gap-y-1 text-xs">
          {keys.map((k) => {
            const value = q.data?.data?.[k];
            const shown = value !== undefined && (!secret || revealed === k);
            return (
              <Fragment key={k}>
                <dt className="truncate pt-0.5 font-mono text-muted-foreground" title={k}>
                  {k}
                </dt>
                <dd className="group flex min-w-0 items-start gap-1">
                  <pre className={cn("max-h-64 min-w-0 flex-1 overflow-auto rounded bg-muted/50 px-2 py-0.5 font-mono whitespace-pre-wrap wrap-anywhere", !shown && "text-muted-foreground")}>
                    {shown ? value : secret ? "••••••••" : "…"}
                  </pre>
                  {secret && value !== undefined && (
                    <Button variant="ghost" size="icon-xs" title={shown ? "Hide" : "Reveal"} onClick={() => setRevealed(shown ? null : k)}>
                      {shown ? <EyeSlashIcon /> : <EyeIcon />}
                    </Button>
                  )}
                  {value !== undefined && <CopyButton text={value} title="Copy value" />}
                </dd>
              </Fragment>
            );
          })}
        </dl>
      </DetailTabs>
    </Inspector>
  );
}
