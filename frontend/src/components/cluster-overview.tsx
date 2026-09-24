import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CaretDownIcon, CaretRightIcon, CheckIcon, MagnifyingGlassIcon } from "@phosphor-icons/react";
import { ClusterService } from "@bindings/internal/bindings";
import { RolloutState, type Cluster } from "@bindings/internal/service";
import { ConfigDetail } from "@/components/config-detail";
import { AddForward, forwardsFor } from "@/components/forwards";
import { hostsLabel, IngressDetail } from "@/components/ingress-detail";
import { PodDetail, ReasonBadge, restartsLabel, usagePressure } from "@/components/pod-detail";
import { WorkloadDetail, workloadLabel, workloadReason } from "@/components/workload-detail";
import { YamlDialog } from "@/components/yaml-view";
import { KindBadge, portsLabel, targetValue, useTargets, type Target, type TargetGroup } from "@/components/targets";
import { TargetVerbs } from "@/components/target-verbs";
import { Button } from "@/components/ui/button";
import { Combobox, ComboboxContent, ComboboxEmpty, ComboboxInput, ComboboxItem, ComboboxList, ComboboxTrigger } from "@/components/ui/combobox";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Toggle } from "@/components/ui/toggle";
import { configQuery, isForbidden, namespacesQuery, podMetricsQuery, podUsageKey } from "@/queries";
import { useUIStore } from "@/store";

// The search box is the only kind filter: every word must match the row's kind, group or name, so "secret pay" is the Secrets with "pay" in the name.
// Running things stay in view; ConfigMaps and Secrets are looked up by name, so each namespace folds them behind one line until asked or searched.
const folded = (group: string) => group === "ConfigMaps" || group === "Secrets";
// "ingresses" loses two letters where "services" loses one.
const singular = (word: string) => (word.endsWith("sses") ? word.slice(0, -2) : word.slice(0, -1));
const counts = (kinds: TargetGroup[]) =>
  kinds.map((g) => `${g.items.length} ${g.items.length === 1 ? singular(g.label.toLowerCase()) : g.label.toLowerCase()}`).join(" · ");

// An Ingress is searched by its hosts too: during an incident the URL is what you have, not the object's name.
const matches = (words: string[], group: string, t: Target) => {
  const hay = `${t.kind} ${t.workload?.kind ?? ""} ${group} ${t.label} ${t.ingress?.hosts?.join(" ") ?? ""}`.toLowerCase();
  return words.every((w) => hay.includes(w));
};

// Passage states a pod goes through on its way up or out; any other reason, a pod against a limit, and a stuck rollout are problems.
const transientReasons = new Set(["ContainerCreating", "PodInitializing", "Terminating"]);
const isProblem = (t: Target, pressure?: string) => {
  if (t.kind === "pod") return !!pressure || (!!t.reason && !transientReasons.has(t.reason.replace(/^Init:/, "")));
  if (t.ingress) return !!t.ingress.problem;
  return t.workload?.rollout?.state === RolloutState.RolloutStuck;
};

