import { useQuery } from "@tanstack/react-query";
import { ArrowsLeftRightIcon, CubeIcon, ScrollIcon } from "@phosphor-icons/react";
import type { Cluster } from "@bindings/internal/service";
import { forwardsFor } from "@/components/forwards";
import { streamOf, targetSource, useStartLogs } from "@/components/logs";
import { logKind, targetValue, type Target } from "@/components/targets";
import { OpenShell } from "@/components/terminal";
import { Button } from "@/components/ui/button";
import { configQuery, errorText } from "@/queries";
import { sessionEnded, useUIStore } from "@/store";

// TargetVerbs is Forward, Logs and Shell for one object, each turning into a link to its tab once running, so a row and a detail say the same thing.
// A row shows only what is running, as the sidebar's icons so a narrow list keeps its text, since its detail beside the list holds the verbs;
// onLeave closes a detail when one of them moves to another view.
// Logs and shells open in the dock under the view, so the detail stays open beside them.
export function TargetVerbs({ cluster, target, onForward, row = false, onLeave }: { cluster: Cluster; target: Target; onForward?: () => void; row?: boolean; onLeave?: () => void }) {
  const { data } = useQuery(configQuery);
  const selectTab = useUIStore((s) => s.selectTab);
  const openDock = useUIStore((s) => s.openDock);
  const forwarded = forwardsFor(data?.forwards, cluster).some(
    (f) => targetValue(f.target.kind === "service" ? "svc" : "pod", f.target.namespace, f.target.name) === target.value,
  );
  // The verb stands for the target's live logs; a previous run or another container opened elsewhere is its own tab.
  const stream = useUIStore((s) => streamOf(s.logStreams, targetSource(cluster, target)));
  const shell = useUIStore((s) =>
    Object.values(s.shellSessions).find(
      (x) => x.target.clusterId === cluster.id && x.target.namespace === target.namespace && x.target.pod === target.name && !sessionEnded(x),
    ),
  );
  const startLogs = useStartLogs(cluster);
  const variant = row ? "ghost" : "outline";
  const size = row ? "icon-xs" : "xs";
  const active = "text-primary hover:text-primary";

  return (
    <>
      {(target.kind === "svc" || target.kind === "pod") &&
        (forwarded ? (
          <Button
            variant={variant}
            size={size}
            className={active}
            title="Forwarding"
            onClick={() => {
              selectTab("forwards");
              onLeave?.();
            }}
          >
            {row ? <ArrowsLeftRightIcon /> : "Forwarding"}
          </Button>
        ) : (
          !row && (
            <Button variant={variant} size="xs" onClick={onForward}>
              Forward
            </Button>
          )
        ))}
      {logKind[target.kind] &&
        (stream ? (
          <Button variant={variant} size={size} className={active} title="Following logs" onClick={() => openDock(cluster.id, stream.id)}>
            {row ? <ScrollIcon /> : "Following logs"}
          </Button>
        ) : (
          !row && (
            <>
              <Button variant={variant} size="xs" disabled={startLogs.isPending} onClick={() => startLogs.mutate(target)}>
                Logs
              </Button>
              {startLogs.error && (
                <span className="my-auto min-w-0 truncate text-xs text-destructive" title={errorText(startLogs.error)}>
                  {errorText(startLogs.error)}
                </span>
              )}
            </>
          )
        ))}
      {target.kind === "pod" &&
        (shell ? (
          <Button
            variant={variant}
            size={size}
            className={active}
            title="Shell open"
            onClick={() => openDock(cluster.id, shell.id)}
          >
            {row ? <CubeIcon /> : "Shell open"}
          </Button>
        ) : (
          !row && <OpenShell cluster={cluster} pods={[target]} variant={variant} size="xs" />
        ))}
    </>
  );
}
