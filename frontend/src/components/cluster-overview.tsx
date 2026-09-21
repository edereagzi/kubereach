import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { MagnifyingGlassIcon } from "@phosphor-icons/react";
import { ClusterService } from "@bindings/internal/bindings";
import { State, type Cluster } from "@bindings/internal/service";
import { AddForward, forwardsFor } from "@/components/forwards";
import { streamFor, useStartLogs } from "@/components/logs";
import { KindBadge, logKind, portsLabel, targetValue, useTargets, type Target, type TargetGroup } from "@/components/targets";
import { OpenShell } from "@/components/terminal";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { configQuery, isForbidden, namespacesQuery } from "@/queries";
import { useUIStore } from "@/store";
import { cn } from "@/lib/utils";

type Filter = "all" | "Services" | "Workloads" | "Pods";

export function ClusterOverview({ cluster }: { cluster: Cluster }) {
  const namespaces = useQuery(namespacesQuery(cluster.id));
  const { data: config } = useQuery(configQuery);
  const { groups, error: listError, pending } = useTargets(cluster);
  const [editing, setEditing] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");
  const [needle, setNeedle] = useState("");
  const [forwarding, setForwarding] = useState<Target | null>(null);
  const search = useRef<HTMLInputElement>(null);
  const explicit = cluster.namespaces ?? [];

  // "/" jumps to the filter from anywhere on the tab that is not already typing.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "/" && !(e.target instanceof HTMLElement && e.target.closest("input, textarea, [contenteditable], [role=dialog]"))) {
        e.preventDefault();
        search.current?.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  if (editing || isForbidden(namespaces.error) || isForbidden(listError)) {
    return <NamespacePrompt cluster={cluster} forbidden={!editing} onDone={() => setEditing(false)} />;
  }
  const error = namespaces.error ?? listError;
  if (error) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyTitle>Cluster could not be listed</EmptyTitle>
          <EmptyDescription>{String(error)}</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  const lower = needle.trim().toLowerCase();
  const shown = groups
    .filter((g) => filter === "all" || g.label === filter)
    .map((g) => ({ ...g, items: g.items.filter((t) => !lower || t.label.toLowerCase().includes(lower)) }));
  const byNamespace = new Map<string, TargetGroup[]>();
  for (const ns of namespaces.data ?? []) byNamespace.set(ns, []);
  for (const g of shown) {
    for (const t of g.items) {
      const list = byNamespace.get(t.namespace) ?? [];
      const own = list.find((x) => x.label === g.label) ?? (list.push({ label: g.label, items: [] }), list.at(-1)!);
      own.items.push(t);
      byNamespace.set(t.namespace, list);
    }
  }
  const total = shown.reduce((n, g) => n + g.items.length, 0);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {forwarding && (
        <AddForward cluster={cluster} saved={forwardsFor(config?.forwards, cluster)} initial={forwarding} onClose={() => setForwarding(null)} />
      )}
      <div className="flex flex-wrap items-center gap-2 px-4 py-2.5">
        <InputGroup className="w-auto min-w-48 flex-1">
          <InputGroupInput ref={search} placeholder="Filter services, workloads and pods" value={needle} onChange={(e) => setNeedle(e.target.value)} />
          <InputGroupAddon>
            <MagnifyingGlassIcon />
          </InputGroupAddon>
          <InputGroupAddon align="inline-end">
            <kbd className="rounded border px-1 font-sans text-[10px] text-muted-foreground">/</kbd>
          </InputGroupAddon>
        </InputGroup>
        <ToggleGroup value={[filter]} onValueChange={(v) => setFilter((v[0] as Filter) ?? "all")} variant="outline" size="sm" spacing={0}>
          {(["all", "Services", "Workloads", "Pods"] as const).map((f) => (
            <ToggleGroupItem key={f} value={f}>
              {f === "all" ? "All" : f}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={() => setEditing(true)}>
          {explicit.length ? explicit.join(", ") : "All namespaces"}
          <span className="text-muted-foreground/70">· Edit scope</span>
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-auto pb-4">
        {total === 0 && !pending && (
          <Empty className="border-0">
            <EmptyHeader>
              <EmptyTitle>{lower ? `Nothing matches “${needle.trim()}”` : "Nothing to show"}</EmptyTitle>
              <EmptyDescription>{lower ? "Try a shorter name." : "This scope has no services, workloads or pods."}</EmptyDescription>
            </EmptyHeader>
          </Empty>
        )}
        {[...byNamespace].map(([ns, kinds]) =>
          kinds.length === 0 ? null : (
            <section key={ns}>
              <h3 className="sticky top-0 z-10 flex items-baseline gap-2 bg-background px-4 pt-3 pb-1 text-sm font-medium">
                {ns}
                <span className="text-xs font-normal text-muted-foreground">
                  {kinds.map((g) => `${g.items.length} ${g.label.toLowerCase().replace(/s$/, g.items.length === 1 ? "" : "s")}`).join(" · ")}
                </span>
              </h3>
              {kinds.flatMap((g) => g.items).map((t) => (
                <TargetLine key={t.value} cluster={cluster} target={t} onForward={() => setForwarding(t)} />
              ))}
            </section>
          ),
        )}
      </div>
    </div>
  );
}

const meta = (t: Target) => {
  if (t.kind === "svc") return portsLabel(t.ports);
  if (t.kind === "pod") return [t.containers.join(", "), portsLabel(t.ports)].filter(Boolean).join(" · ");
  return "";
};

function TargetLine({ cluster, target, onForward }: { cluster: Cluster; target: Target; onForward: () => void }) {
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
  const verb = "h-6 px-2 text-xs";
  const quiet = cn(verb, "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 focus-visible:opacity-100 aria-expanded:opacity-100");
  const active = cn(verb, "text-primary hover:text-primary");

  return (
    <div className="group grid h-8 grid-cols-[44px_minmax(160px,240px)_minmax(0,1fr)_auto] items-center gap-3 px-4 hover:bg-accent focus-within:bg-accent">
      <KindBadge kind={target.kind} />
      <span className="truncate" title={target.name}>
        {target.name}
      </span>
      <span className="truncate font-mono text-xs text-muted-foreground">{meta(target)}</span>
      <span className="flex justify-end gap-0.5">
        {(target.kind === "svc" || target.kind === "pod") &&
          (forwarded ? (
            <Button variant="ghost" size="xs" className={active} onClick={() => selectTab("forwards")}>
              Forwarding
            </Button>
          ) : (
            <Button variant="ghost" size="xs" className={quiet} onClick={onForward}>
              Forward
            </Button>
          ))}
        {target.kind !== "svc" &&
          (following ? (
            <Button variant="ghost" size="xs" className={active} onClick={() => selectTab("logs")}>
              Following logs
            </Button>
          ) : (
            <Button variant="ghost" size="xs" className={quiet} disabled={startLogs.isPending} onClick={() => startLogs.mutate(target)}>
              Logs
            </Button>
          ))}
        {target.kind === "pod" &&
          (shell ? (
            <Button
              variant="ghost"
              size="xs"
              className={active}
              onClick={() => {
                selectShell(shell.id);
                selectTab("shell");
              }}
            >
              Shell open
            </Button>
          ) : (
            <OpenShell cluster={cluster} pods={[target]} variant="ghost" size="xs" className={quiet} />
          ))}
      </span>
    </div>
  );
}

function NamespacePrompt({ cluster, forbidden, onDone }: { cluster: Cluster; forbidden: boolean; onDone: () => void }) {
  const queryClient = useQueryClient();
  const [value, setValue] = useState(cluster.namespaces?.join(", ") ?? "");
  const save = useMutation({
    mutationFn: (namespaces: string[]) => ClusterService.SetNamespaces(cluster.id, namespaces),
    onSuccess: async () => {
      await queryClient.invalidateQueries();
      onDone();
    },
  });
  const namespaces = value.split(/[\s,]+/).filter(Boolean);

  return (
    <form
      className="flex max-w-lg flex-col gap-3 p-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate(namespaces);
      }}
    >
      <div>
        <p className="text-sm font-medium">{forbidden ? "Cluster-wide listing is forbidden" : "Namespace scope"}</p>
        <p className="text-sm text-muted-foreground">
          Enter the namespaces you may use. They are remembered for this cluster.
          {!forbidden && " Leave empty to list all namespaces."}
        </p>
      </div>
      <div className="flex gap-2">
        <Input autoFocus placeholder="default, payments" value={value} onChange={(e) => setValue(e.target.value)} />
        <Button type="submit" disabled={(forbidden && namespaces.length === 0) || save.isPending}>
          Save
        </Button>
        {!forbidden && (
          <Button type="button" variant="ghost" onClick={onDone}>
            Cancel
          </Button>
        )}
      </div>
      {save.error && <p className="text-sm text-destructive">{String(save.error)}</p>}
    </form>
  );
}
