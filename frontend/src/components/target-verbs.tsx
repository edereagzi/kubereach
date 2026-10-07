import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowsLeftRightIcon, CubeIcon, ScrollIcon, TerminalIcon } from "@phosphor-icons/react";
import type { Cluster } from "@bindings/internal/service";
import { forwardsFor } from "@/components/forwards";
import { useStartLogs } from "@/components/logs";
import { logKind, targetValue, type Kind, type Target } from "@/components/targets";
import { OpenShell } from "@/components/terminal";
import { Button } from "@/components/ui/button";
import { configQuery, errorText } from "@/queries";
import { sessionEnded, useUIStore } from "@/store";

// TargetVerbs is Forward, Logs and Shell for one object in its detail, each turning into a link to its tab once running,
// as its row's RowVerbs say. onLeave closes the detail when one of them moves to another view.
// Logs and shells open in the dock under the view, so the detail stays open beside them.
export function TargetVerbs({ cluster, target, onForward, onLeave }: { cluster: Cluster; target: Target; onForward?: () => void; onLeave?: () => void }) {
  const selectTab = useUIStore((s) => s.selectTab);
  const openDock = useUIStore((s) => s.openDock);
  const { forwarded, stream, shell } = useRowFacts(cluster).get(target.value) ?? {};
  const startLogs = useStartLogs(cluster);
  const active = "text-primary hover:text-primary";

  return (
    <>
      {(target.kind === "svc" || target.kind === "pod") &&
        (forwarded ? (
          <Button
            variant="outline"
            size="xs"
            className={active}
            title="Forwarding"
            onClick={() => {
              selectTab("forwards");
              onLeave?.();
            }}
          >
            <ArrowsLeftRightIcon />
            Forward
          </Button>
        ) : (
          <Button variant="outline" size="xs" onClick={onForward}>
            <ArrowsLeftRightIcon />
            Forward
          </Button>
        ))}
      {logKind[target.kind] &&
        (stream ? (
          <Button variant="outline" size="xs" className={active} title="Following logs" onClick={() => openDock(cluster.id, stream)}>
            <ScrollIcon />
            Logs
          </Button>
        ) : (
          <>
            <Button variant="outline" size="xs" disabled={startLogs.isPending} onClick={() => startLogs.mutate(target)}>
              <ScrollIcon />
              Logs
            </Button>
            {startLogs.error && (
              <span className="my-auto min-w-0 truncate text-xs text-destructive" title={errorText(startLogs.error)}>
                {errorText(startLogs.error)}
              </span>
            )}
          </>
        ))}
      {target.kind === "pod" &&
        (shell ? (
          <Button variant="outline" size="xs" className={active} title="Shell open" onClick={() => openDock(cluster.id, shell)}>
            <TerminalIcon />
            Shell
          </Button>
        ) : (
          <OpenShell cluster={cluster} pods={[target]} variant="outline" size="xs" />
        ))}
    </>
  );
}

// RowFacts are what an object's verbs show: whether it is forwarded, and the session of its live logs or of an open Shell.
export type RowFacts = { forwarded?: boolean; stream?: string; shell?: string };
const logRowKind = Object.fromEntries(Object.entries(logKind).map(([kind, source]) => [source, kind as Kind]));

// useRowFacts works out the facts of every object of a Cluster at once, by row value, so a list of thousands reads them
// once rather than once a row.
export function useRowFacts(cluster: Cluster) {
  const { data } = useQuery(configQuery);
  const logStreams = useUIStore((s) => s.logStreams);
  const shellSessions = useUIStore((s) => s.shellSessions);
  return useMemo(() => {
    const facts = new Map<string, RowFacts>();
    const add = (value: string, f: RowFacts) => facts.set(value, { ...f, ...facts.get(value) });
    for (const f of forwardsFor(data?.forwards, cluster)) add(targetValue(f.target.kind === "service" ? "svc" : "pod", f.target.namespace, f.target.name), { forwarded: true });
    // The verb stands for the target's live logs; a previous run or another container opened elsewhere is its own tab.
    for (const st of Object.values(logStreams)) {
      const kind = logRowKind[st.source.kind];
      if (kind && st.source.clusterId === cluster.id && !st.source.container && !st.source.previous) add(targetValue(kind, st.source.namespace, st.source.name), { stream: st.id });
    }
    for (const x of Object.values(shellSessions)) {
      if (x.target.clusterId === cluster.id && !sessionEnded(x)) add(targetValue("pod", x.target.namespace, x.target.pod), { shell: x.id });
    }
    return facts;
  }, [data?.forwards, cluster, logStreams, shellSessions]);
}

// RowVerbs shows only what is running, as the sidebar's icons so a narrow list keeps its text, since the detail beside
// the list holds the verbs. It reads nothing itself, so a list of thousands holds no subscription per row.
export function RowVerbs({ facts, onForwards, onOpen }: { facts?: RowFacts; onForwards: () => void; onOpen: (sessionId: string) => void }) {
  const active = "text-primary hover:text-primary";
  return (
    <>
      {facts?.forwarded && (
        <Button variant="ghost" size="icon-xs" className={active} title="Forwarding" onClick={onForwards}>
          <ArrowsLeftRightIcon />
        </Button>
      )}
      {facts?.stream && (
        <Button variant="ghost" size="icon-xs" className={active} title="Following logs" onClick={() => onOpen(facts.stream!)}>
          <ScrollIcon />
        </Button>
      )}
      {facts?.shell && (
        <Button variant="ghost" size="icon-xs" className={active} title="Shell open" onClick={() => onOpen(facts.shell!)}>
          <CubeIcon />
        </Button>
      )}
    </>
  );
}