export function ClusterOverview({ cluster }: { cluster: Cluster }) {
  const namespaces = useQuery(namespacesQuery(cluster.id));
  const { data: config } = useQuery(configQuery);
  const { groups, error: listError, pending } = useTargets(cluster, true);
  const metrics = useQuery(podMetricsQuery(cluster.id)).data;
  const pressure = (t: Target) => (t.kind === "pod" ? usagePressure(metrics?.get(podUsageKey(t.namespace, t.name))?.usage, t.limits) : undefined);
  const [problems, setProblems] = useState(false);
  const [unfolded, setUnfolded] = useState<Record<string, boolean>>({});
  const [needle, setNeedle] = useState("");
  const [forwarding, setForwarding] = useState<Target | null>(null);
  const [inspecting, setInspecting] = useState<Target | null>(null);
  const search = useRef<HTMLInputElement>(null);
  const forwardFrom = (t: Target) => {
    setInspecting(null);
    setForwarding(t);
  };
  const explicit = cluster.namespaces ?? [];
  const inspectRequest = useUIStore((s) => s.inspectRequest);
  const requestInspect = useUIStore((s) => s.requestInspect);

  // Another tab asked for an object's detail; it opens once the lists have it, and a request for nothing listed is dropped.
  useEffect(() => {
    if (!inspectRequest || inspectRequest.clusterId !== cluster.id || pending) return;
    setInspecting(groups.flatMap((g) => g.items).find((t) => t.value === targetValue(inspectRequest.kind, inspectRequest.namespace, inspectRequest.name)) ?? null);
    requestInspect(null);
  }, [inspectRequest, pending, groups, cluster.id, requestInspect]);

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

  if ((!explicit.length && isForbidden(namespaces.error)) || isForbidden(listError)) {
    return <NamespacePrompt cluster={cluster} />;
  }
  const error = listError ?? (explicit.length ? null : namespaces.error);
  if (error) {
    return (
      <Empty className="justify-start border-0 pt-12">
        <EmptyHeader>
          <EmptyTitle>Cluster could not be listed</EmptyTitle>
          <EmptyDescription>{String(error)}</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  const words = needle.toLowerCase().split(/\s+/).filter(Boolean);
  const shown = groups.map((g) => ({ ...g, items: g.items.filter((t) => (!problems || isProblem(t, pressure(t))) && matches(words, g.label, t)) }));
  const byNamespace = new Map<string, TargetGroup[]>();
  for (const ns of explicit.length ? explicit : (namespaces.data ?? [])) byNamespace.set(ns, []);
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
      {inspecting?.kind === "pod" && <PodDetail cluster={cluster} target={inspecting} onForward={() => forwardFrom(inspecting)} onClose={() => setInspecting(null)} />}
      {inspecting?.workload && <WorkloadDetail cluster={cluster} target={inspecting} workload={inspecting.workload} onClose={() => setInspecting(null)} />}
      {inspecting?.config && <ConfigDetail cluster={cluster} target={inspecting} onClose={() => setInspecting(null)} />}
      {inspecting?.ingress && <IngressDetail cluster={cluster} target={inspecting} onClose={() => setInspecting(null)} />}
      {inspecting?.kind === "svc" && <YamlDialog cluster={cluster} target={inspecting} onClose={() => setInspecting(null)} />}
      <div className="flex flex-wrap items-center gap-2 px-4 py-2.5">
        <InputGroup className="w-auto min-w-48 flex-1">
          <InputGroupInput ref={search} placeholder="Filter by name or kind" value={needle} onChange={(e) => setNeedle(e.target.value)} />
          <InputGroupAddon>
            <MagnifyingGlassIcon />
          </InputGroupAddon>
          <InputGroupAddon align="inline-end">
            <kbd className="rounded border px-1 font-sans text-[10px] text-muted-foreground">/</kbd>
          </InputGroupAddon>
        </InputGroup>
        <Toggle variant="outline" size="sm" pressed={problems} onPressedChange={setProblems} title="Only pods, workloads and ingresses that are not healthy">
          Problems
        </Toggle>
        <NamespaceScope cluster={cluster} known={namespaces.error ? undefined : (namespaces.data ?? [])} />
      </div>
      {groups
        .filter((g) => g.error)
        .map((g) => (
          <p key={g.label} className="px-4 pb-1 text-xs text-muted-foreground">
            {isForbidden(g.error) ? `${g.label} are forbidden for this role.` : `${g.label} could not be listed: ${String(g.error)}`}
          </p>
        ))}
      <div className="min-h-0 flex-1 overflow-auto pb-4">
        {total === 0 && !pending && (
          <Empty className="justify-start border-0 pt-12">
            <EmptyHeader>
              <EmptyTitle>{words.length ? `Nothing matches “${needle.trim()}”` : problems ? "This scope is healthy" : "Nothing to show"}</EmptyTitle>
              <EmptyDescription>
                {words.length ? "Try a shorter name, or a kind such as pod or secret." : problems ? "No crash loops, pull failures, pending pods, pods against a limit, stuck rollouts or Ingresses that reach no pod." : "This scope has no services, workloads, pods, ingresses, configmaps or secrets."}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        )}
        {[...byNamespace].map(([ns, kinds]) => {
          if (kinds.length === 0) return null;
          const running = kinds.filter((g) => !folded(g.label));
          const reference = kinds.filter((g) => folded(g.label));
          const open = words.length > 0 || !!unfolded[ns];
          const line = (t: Target) => (
            <TargetLine key={t.value} cluster={cluster} target={t} pressure={pressure(t)} onForward={() => setForwarding(t)} onInspect={() => setInspecting(t)} />
          );
          return (
            <section key={ns}>
              <h3 className="sticky top-0 z-10 flex items-baseline gap-2 bg-background px-4 pt-3 pb-1 text-sm font-medium">
                {ns}
                <span className="text-xs font-normal text-muted-foreground">{counts(running)}</span>
              </h3>
              {running.flatMap((g) => g.items).map(line)}
              {reference.length > 0 && words.length === 0 && (
                <button
                  type="button"
                  className="grid h-8 w-full grid-cols-[48px_1fr] items-center gap-3 px-4 text-left text-xs text-muted-foreground hover:bg-accent"
                  aria-expanded={open}
                  onClick={() => setUnfolded((u) => ({ ...u, [ns]: !open }))}
                >
                  {open ? <CaretDownIcon className="size-3" /> : <CaretRightIcon className="size-3" />}
                  {counts(reference)}
                </button>
              )}
              {open && reference.flatMap((g) => g.items).map(line)}
            </section>
          );
        })}
      </div>
    </div>
  );
}

// A row is identity and what is wrong. A service's ports and a rollout's readiness are bounded and stay;
// a pod's containers and ports, a workload's images and a config object's keys are in the detail, where they are acted on.
const keysLabel = (n: number) => (n === 1 ? "1 key" : `${n} keys`);
const meta = (t: Target) => {
  if (t.kind === "svc") return portsLabel(t.ports);
  if (t.kind === "pod" || t.kind === "secret") return "";
  if (t.ingress) return hostsLabel(t.ingress.hosts);
  if (t.config) return keysLabel(t.config.keys?.length ?? 0);
  return t.workload ? workloadLabel(t.workload) : "";
};

function TargetLine({ cluster, target, pressure, onForward, onInspect }: { cluster: Cluster; target: Target; pressure?: string; onForward: () => void; onInspect: () => void }) {
  return (
    <div className="group grid h-8 grid-cols-[48px_minmax(220px,26rem)_minmax(0,1fr)_auto] items-center gap-3 px-4 hover:bg-accent focus-within:bg-accent">
      <KindBadge kind={target.kind} />
      <button
        type="button"
        className="truncate text-left hover:underline"
        title={target.config ? `What is in ${target.name}?` : target.kind === "svc" ? `Show ${target.name}` : target.ingress ? `What does ${target.name} reach?` : `Why is ${target.name} in this state?`}
        onClick={onInspect}
      >
        {target.name}
      </button>
      <span className="flex min-w-0 items-center gap-2 font-mono text-xs text-muted-foreground">
        {target.kind === "pod" && (
          <>
            <ReasonBadge reason={target.reason} className="cursor-pointer" onClick={onInspect} />
            <ReasonBadge reason={pressure} className="cursor-pointer" title="Close to its limit" onClick={onInspect} />
            {!!target.restarts && <span className="shrink-0">{restartsLabel(target.restarts, target.lastRestart)}</span>}
          </>
        )}
        {target.workload && <ReasonBadge reason={workloadReason(target.workload)} className="cursor-pointer" onClick={onInspect} />}
        {target.ingress && <ReasonBadge reason={target.ingress.problem} className="cursor-pointer" title="Where the chain to its pods stops" onClick={onInspect} />}
        <span className="truncate">{meta(target)}</span>
      </span>
      <span className="flex justify-end gap-0.5">
        <TargetVerbs cluster={cluster} target={target} onForward={onForward} row />
      </span>
    </div>
  );
}

// A role that may not list namespaces can still add one by name.
function NamespaceScope({ cluster, known }: { cluster: Cluster; known?: string[] }) {
  const queryClient = useQueryClient();
  const scope = cluster.namespaces ?? [];
  const [draft, setDraft] = useState(scope);
  const [query, setQuery] = useState("");
  // Saves run one after another, so quick ticks land in the order they were made.
  const save = useMutation({
    scope: { id: `namespaces:${cluster.id}` },
    mutationFn: (namespaces: string[]) => ClusterService.SetNamespaces(cluster.id, namespaces),
    onSettled: () => queryClient.invalidateQueries(),
  });
  const pick = (namespaces: string[]) => {
    setDraft(namespaces);
    save.mutate([...namespaces].sort());
  };
  const typed = query.trim();
  const items = [...new Set([...(known ?? []), ...draft, ...(!known && typed ? [typed] : [])])].sort();
  const label = scope.length ? scope.join(", ") : "All namespaces";

  return (
    <Combobox
      multiple
      items={items}
      value={draft}
      onValueChange={pick}
      inputValue={query}
      onInputValueChange={setQuery}
      onOpenChange={(open) => {
        if (open) setDraft(scope);
        else setQuery("");
      }}
    >
      <ComboboxTrigger render={<Button variant="ghost" size="sm" className="max-w-80 text-muted-foreground" title={label} />}>
        <span className="truncate">{label}</span>
      </ComboboxTrigger>
      <ComboboxContent align="end" className="w-72">
        <ComboboxInput showTrigger={false} placeholder={known ? "Search namespaces" : "Add a namespace by name"} autoFocus>
          <MagnifyingGlassIcon className="order-first ml-2 size-4 text-muted-foreground" />
        </ComboboxInput>
        {known && (
          <button
            type="button"
            className="relative mx-1 mt-1 flex w-[calc(100%-0.5rem)] items-center rounded-md py-1 pr-8 pl-1.5 text-left text-sm hover:bg-accent"
            onClick={() => pick([])}
          >
            All namespaces
            {draft.length === 0 && <CheckIcon className="absolute right-2 size-4" />}
          </button>
        )}
        <ComboboxEmpty>{known ? "No namespace matches." : "Type a namespace you may use."}</ComboboxEmpty>
        <ComboboxList>
          {(ns: string) => (
            <ComboboxItem key={ns} value={ns}>
              {ns}
            </ComboboxItem>
          )}
        </ComboboxList>
        {save.error && <p className="px-2 pb-2 text-xs text-destructive">{String(save.error)}</p>}
      </ComboboxContent>
    </Combobox>
  );
}

function NamespacePrompt({ cluster }: { cluster: Cluster }) {
  const queryClient = useQueryClient();
  const [value, setValue] = useState(cluster.namespaces?.join(", ") ?? "");
  const save = useMutation({
    mutationFn: (namespaces: string[]) => ClusterService.SetNamespaces(cluster.id, namespaces),
    onSuccess: () => queryClient.invalidateQueries(),
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
        <p className="text-sm font-medium">Cluster-wide listing is forbidden</p>
        <p className="text-sm text-muted-foreground">Enter the namespaces you may use. They are remembered for this cluster.</p>
      </div>
      <div className="flex gap-2">
        <Input autoFocus placeholder="default, payments" value={value} onChange={(e) => setValue(e.target.value)} />
        <Button type="submit" disabled={namespaces.length === 0 || save.isPending}>
          Save
        </Button>
      </div>
      {save.error && <p className="text-sm text-destructive">{String(save.error)}</p>}
    </form>
  );
}
