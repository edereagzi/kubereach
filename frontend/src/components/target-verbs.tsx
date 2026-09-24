import { useQuery } from "@tanstack/react-query";
import { ArrowsLeftRightIcon, ScrollIcon, TerminalWindowIcon } from "@phosphor-icons/react";
import { State, type Cluster } from "@bindings/internal/service";
import { forwardsFor } from "@/components/forwards";
import { streamFor, useStartLogs } from "@/components/logs";
import { logKind, targetValue, type Target } from "@/components/targets";
import { OpenShell } from "@/components/terminal";
import { Button } from "@/components/ui/button";
import { configQuery } from "@/queries";
import { useUIStore, type ClusterTab } from "@/store";

// TargetVerbs is Forward, Logs and Shell for one object, each turning into a link to its tab once running, so a row and a detail say the same thing.
// A row shows only what is running, as the sidebar's icons so a narrow list keeps its text, since its detail beside the list holds the verbs;
// onLeave closes a detail when one of them moves to another view.
// Logs and shells open in the dock under the view, so the detail stays open beside them.
export function TargetVerbs({ cluster, target, onForward, row = false, onLeave }: { cluster: Cluster; target: Target; onForward?: () => void; row?: boolean; onLeave?: () => void }) {
  const { data } = useQuery(configQuery);
  const selectTab = useUIStore((s) => s.selectTab);
  const selectShell = useUIStore((s) => s.selectShell);
  const forwarded = forwardsFor(data?.forwards, cluster).some(
    (f) => targetValue(f.target.kind === "service" ? "svc" : "pod", f.target.namespace, f.target.name) === target.value,
  );
  const stream = useUIStore((s) => streamFor(s.logStreams, cluster));
  const following =
    !!stream && stream.source.kind === logKind[target.kind] && stream.source.namespace === target.namespace && stream.source.name === target.name;
  const shell = useUIStore((s) =>
    Object.values(s.shellSessions).find(
      (x) => x.target.clusterId === cluster.id && x.target.namespace === target.namespace && x.target.pod === target.name && x.state !== State.StateStopped && x.state !== State.StateError,
    ),
  );
  const startLogs = useStartLogs(cluster);
  const variant = row ? "ghost" : "outline";
  const size = row ? "icon-xs" : "xs";
  const active = "text-primary hover:text-primary";
  const go = (tab: ClusterTab) => {
    selectTab(tab);
    if (tab !== "logs" && tab !== "shell") onLeave?.();
  };

  return (
    <>
      {(target.kind === "svc" || target.kind === "pod") &&
        (forwarded ? (
          <Button variant={variant} size={size} className={active} title="Forwarding" onClick={() => go("forwards")}>
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
        (following ? (
          <Button variant={variant} size={size} className={active} title={stream.source.previous ? "Previous logs" : "Following logs"} onClick={() => go("logs")}>
            {row ? <ScrollIcon /> : stream.source.previous ? "Previous logs" : "Following logs"}
          </Button>
        ) : (
          !row && (
            <Button variant={variant} size="xs" disabled={startLogs.isPending} onClick={() => startLogs.mutate(target)}>
              Logs
            </Button>
          )
        ))}
      {target.kind === "pod" &&
        (shell ? (
          <Button
            variant={variant}
            size={size}
            className={active}
            title="Shell open"
            onClick={() => {
              selectShell(shell.id);
              go("shell");
            }}
          >
            {row ? <TerminalWindowIcon /> : "Shell open"}
          </Button>
        ) : (
          !row && <OpenShell cluster={cluster} pods={[target]} variant={variant} size="xs" />
        ))}
    </>
  );
}
