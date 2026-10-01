import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { useIsMutating, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CaretDownIcon, CaretRightIcon, CheckIcon, CircleNotchIcon, MagnifyingGlassIcon } from "@phosphor-icons/react";
import { ClusterService } from "@bindings/internal/bindings";
import { JobResult, RolloutState, type Cluster, type KubeHPA } from "@bindings/internal/service";
import { ConfigDetail } from "@/components/config-detail";
import { AddForward, forwardsFor } from "@/components/forwards";
import { HPADetail, metricsLabel, metricsTitle } from "@/components/hpa-detail";
import { hostsLabel, IngressDetail } from "@/components/ingress-detail";
import { useInspectorWalk } from "@/components/inspector";
import { ago, PodDetail, ReasonBadge, restartsLabel, since, usagePressure } from "@/components/pod-detail";
import { PVCDetail, pvcLabel, pvcReason } from "@/components/pvc-detail";
import { WorkloadDetail, workloadLabel, workloadReason } from "@/components/workload-detail";
import { YamlDetail } from "@/components/yaml-view";
import { KindBadge, portsLabel, targetValue, useTargets, type Target, type TargetGroup } from "@/components/targets";
import { TargetVerbs } from "@/components/target-verbs";
import { Button } from "@/components/ui/button";
import { Combobox, ComboboxContent, ComboboxEmpty, ComboboxInput, ComboboxItem, ComboboxList, ComboboxTrigger } from "@/components/ui/combobox";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { configQuery, isForbidden, namespacesQuery, podMetricsQuery, podUsageKey, errorText } from "@/queries";
import { useUIStore } from "@/store";
import { cn, isZeroTime } from "@/lib/utils";

// Every search word must be in the row's name; the kind is picked from the menu beside it, which lists that kind alone,
// flat: picking Pods lists pods without the workloads that run them.
// Running things stay in view; ConfigMaps and Secrets are looked up by name, so each namespace folds them behind one line until asked or searched.
const folded = (group: string) => group === "ConfigMaps" || group === "Secrets";
// "ingresses" loses two letters where "services" loses one.
const singular = (word: string) => (word.endsWith("sses") ? word.slice(0, -2) : word.slice(0, -1));
const counts = (kinds: TargetGroup[]) =>
  kinds.map((g) => `${g.items.length} ${g.items.length === 1 ? singular(g.label.toLowerCase()) : g.label.toLowerCase()}`).join(" · ");

const matches = (words: string[], t: Target) => words.every((w) => t.name.toLowerCase().includes(w));

// The tick goes before the label, as in a macOS menu, so a count keeps the right edge to itself.
const checkLeft = "pr-2 pl-7 *:data-[slot$=indicator]:right-auto *:data-[slot$=indicator]:left-2";

// Passage states a pod goes through on its way up or out, and the end of one that finished cleanly; any other reason,
// a stuck rollout, a failed Job, a claim no volume backs and an autoscaler that cannot scale are problems.
// A pod near its limit is not: a JVM sized to its limit lives there, so it keeps its badge but raises no alarm, as in k9s and Headlamp.
const transientReasons = new Set(["ContainerCreating", "PodInitializing", "Terminating", "Completed"]);
const isProblem = (t: Target) => {
  if (t.kind === "pod") return !!t.reason && !transientReasons.has(t.reason.replace(/^Init:/, ""));
  if (t.ingress) return !!t.ingress.problem;
  if (t.pvc) return !!pvcReason(t.pvc);
  if (t.hpa) return !!t.hpa.problem;
  return t.workload?.rollout?.state === RolloutState.RolloutStuck || t.workload?.job?.result === JobResult.JobFailed;
};

