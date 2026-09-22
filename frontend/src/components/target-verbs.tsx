import { useQuery } from "@tanstack/react-query";
import { State, type Cluster } from "@bindings/internal/service";
import { forwardsFor } from "@/components/forwards";
import { streamFor, useStartLogs } from "@/components/logs";
import { logKind, targetValue, type Target } from "@/components/targets";
import { OpenShell } from "@/components/terminal";
import { Button } from "@/components/ui/button";
import { configQuery } from "@/queries";
import { useUIStore, type ClusterTab } from "@/store";
import { cn } from "@/lib/utils";

// TargetVerbs is Forward, Logs and Shell for one object, each turning into a link to its tab once running, so a row and a detail say the same thing.
// On a row they stay out of sight until hovered; in a detail they are always there, and onLeave closes it when one of them moves to another tab.
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
  const verb = row ? "h-6 px-2 text-xs" : undefined;
  const quiet = row ? cn(verb, "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 focus-visible:opacity-100 aria-expanded:opacity-100") : undefined;
  const active = cn(verb, "text-primary hover:text-primary");
  const go = (tab: ClusterTab) => {
    selectTab(tab);
    onLeave?.();
  };

  return (
    <>
      {(target.kind === "svc" || target.kind === "pod") &&
        (forwarded ? (
          <Button variant={variant} size="xs" className={active} onClick={() => go("forwards")}>
            Forwarding
          </Button>
        ) : (
          <Button variant={variant} size="xs" className={quiet} onClick={onForward}>
            Forward
          </Button>
        ))}
      {logKind[target.kind] &&
        (following ? (
          <Button variant={variant} size="xs" className={active} onClick={() => go("logs")}>
            {stream.source.previous ? "Previous logs" : "Following logs"}
          </Button>
        ) : (
          <Button variant={variant} size="xs" className={quiet} disabled={startLogs.isPending} onClick={() => startLogs.mutate(target, { onSuccess: onLeave })}>
            Logs
          </Button>
        ))}
      {target.kind === "pod" &&
        (shell ? (
          <Button
            variant={variant}
            size="xs"
            className={active}
            onClick={() => {
              selectShell(shell.id);
              go("shell");
            }}
          >
            Shell open
          </Button>
        ) : (
          <OpenShell cluster={cluster} pods={[target]} variant={variant} size="xs" className={quiet} onStarted={onLeave} />
        ))}
    </>
  );
}