export function ClusterOverview({ cluster }: { cluster: Cluster }) {
  const namespaces = useQuery(namespacesQuery(cluster.id));
  const { data: config } = useQuery(configQuery);
  const { groups: liveGroups, error: listError, pending, fetching } = useTargets(cluster, true);
  const rescoping = useIsMutating({ mutationKey: scopeKey(cluster.id) }) > 0;
  const explicit = cluster.namespaces ?? [];
  // Each kind is its own list and lands on its own; the rows change once all have, so a namespace fills in one step.
  const live = { groups: liveGroups, namespaces: explicit.length ? explicit : (namespaces.data ?? []) };
  // Until the first time they do, the Overview is loading rather than empty.
  const settled = useRef({ ...live, loaded: false });
  if (!fetching && !namespaces.isFetching && !rescoping) settled.current = { ...live, loaded: true };
  const { groups, loaded } = settled.current;
  const metrics = useQuery(podMetricsQuery(cluster.id)).data;
  const pressure = (t: Target) => (t.kind === "pod" ? usagePressure(metrics?.get(podUsageKey(t.namespace, t.name))?.usage, t.limits) : undefined);
  // A Job that succeeded, and its pods, stay listed only until the Job is cleaned up, so each namespace folds them behind one line.
  // A pod it retried after an error is part of that run, not a problem of its own.
  const succeeded = new Set(groups.flatMap((g) => g.items).filter((t) => t.workload?.job?.result === JobResult.JobComplete).map((t) => t.value));
  const finished = (t: Target) => succeeded.has(t.value) || (t.kind === "pod" && (t.reason === "Completed" || (!!t.owner && succeeded.has(t.owner))));
  const problem = (t: Target) => !finished(t) && isProblem(t);
  const [onlyProblems, setOnlyProblems] = useState(false);
  const [unfolded, setUnfolded] = useState<Record<string, boolean>>({});
  // Workloads whose pods or runs are folded away, by row value; open is the default, and a filter shows its matches regardless.
  const [hiddenChildren, setHiddenChildren] = useState<Record<string, boolean>>({});
  const [needle, setNeedle] = useState("");
  const [picked, setPicked] = useState<string | null>(null);
  const [forwarding, setForwarding] = useState<Target | null>(null);
  const [inspecting, setInspecting] = useState<Target | null>(null);
  const search = useRef<HTMLInputElement>(null);
  const forwardFrom = (t: Target) => {
    setInspecting(null);
    setForwarding(t);
  };
  const inspectRequest = useUIStore((s) => s.inspectRequest);
  const requestInspect = useUIStore((s) => s.requestInspect);

  // Another tab asked for an object's detail (a node's is the Nodes tab's); it opens once the lists have it, and a request for nothing listed is dropped.
  useEffect(() => {
    if (!inspectRequest || inspectRequest.clusterId !== cluster.id || inspectRequest.kind === "node" || pending) return;
    setInspecting(liveGroups.flatMap((g) => g.items).find((t) => t.value === targetValue(inspectRequest.kind, inspectRequest.namespace, inspectRequest.name)) ?? null);
    requestInspect(null);
  }, [inspectRequest, pending, liveGroups, cluster.id, requestInspect]);

  const shownRows = useRef<Target[]>([]);
  useInspectorWalk(shownRows, inspecting, (t) => t.value, setInspecting);

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
    return <NamespacePrompt cluster={cluster} error={explicit.length ? listError : null} />;
  }
  const error = listError ?? (explicit.length ? null : namespaces.error);
  if (error) {
    return (
      <Empty className="justify-start border-0 pt-12">
        <EmptyHeader>
          <EmptyTitle>Cluster could not be listed</EmptyTitle>
          <EmptyDescription>{errorText(error)}</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  const words = needle.toLowerCase().split(/\s+/).filter(Boolean);
  // A kind the scope no longer has lets go, rather than leave the list empty with no way to see why.
  const kinds = groups.filter((g) => g.items.length);
  const kind = kinds.some((g) => g.label === picked) ? picked : null;
  // Counted over the scope, not the search or the kind: narrowing the list must not make the alarm go quiet.
  const unhealthy = groups.map((g) => ({ ...g, items: g.items.filter(problem) })).filter((g) => g.items.length);
  // Once nothing is unhealthy the filter lets go, rather than leave an empty list behind a line that is gone.
  const problems = onlyProblems && unhealthy.length > 0;
  const shown = groups.map((g) => ({ ...g, items: !kind || g.label === kind ? g.items.filter((t) => (!problems || problem(t)) && matches(words, t)) : [] }));
  const byNamespace = new Map<string, TargetGroup[]>();
  for (const ns of settled.current.namespaces) byNamespace.set(ns, []);
  for (const g of shown) {
    for (const t of g.items) {
      const list = byNamespace.get(t.namespace) ?? [];
      const own = list.find((x) => x.label === g.label) ?? (list.push({ label: g.label, items: [] }), list.at(-1)!);
      own.items.push(t);
      byNamespace.set(t.namespace, list);
    }
  }
  const total = shown.reduce((n, g) => n + g.items.length, 0);
  // A pod goes under the workload or Job that runs it, and a Job under its CronJob; a row stays in view while anything under it matches.
  const passing = new Set(shown.flatMap((g) => g.items.map((t) => t.value)));
  const everything = groups.flatMap((g) => g.items);
  const workloads = new Set(everything.filter((t) => t.workload).map((t) => t.value));
  const childrenOf = new Map<string, Target[]>();
  for (const t of everything) if (t.owner && workloads.has(t.owner) && !finished(t)) childrenOf.set(t.owner, [...(childrenOf.get(t.owner) ?? []), t]);
  const scaledBy = new Map(everything.flatMap((t) => (t.hpa && t.owner ? [[t.owner, t.hpa] as const] : [])));
  const shows = (t: Target): boolean => passing.has(t.value) || (childrenOf.get(t.value) ?? []).some(shows);
  const seeking = words.length > 0 || !!kind;
  const filtering = seeking || problems;
  shownRows.current = [];

  return (
    <div className="@container flex min-h-0 flex-1 flex-col">
      {forwarding && (
        <AddForward cluster={cluster} saved={forwardsFor(config?.forwards, cluster)} initial={forwarding} onClose={() => setForwarding(null)} />
      )}
      {inspecting?.kind === "pod" && <PodDetail key={inspecting.value} cluster={cluster} target={inspecting} onForward={() => forwardFrom(inspecting)} onClose={() => setInspecting(null)} />}
      {inspecting?.workload && <WorkloadDetail key={inspecting.value} cluster={cluster} target={inspecting} workload={inspecting.workload} onClose={() => setInspecting(null)} />}
      {inspecting?.config && <ConfigDetail key={inspecting.value} cluster={cluster} target={inspecting} onClose={() => setInspecting(null)} />}
      {inspecting?.pvc && <PVCDetail key={inspecting.value} cluster={cluster} target={inspecting} pvc={inspecting.pvc} onClose={() => setInspecting(null)} />}
      {inspecting?.hpa && <HPADetail key={inspecting.value} cluster={cluster} target={inspecting} hpa={inspecting.hpa} onClose={() => setInspecting(null)} />}
      {inspecting?.ingress && <IngressDetail key={inspecting.value} cluster={cluster} target={inspecting} onClose={() => setInspecting(null)} />}
      {inspecting?.kind === "svc" && <YamlDetail key={inspecting.value} cluster={cluster} target={inspecting} onForward={() => forwardFrom(inspecting)} onClose={() => setInspecting(null)} />}
      <div className="flex flex-wrap items-center gap-2 px-4 py-2.5">
        <InputGroup className="h-7 w-72">
          <InputGroupInput ref={search} placeholder="Filter by name" value={needle} onChange={(e) => setNeedle(e.target.value)} />
          <InputGroupAddon>
            <MagnifyingGlassIcon />
          </InputGroupAddon>
          <InputGroupAddon align="inline-end">
            <kbd className="font-sans text-[10px] text-muted-foreground">/</kbd>
          </InputGroupAddon>
        </InputGroup>
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant="outline" size="sm" />}>
            {kind ?? "All"}
            <CaretDownIcon />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="min-w-48">
            <DropdownMenuRadioGroup value={kind ?? ""} onValueChange={(v: string) => setPicked(v || null)}>
              <DropdownMenuRadioItem value="" closeOnClick className={checkLeft}>
                All
              </DropdownMenuRadioItem>
              {kinds.map((g) => (
                <DropdownMenuRadioItem key={g.label} value={g.label} closeOnClick className={checkLeft}>
                  {g.label}
                  <DropdownMenuShortcut className="tracking-normal tabular-nums">{g.items.length}</DropdownMenuShortcut>
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {/* Only there while something is broken: a healthy cluster shows no counter to read. "Show only these" lists exactly what it names, so it drops the kind. */}
      {unhealthy.length > 0 && (
        <div className="mx-4 mb-2 flex items-center gap-3 rounded-md bg-destructive/10 py-1 pr-1 pl-3 text-sm text-destructive">
          <span className="min-w-0 flex-1 truncate">{counts(unhealthy)} not healthy</span>
          <Button
            variant="ghost"
            size="xs"
            className="text-destructive hover:bg-destructive/15 hover:text-destructive"
            onClick={() => {
              if (!problems) setPicked(null);
              setOnlyProblems(!problems);
            }}
          >
            {problems ? "Show everything" : "Show only these"}
          </Button>
        </div>
      )}
      {groups
        .filter((g) => g.error)
        .map((g) => (
          <p key={g.label} className="px-4 pb-1 text-xs text-muted-foreground">
            {isForbidden(g.error) ? `${g.label} are forbidden for this role.` : `${g.label} could not be listed: ${errorText(g.error)}`}
          </p>
        ))}
      {/* Every row is a subgrid of this one, so its columns line up across rows: kind, name, details, age, verbs.
          The list holds every kind, so only columns every kind fills get one: a problem's badge follows the name, and Details is each
          kind's one key fact. Details takes what is left, so a long one has room and Age sits at the right edge, as in Lens.
          No header row names them: each column means one thing, which the kind badge and the value's own shape already say.
          The name is as wide as its widest cell, but never less than 40% of the list (at most 12rem).
          When the list is narrow (a detail open beside it), Age steps aside with its header, the gutters shrink to 20px and Details
          keeps at least 8rem, so long pod names are cut before the squares and ports are.
          The edge columns are auto because a row's padding is laid into them, which a fixed width would not fit. */}
      <div
        className={cn(
          "grid min-h-0 flex-1 grid-cols-[auto_minmax(min(12rem,40%),auto)_minmax(8rem,1fr)_auto] content-start gap-x-5 overflow-x-hidden overflow-y-auto pb-4 transition-opacity @2xl:grid-cols-[auto_minmax(min(12rem,40%),auto)_1fr_auto_auto] @2xl:gap-x-8",
          rescoping && "opacity-50",
        )}
      >
        {total === 0 && (
          <Empty className="col-span-full justify-start border-0 pt-12">
            <EmptyHeader>
              <EmptyTitle>{!loaded ? "Loading…" : words.length ? `Nothing matches “${needle.trim()}”` : problems ? `No ${kind?.toLowerCase()} are unhealthy` : "Nothing to show"}</EmptyTitle>
              <EmptyDescription>
                {!loaded ? "Its objects appear as soon as the cluster answers." : words.length ? "Try a shorter name." : problems ? "Pick All to see the rest." : "This scope has no services, workloads, jobs, autoscalers, volume claims, pods, ingresses, configmaps or secrets."}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        )}
        {[...byNamespace].map(([ns, kinds]) => {
          if (kinds.length === 0) return null;
          const done = kinds.flatMap((g) => g.items).filter(finished);
          const reference = kinds.filter((g) => folded(g.label));
          const line = (t: Target, depth: Depth = 0, children?: Target[]) => {
            shownRows.current.push(t);
            return (
              <TargetLine
                key={t.value}
                cluster={cluster}
                target={t}
                pressure={pressure(t)}
                scaledBy={scaledBy.get(t.value)}
                done={finished(t)}
                selected={t.value === inspecting?.value}
                onInspect={() => setInspecting(t)}
                depth={depth}
                fold={children?.length ? { open: filtering || !hiddenChildren[t.value], toggle: () => setHiddenChildren((h) => ({ ...h, [t.value]: !h[t.value] })) } : undefined}
              />
            );
          };
          // The header counts what is shown, a workload kept for its matching pods included.
          const live = everything.filter((t) => t.namespace === ns && t.kind !== "cm" && t.kind !== "secret" && !finished(t));
          const shownLive = new Set<string>();
          const tree = (t: Target, depth: Depth): ReactNode[] => {
            if (!shows(t)) return [];
            const children = (childrenOf.get(t.value) ?? []).filter(shows);
            shownLive.add(t.value);
            const open = filtering || !hiddenChildren[t.value];
            return [line(t, depth, children), ...(open ? children.flatMap((c) => tree(c, (depth + 1) as Depth)) : [])];
          };
          const rows = kind
            ? live.filter((t) => passing.has(t.value)).map((t) => (shownLive.add(t.value), line(t)))
            : live.filter((t) => !(t.owner && workloads.has(t.owner))).flatMap((t) => tree(t, 0));
          const running = groups.filter((g) => !folded(g.label)).map((g) => ({ ...g, items: g.items.filter((t) => t.namespace === ns && shownLive.has(t.value)) }));
          const foldLine = (key: string, list: Target[], label: string) => {
            if (!list.length) return null;
            const open = seeking || !!unfolded[key];
            return (
              <>
                {!seeking && (
                  <button
                    type="button"
                    className="col-span-full grid h-8 grid-cols-[48px_1fr] items-center gap-3 px-4 text-left text-xs text-muted-foreground hover:bg-accent"
                    aria-expanded={open}
                    onClick={() => setUnfolded((u) => ({ ...u, [key]: !open }))}
                  >
                    {open ? <CaretDownIcon className="size-3" /> : <CaretRightIcon className="size-3" />}
                    <span className="pl-5">{label}</span>
                  </button>
                )}
                {open && list.map((t) => line(t))}
              </>
            );
          };
          return (
            <section key={ns} className="col-span-full grid grid-cols-subgrid">
              {/* Laid on the rows' columns: the namespace across kind and name, its counts where Details starts, so neither runs across a column's start. */}
              <h3 className="sticky top-0 z-10 col-span-full grid grid-cols-subgrid items-baseline bg-background px-4 pt-3 pb-1 text-sm font-medium whitespace-nowrap">
                <span className="col-span-2 truncate" title={ns}>
                  {ns}
                </span>
                <span className="min-w-0 truncate text-xs font-normal text-muted-foreground" title={counts(running.filter((g) => g.items.length))}>
                  {counts(running.filter((g) => g.items.length))}
                </span>
              </h3>
              {rows}
              {foldLine(`${ns}:finished`, done, `${counts([{ label: "Jobs", items: done.filter((t) => t.kind === "job") }, { label: "Pods", items: done.filter((t) => t.kind === "pod") }].filter((g) => g.items.length))} finished`)}
              {foldLine(ns, reference.flatMap((g) => g.items), counts(reference))}
            </section>
          );
        })}
      </div>
    </div>
  );
}

// Details is the one short figure each kind has: a service's ports, an Ingress's host, a CronJob's schedule, a claim's size
// and class, what drives an autoscaler, or a config object's key count. The sentence behind a figure is its title; containers, images and keys are in the detail, where they are acted on.
type Figure = { text: string; title?: string; mono?: boolean };
const keysLabel = (n: number) => (n === 1 ? "1 key" : `${n} keys`);
const figure = (t: Target): Figure | null => {
  if (t.kind === "svc") return { text: portsLabel(t.ports), mono: true };
  if (t.ingress) return { text: hostsLabel(t.ingress.hosts), title: t.ingress.hosts?.join("\n"), mono: true };
  if (t.config) return { text: keysLabel(t.config.keys?.length ?? 0) };
  if (t.pvc) return { text: pvcLabel(t.pvc), mono: true };
  if (t.hpa) return { text: metricsLabel(t.hpa), title: metricsTitle(t.hpa), mono: true };
  if (t.workload?.cronJob) return { text: t.workload.cronJob.schedule, title: workloadLabel(t.workload), mono: true };
  return null;
};

// Ready as Lens draws it, the same in every row: one square per replica, container or completion, filled when it is ready
// and hollow when it is not, hollow in red when the rollout is stuck or the Job failed. The squares start where the column does,
// so rows line up; the count is in the title. An autoscaled workload runs on to its maximum, the replicas it may still add drawn
// as stubs on the baseline, and past 12 slots each square stands for a share.
const maxSlots = 12;
function Slots({ ready, of, max = 0, failed, title }: { ready: number; of: number; max?: number; failed?: boolean; title: string }) {
  const slots = Math.max(of, max);
  const scale = (n: number) => (slots > maxSlots ? Math.round((n * maxSlots) / slots) : n);
  const [filled, wanted] = [scale(ready), scale(of)];
  return (
    <span className="flex h-2.5 items-end gap-[3px]" title={title}>
      {Array.from({ length: Math.min(slots, maxSlots) }, (_, i) => (
        <span
          key={i}
          className={cn(
            "w-2.5 rounded-[2px]",
            i < filled ? "h-2.5 bg-foreground/60" : i >= wanted ? "h-px bg-foreground/30" : cn("h-2.5 border", failed ? "border-destructive" : "border-foreground/45"),
          )}
        />
      ))}
    </span>
  );
}

// Names sit one step in so a workload's caret has room left of its name, and each level under it one more step behind a guide line.
type Depth = 0 | 1 | 2;
const indent: Record<Depth, string> = { 0: "pl-5", 1: "ml-1.5 border-l pl-8", 2: "ml-1.5 border-l pl-13" };

// Name and Details stay when the list is narrow (a detail open beside it); Age steps aside for every row at once,
// rather than being truncated row by row.
function TargetLine({
  cluster,
  target,
  pressure,
  scaledBy,
  done,
  selected,
  onInspect,
  depth,
  fold,
}: {
  cluster: Cluster;
  target: Target;
  pressure?: string;
  // The autoscaler of a workload, whose range its replica ticks run to.
  scaledBy?: KubeHPA;
  // Finished: a Job that succeeded or a pod of one, listed muted in the namespace's fold.
  done: boolean;
  selected: boolean;
  onInspect: () => void;
  depth: Depth;
  fold?: { open: boolean; toggle: () => void };
}) {
  const f = figure(target);
  const job = target.workload?.job;
  const reason = target.workload && workloadReason(target.workload);
  return (
    <div
      data-row={target.value}
      className={cn(
        "group col-span-full grid h-8 grid-cols-subgrid items-center px-4 text-[13px] tabular-nums hover:bg-accent focus-within:bg-accent",
        selected && "bg-accent shadow-[inset_2px_0_0_var(--primary)]",
        done && "text-muted-foreground",
      )}
    >
      {/* 12px between the kind and its name, which belong together, rather than a whole gutter. */}
      <KindBadge kind={target.kind} className="-mr-2 @2xl:-mr-5" />
      <span className={cn("flex min-w-0 items-center self-stretch", indent[depth])}>
        {fold && (
          <button
            type="button"
            className="-ml-5 flex w-5 shrink-0 text-muted-foreground hover:text-foreground"
            aria-expanded={fold.open}
            aria-label={`${fold.open ? "Hide" : "Show"} the ${target.kind === "cron" ? "runs" : "pods"} of ${target.name}`}
            onClick={fold.toggle}
          >
            {fold.open ? <CaretDownIcon className="size-3" /> : <CaretRightIcon className="size-3" />}
          </button>
        )}
        <button
          type="button"
          className={cn("truncate text-left text-sm hover:underline", target.workload && !depth && "font-medium")}
          title={target.config ? `What is in ${target.name}?` : target.kind === "svc" ? `Show ${target.name}` : target.ingress ? `What does ${target.name} reach?` : `Why is ${target.name} in this state?`}
          onClick={onInspect}
        >
          {target.name}
        </button>
        {/* Only what is wrong, or on its way, right after the name: a healthy or finished object says nothing, and the name is cut before the badge. */}
        {target.kind === "pod" && (
          <>
            <ReasonBadge reason={target.reason === "Completed" ? undefined : target.reason} className="ml-2 shrink-0 cursor-pointer" onClick={onInspect} />
            <ReasonBadge reason={pressure} className="ml-2 shrink-0 cursor-pointer" title="Close to its limit" onClick={onInspect} />
          </>
        )}
        {target.workload && <ReasonBadge reason={reason} className="ml-2 shrink-0 cursor-pointer" title={job?.message || reason} onClick={onInspect} />}
        {target.pvc && <ReasonBadge reason={pvcReason(target.pvc)} className="ml-2 shrink-0 cursor-pointer" onClick={onInspect} />}
        {target.hpa && <ReasonBadge reason={target.hpa.problem} className="ml-2 shrink-0 cursor-pointer" onClick={onInspect} />}
        {target.ingress && (
          <ReasonBadge reason={target.ingress.problem} className="ml-2 shrink-0 cursor-pointer" title="Where the chain to its pods stops" onClick={onInspect} />
        )}
      </span>
      {/* Each kind's one key fact: a workload's replicas, a Job's completions or a pod's containers as squares, as kubectl counts them,
          a pod's restarts beside them once there are any, or the figure of a kind that runs nothing. */}
      <span className="flex min-w-0 items-center gap-2.5">
        {target.workload?.rollout && (
          <Slots
            ready={target.workload.rollout.ready}
            of={target.workload.rollout.desired}
            max={scaledBy?.max}
            failed={target.workload.rollout.state === RolloutState.RolloutStuck}
            title={`${target.workload.rollout.ready} of ${target.workload.rollout.desired} ready${scaledBy ? `, autoscaled between ${scaledBy.min} and ${scaledBy.max}` : ""}`}
          />
        )}
        {job && (
          <Slots ready={job.succeeded} of={job.completions} failed={job.result === JobResult.JobFailed} title={`${job.succeeded} of ${job.completions} completed`} />
        )}
        {target.kind === "pod" && target.reason !== "Completed" && (
          <Slots ready={target.ready ?? 0} of={target.containers.length} title={`${target.ready ?? 0} of ${target.containers.length} containers ready`} />
        )}
        {!!target.restarts && (
          <span
            className={cn("shrink-0", target.reason === "CrashLoopBackOff" ? "text-destructive" : "text-muted-foreground")}
            title={restartsLabel(target.restarts, target.lastRestart)}
          >
            {target.restarts === 1 ? "1 restart" : `${target.restarts} restarts`}
          </span>
        )}
        {f && (
          <span className={cn("truncate text-xs", f.mono && "font-mono")} title={f.title ?? f.text}>
            {f.text}
          </span>
        )}
      </span>
      <span className="hidden @2xl:flex">
        {target.created && !isZeroTime(target.created) && <span title={`Created ${ago(target.created)}, ${new Date(target.created).toLocaleString()}`}>{since(target.created)}</span>}
      </span>
      <span className="flex justify-end gap-0.5">
        <TargetVerbs cluster={cluster} target={target} row />
      </span>
    </div>
  );
}

const scopeKey = (clusterId: string) => ["namespace-scope", clusterId];
const textWidth = document.createElement("canvas").getContext("2d")!;

// The scope is the Cluster's, not the Overview's: Events and the Nodes' pods are read through it too.
// A role that may not list namespaces can still add one by name.
export function NamespaceScope({ cluster }: { cluster: Cluster }) {
  const queryClient = useQueryClient();
  const namespaces = useQuery(namespacesQuery(cluster.id));
  const known = namespaces.error ? undefined : (namespaces.data ?? []);
  const scope = cluster.namespaces ?? [];
  const [draft, setDraft] = useState(scope);
  const [query, setQuery] = useState("");
  // Saves run one after another, so quick ticks land in the order they were made; each stays pending until the lists
  // have been fetched again, which is what the spinner and the dimmed list show.
  const save = useMutation({
    mutationKey: scopeKey(cluster.id),
    scope: { id: scopeKey(cluster.id).join(":") },
    mutationFn: (namespaces: string[]) => ClusterService.SetNamespaces(cluster.id, namespaces),
    onSettled: () =>
      Promise.all([queryClient.invalidateQueries({ queryKey: ["config"] }), queryClient.invalidateQueries({ queryKey: ["cluster", cluster.id] })]),
  });
  const pick = (namespaces: string[]) => {
    setDraft(namespaces);
    save.mutate([...namespaces].sort());
  };
  const typed = query.trim();
  const items = [...new Set([...(known ?? []), ...draft, ...(!known && typed ? [typed] : [])])].sort();
  const label = scope.length ? scope.join(", ") : "All namespaces";
  // Every name is written and cut where the button ends; the count is of the names that cannot be read whole.
  const labelRef = useRef<HTMLSpanElement>(null);
  const [shown, setShown] = useState(scope.length);
  useLayoutEffect(() => {
    const el = labelRef.current;
    if (!el) return;
    const { font, fontFamily } = getComputedStyle(el);
    const width = (text: string, as: string) => ((textWidth.font = as), textWidth.measureText(text).width);
    // The count is an 11px pill with 6px padding a side, 6px from the names.
    const count = (n: number) => (n < scope.length ? width(`+${scope.length - n}`, `11px ${fontFamily}`) + 18 : 0);
    const fits = (n: number) => width(scope.slice(0, n).join(", "), font) + count(n) <= el.clientWidth;
    const fit = () => {
      let n = scope.length;
      while (n > 1 && !fits(n)) n--;
      setShown(n);
    };
    fit();
    // Widths measured before the app's font arrives are the fallback's.
    void document.fonts.ready.then(fit);
  }, [label]);

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
      {/* The caret sits by the name, and the spinner takes its place while the scope saves. */}
      <ComboboxTrigger
        render={<Button variant="ghost" size="sm" className={cn("max-w-60 bg-foreground/6 text-muted-foreground [&>svg:last-child]:size-3.5", save.isPending && "[&>svg:last-child]:hidden")} title={label} />}
      >
        <span ref={labelRef} className="flex min-w-0 flex-1 items-center gap-1.5">
          <span className="truncate">{label}</span>
          {shown < scope.length && <span className="shrink-0 rounded-full bg-foreground/8 px-1.5 text-[11px] leading-4 tabular-nums">+{scope.length - shown}</span>}
        </span>
        {save.isPending && <CircleNotchIcon className="size-4 animate-spin" />}
      </ComboboxTrigger>
      <ComboboxContent align="start" className="w-72">
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
              {known || draft.includes(ns) ? ns : `Add “${ns}”`}
            </ComboboxItem>
          )}
        </ComboboxList>
        {save.error && <p className="px-2 pb-2 text-xs text-destructive">{errorText(save.error)}</p>}
      </ComboboxContent>
    </Combobox>
  );
}

// With a scope set, the error says which namespace the role may not read.
function NamespacePrompt({ cluster, error }: { cluster: Cluster; error: unknown }) {
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
        <p className="text-sm font-medium">{error ? "Some of these namespaces are forbidden" : "Cluster-wide listing is forbidden"}</p>
        <p className="text-sm text-muted-foreground">Enter the namespaces you may use. They are remembered for this cluster.</p>
        {!!error && <p className="mt-2 text-xs text-muted-foreground">{errorText(error)}</p>}
      </div>
      <div className="flex gap-2">
        <Input autoFocus placeholder="default, payments" value={value} onChange={(e) => setValue(e.target.value)} />
        <Button type="submit" disabled={namespaces.length === 0 || save.isPending}>
          Save
        </Button>
      </div>
      {save.error && <p className="text-sm text-destructive">{errorText(save.error)}</p>}
    </form>
  );
}
