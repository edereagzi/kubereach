import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { defaultRangeExtractor, useVirtualizer, type Range } from "@tanstack/react-virtual";
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
import { RowVerbs, useRowFacts } from "@/components/target-verbs";
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
const counts = (kinds: [label: string, n: number][]) => kinds.map(([label, n]) => `${n} ${n === 1 ? singular(label.toLowerCase()) : label.toLowerCase()}`).join(" · ");
const tally = (groups: TargetGroup[]) => groups.map((g): [string, number] => [g.label, g.items.length]);

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

// The Overview is drawn as one sequence of fixed-height lines, of which only those in view are mounted: a namespace's
// header, an object's row, and the line a fold stands behind. Folding, the filter, the kind and "show only these" change
// the sequence, never a line's height.
type Line =
  | { type: "ns"; key: string; ns: string; counts: string }
  // fold is whether the row's pods or runs are shown, undefined for a row with none.
  | { type: "row"; key: string; target: Target; depth: Depth; done: boolean; fold?: boolean }
  | { type: "fold"; key: string; fold: string; label: string; open: boolean };
const headerHeight = 36;
const lineHeight = 32;
const noNamespaces: string[] = [];

// The maps the lines are drawn from, built once per change of the lists.
function derive(groups: TargetGroup[]) {
  const everything = groups.flatMap((g) => g.items);
  const groupOf = new Map<string, string>();
  const workloads = new Set<string>();
  // A Job that succeeded, and its pods, stay listed only until the Job is cleaned up, so each namespace folds them behind one line.
  // A pod it retried after an error is part of that run, not a problem of its own.
  const succeeded = new Set<string>();
  for (const g of groups) for (const t of g.items) groupOf.set(t.value, g.label);
  for (const t of everything) {
    if (t.workload) workloads.add(t.value);
    if (t.workload?.job?.result === JobResult.JobComplete) succeeded.add(t.value);
  }
  const finished = (t: Target) => succeeded.has(t.value) || (t.kind === "pod" && (t.reason === "Completed" || (!!t.owner && succeeded.has(t.owner))));
  const problem = (t: Target) => !finished(t) && isProblem(t);
  // A pod goes under the workload or Job that runs it, and a Job under its CronJob.
  const childrenOf = new Map<string, Target[]>();
  const scaledBy = new Map<string, KubeHPA>();
  const byNamespace = new Map<string, Target[]>();
  for (const t of everything) {
    if (t.owner && workloads.has(t.owner) && !finished(t)) push(childrenOf, t.owner, t);
    if (t.hpa && t.owner) scaledBy.set(t.owner, t.hpa);
    push(byNamespace, t.namespace, t);
  }
  // Counted over the scope, not the search or the kind: narrowing the list must not make the alarm go quiet.
  const unhealthy = groups.map((g) => ({ ...g, items: g.items.filter(problem) })).filter((g) => g.items.length);
  return { groupOf, workloads, finished, problem, childrenOf, scaledBy, byNamespace, unhealthy };
}

function push<K, V>(m: Map<K, V[]>, key: K, value: V) {
  const list = m.get(key);
  if (list) list.push(value);
  else m.set(key, [value]);
}

type View = { words: string[]; kind: string | null; problems: boolean; hidden: Record<string, boolean>; unfolded: Record<string, boolean> };

function lay(groups: TargetGroup[], index: ReturnType<typeof derive>, namespaces: string[], { words, kind, problems, hidden, unfolded }: View) {
  const seeking = words.length > 0 || !!kind;
  const filtering = seeking || problems;
  // What passes the kind, the search and the problems, by namespace and kind; every namespace in scope comes first, in its order.
  const shown = new Map<string, Map<string, Target[]>>(namespaces.map((ns) => [ns, new Map()]));
  const passing = new Set<string>();
  for (const g of groups) {
    if (kind && g.label !== kind) continue;
    for (const t of g.items) {
      if ((problems && !index.problem(t)) || !matches(words, t)) continue;
      passing.add(t.value);
      let kinds = shown.get(t.namespace);
      if (!kinds) shown.set(t.namespace, (kinds = new Map()));
      push(kinds, g.label, t);
    }
  }
  // A row stays in view while anything under it matches.
  const showing = new Map<string, boolean>();
  const shows = (t: Target): boolean => {
    let yes = showing.get(t.value);
    if (yes === undefined) showing.set(t.value, (yes = passing.has(t.value) || (index.childrenOf.get(t.value) ?? []).some(shows)));
    return yes;
  };
  const lines: Line[] = [];
  const row = (t: Target, depth: Depth = 0, fold?: boolean): Line => ({ type: "row", key: t.value, target: t, depth, done: index.finished(t), fold });
  for (const [ns, kinds] of shown) {
    if (kinds.size === 0) continue;
    const rows: Line[] = [];
    // The header counts what is shown, a workload kept for its matching pods included.
    const shownLive = new Set<string>();
    const live = (index.byNamespace.get(ns) ?? []).filter((t) => t.kind !== "cm" && t.kind !== "secret" && !index.finished(t));
    const tree = (t: Target, depth: Depth) => {
      if (!shows(t)) return;
      const children = (index.childrenOf.get(t.value) ?? []).filter(shows);
      shownLive.add(t.value);
      const open = filtering || !hidden[t.value];
      rows.push(row(t, depth, children.length ? open : undefined));
      if (open) for (const c of children) tree(c, (depth + 1) as Depth);
    };
    for (const t of live) {
      if (kind) {
        if (passing.has(t.value)) shownLive.add(t.value), rows.push(row(t));
      } else if (!(t.owner && index.workloads.has(t.owner))) tree(t, 0);
    }
    const running = new Map<string, number>();
    for (const value of shownLive) running.set(index.groupOf.get(value)!, (running.get(index.groupOf.get(value)!) ?? 0) + 1);
    lines.push({ type: "ns", key: `ns:${ns}`, ns, counts: counts(groups.filter((g) => !folded(g.label) && running.has(g.label)).map((g) => [g.label, running.get(g.label)!])) });
    lines.push(...rows);
    const foldLine = (key: string, list: Target[], label: string) => {
      if (!list.length) return;
      const open = seeking || !!unfolded[key];
      if (!seeking) lines.push({ type: "fold", key: `fold:${key}`, fold: key, label, open });
      if (open) for (const t of list) lines.push(row(t));
    };
    const all = [...kinds.values()].flat();
    const done = all.filter(index.finished);
    const reference = [...kinds].filter(([label]) => folded(label));
    foldLine(`${ns}:finished`, done, `${counts([["Jobs", done.filter((t) => t.kind === "job").length], ["Pods", done.filter((t) => t.kind === "pod").length]].filter(([, n]) => n) as [string, number][])} finished`);
    foldLine(ns, reference.flatMap(([, items]) => items), counts(reference.map(([label, items]) => [label, items.length])));
  }
  return { lines, total: passing.size, rows: lines.flatMap((l) => (l.type === "row" ? [l.target] : [])) };
}

// How far each level's name sits in; see indent.
const indentWidth: Record<Depth, number> = { 0: 20, 1: 39, 2: 59 };
const canvas = document.createElement("canvas").getContext("2d")!;
// Rows set their figures tabular, which canvas cannot, so a digit's width is read once from a hidden span and the rest
// is measured on canvas; 2px covers the kerning lost around the digits.
function measure(sizes: Map<string, number>, text: string, font: string) {
  const key = `${font}|${text}`;
  let width = sizes.get(key);
  if (width === undefined) {
    let digit = sizes.get(`digit|${font}`);
    if (digit === undefined) {
      const probe = document.createElement("span");
      probe.style.cssText = `position:absolute;visibility:hidden;white-space:pre;font:${font};font-variant-numeric:tabular-nums`;
      probe.textContent = "0000000000";
      document.body.append(probe);
      sizes.set(`digit|${font}`, (digit = probe.getBoundingClientRect().width / 10));
      probe.remove();
    }
    canvas.font = font;
    width = canvas.measureText(text.replace(/\d/g, "")).width + (text.match(/\d/g)?.length ?? 0) * digit + 2;
    sizes.set(key, width);
  }
  return width;
}

export function ClusterOverview({ cluster }: { cluster: Cluster }) {
  const namespaces = useQuery(namespacesQuery(cluster.id));
  const { data: config } = useQuery(configQuery);
  const { groups: liveGroups, error: listError, pending, fetching } = useTargets(cluster, true);
  const rescoping = useIsMutating({ mutationKey: scopeKey(cluster.id) }) > 0;
  const explicit = cluster.namespaces ?? noNamespaces;
  // Each kind is its own list and lands on its own; the rows change once all have, so a namespace fills in one step.
  // Until the first time they do, the Overview is loading rather than empty.
  const liveNamespaces = explicit.length ? explicit : (namespaces.data ?? noNamespaces);
  const settled = useRef({ groups: liveGroups, namespaces: liveNamespaces, loaded: false });
  if (!fetching && !namespaces.isFetching && !rescoping) {
    const s = settled.current;
    if (!s.loaded || s.groups !== liveGroups || s.namespaces !== liveNamespaces) settled.current = { groups: liveGroups, namespaces: liveNamespaces, loaded: true };
  }
  const { groups, loaded } = settled.current;
  const index = useMemo(() => derive(groups), [groups]);
  const metrics = useQuery(podMetricsQuery(cluster.id)).data;
  const pressure = (t: Target) => (t.kind === "pod" ? usagePressure(metrics?.get(podUsageKey(t.namespace, t.name))?.usage, t.limits) : undefined);
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

  const words = useMemo(() => needle.toLowerCase().split(/\s+/).filter(Boolean), [needle]);
  // A kind the scope no longer has lets go, rather than leave the list empty with no way to see why.
  const kinds = groups.filter((g) => g.items.length);
  const kind = kinds.some((g) => g.label === picked) ? picked : null;
  // Once nothing is unhealthy the filter lets go, rather than leave an empty list behind a line that is gone.
  const problems = onlyProblems && index.unhealthy.length > 0;
  const { lines, total, rows } = useMemo(
    () => lay(groups, index, settled.current.namespaces, { words, kind, problems, hidden: hiddenChildren, unfolded }),
    [groups, index, settled.current.namespaces, words, kind, problems, hiddenChildren, unfolded],
  );
  const shownRows = useRef<Target[]>(rows);
  shownRows.current = rows;
  useInspectorWalk(shownRows, inspecting, (t) => t.value, setInspecting);

  // What the verbs of a row show, worked out once rather than read by every row.
  const facts = useRowFacts(cluster);

  // The columns come from the data, not from cells, since most rows are not mounted: the name is as wide as its widest
  // name, but never less than 40% of the list (at most 12rem); Age as its widest age, and the verbs as the most a row shows.
  // Widths measured before the app's font arrives are the fallback's, so they are measured again once it has.
  const [fonts, setFonts] = useState(document.fonts.status);
  useEffect(() => void document.fonts.ready.then(() => setFonts("loaded")), []);
  const sizes = useMemo(() => new Map<string, number>(), [groups, fonts]);
  const columns = useMemo(() => {
    const family = getComputedStyle(document.body).fontFamily;
    let name = 0;
    let age = 0;
    let verbs = 0;
    for (const l of lines) {
      if (l.type !== "row") continue;
      const t = l.target;
      name = Math.max(name, indentWidth[l.depth] + measure(sizes, t.name, `${t.workload && !l.depth ? 500 : 400} 13px ${family}`));
      if (t.created) age = Math.max(age, measure(sizes, since(t.created), `400 13px ${family}`));
      const f = facts.get(t.value);
      if (f) verbs = Math.max(verbs, +!!f.forwarded + +!!f.stream + +!!f.shell);
    }
    // The list's 40% is of the rows' width, which their padding is laid outside of. The kind's badge reaches into the gutter
    // after it, so it sits 12px from its name; see KindBadge in TargetLine.
    const nameColumn = `minmax(min(12rem,calc(40% + 0.8rem)),${Math.ceil(name)}px)`;
    const verbsColumn = `${verbs * 24 + Math.max(0, verbs - 1) * 2}px`;
    return { "--cols": `40px ${nameColumn} minmax(8rem,1fr) ${verbsColumn}`, "--cols-wide": `28px ${nameColumn} 1fr ${Math.ceil(age)}px ${verbsColumn}` } as CSSProperties;
  }, [lines, facts, sizes]);

  const scroller = useRef<HTMLDivElement>(null);
  const headers = useMemo(() => lines.flatMap((l, i) => (l.type === "ns" ? [i] : [])), [lines]);
  const pinned = useRef(-1);
  const virtualizer = useVirtualizer({
    count: lines.length,
    getScrollElement: () => scroller.current,
    estimateSize: useCallback((i: number) => (lines[i]!.type === "ns" ? headerHeight : lineHeight), [lines]),
    getItemKey: useCallback((i: number) => lines[i]!.key, [lines]),
    overscan: 10,
    paddingEnd: 16,
    scrollPaddingStart: headerHeight,
    // The header of the namespace at the top is mounted however far its rows run, to stay pinned above them.
    rangeExtractor: useCallback(
      (range: Range) => {
        pinned.current = -1;
        for (const i of headers) if (i <= range.startIndex) pinned.current = i;
        return [...new Set([...(pinned.current >= 0 ? [pinned.current] : []), ...defaultRangeExtractor(range)])].sort((a, b) => a - b);
      },
      [headers],
    ),
  });
  // A row walked to, or opened from another tab, is scrolled into view; it may not be mounted, so the list scrolls to it.
  // One opened from another tab may land before the rows that hold it do, so it is looked for again as they change.
  const revealed = useRef<string | null>(null);
  useEffect(() => {
    if (!inspecting || revealed.current === inspecting.value) return;
    const i = lines.findIndex((l) => l.key === inspecting.value);
    if (i < 0) return;
    revealed.current = inspecting.value;
    virtualizer.scrollToIndex(i);
  }, [inspecting, lines]);

  const inspect = useCallback((t: Target) => setInspecting((cur) => (cur?.value === t.value ? null : t)), []);
  const toggleChildren = useCallback((value: string) => setHiddenChildren((h) => ({ ...h, [value]: !h[value] })), []);
  const selectTab = useUIStore((s) => s.selectTab);
  const openDock = useUIStore((s) => s.openDock);
  const showForwards = useCallback(() => selectTab("forwards"), [selectTab]);
  const openSession = useCallback((id: string) => openDock(cluster.id, id), [openDock, cluster.id]);

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

  // The pinned header is pushed up by the next namespace's as that one reaches the top, as a section's own header would be.
  const pushed = () => {
    const next = virtualizer.measurementsCache[headers[headers.indexOf(pinned.current) + 1] ?? -1];
    return next ? Math.min(0, next.start - (virtualizer.scrollOffset ?? 0) - headerHeight) : 0;
  };

  const line = (l: Line) => {
    if (l.type === "ns") {
      // Laid on the rows' columns: the namespace across kind and name, its counts where Details starts, so neither runs across a column's start.
      return (
        <h3 className="grid h-9 grid-cols-(--cols) items-baseline gap-x-5 bg-background px-4 pt-3 pb-1 text-sm font-medium whitespace-nowrap @2xl:grid-cols-(--cols-wide) @2xl:gap-x-8">
          <span className="col-span-2 truncate" title={l.ns}>
            {l.ns}
          </span>
          <span className="min-w-0 truncate text-xs font-normal text-muted-foreground" title={l.counts}>
            {l.counts}
          </span>
        </h3>
      );
    }
    if (l.type === "fold") {
      return (
        <button
          type="button"
          className="grid h-8 w-full grid-cols-[48px_1fr] items-center gap-3 px-4 text-left text-xs text-muted-foreground hover:bg-accent"
          aria-expanded={l.open}
          onClick={() => setUnfolded((u) => ({ ...u, [l.fold]: !l.open }))}
        >
          {l.open ? <CaretDownIcon className="size-3" /> : <CaretRightIcon className="size-3" />}
          <span className="pl-5">{l.label}</span>
        </button>
      );
    }
    const t = l.target;
    const f = facts.get(t.value);
    return (
      <TargetLine
        target={t}
        pressure={pressure(t)}
        scaledBy={index.scaledBy.get(t.value)}
        done={l.done}
        selected={t.value === inspecting?.value}
        onInspect={inspect}
        depth={l.depth}
        fold={l.fold}
        onFold={toggleChildren}
        forwarded={f?.forwarded}
        stream={f?.stream}
        shell={f?.shell}
        onForwards={showForwards}
        onOpen={openSession}
      />
    );
  };

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
      {index.unhealthy.length > 0 && (
        <div className="mx-4 mb-2 flex items-center gap-3 rounded-md bg-muted/60 py-1 pr-1 pl-3 text-sm text-muted-foreground">
          <span className="min-w-0 flex-1 truncate">{counts(tally(index.unhealthy))} not healthy</span>
          <Button
            variant="ghost"
            size="xs"
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
      {/* Every line lays its cells on the same columns, so they line up across rows: kind, name, details, age, verbs.
          The list holds every kind, so only columns every kind fills get one: a problem's badge follows the name, and Details is each
          kind's one key fact. Details takes what is left, so a long one has room and Age sits at the right edge, as in Lens.
          No header row names them: each column means one thing, which the kind badge and the value's own shape already say.
          When the list is narrow (a detail open beside it), Age steps aside with its header, the gutters shrink to 20px and Details
          keeps at least 8rem, so long pod names are cut before the squares and ports are. */}
      <div ref={scroller} style={columns} className={cn("min-h-0 flex-1 overflow-x-hidden overflow-y-auto transition-opacity", rescoping && "opacity-50")}>
        {total === 0 ? (
          <Empty className="justify-start border-0 pt-12">
            <EmptyHeader>
              <EmptyTitle>{!loaded ? "Loading…" : words.length ? `Nothing matches “${needle.trim()}”` : problems ? `No ${kind?.toLowerCase()} are unhealthy` : "Nothing to show"}</EmptyTitle>
              <EmptyDescription>
                {!loaded ? "Its objects appear as soon as the cluster answers." : words.length ? "Try a shorter name." : problems ? "Pick All to see the rest." : "This scope has no services, workloads, jobs, autoscalers, volume claims, pods, ingresses, configmaps or secrets."}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
            {virtualizer.getVirtualItems().map((item) => {
              const l = lines[item.index]!;
              return (
                <div
                  key={item.key}
                  className={cn("top-0 left-0 w-full", l.type === "ns" && "z-10")}
                  style={item.index === pinned.current ? { position: "sticky", top: pushed() } : { position: "absolute", transform: `translateY(${item.start}px)` }}
                >
                  {line(l)}
                </div>
              );
            })}
          </div>
        )}
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
// and hollow when it is not; why a rollout is stuck or a Job failed is its badge. The squares start where the column does,
// so rows line up; the count is in the title. An autoscaled workload runs on to its maximum, the replicas it may still add drawn
// as stubs on the baseline, and past 12 slots each square stands for a share.
const maxSlots = 12;
function Slots({ ready, of, max = 0, title }: { ready: number; of: number; max?: number; title: string }) {
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
            i < filled ? "h-2.5 bg-foreground/60" : i >= wanted ? "h-px bg-foreground/30" : "h-2.5 border border-foreground/45",
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
// A row reads nothing itself and renders again only when its own object or facts change: its handlers are the Overview's,
// the same on every render.
const TargetLine = memo(function TargetLine({
  target,
  pressure,
  scaledBy,
  done,
  selected,
  onInspect,
  depth,
  fold,
  onFold,
  forwarded,
  stream,
  shell,
  onForwards,
  onOpen,
}: {
  target: Target;
  pressure?: string;
  // The autoscaler of a workload, whose range its replica ticks run to.
  scaledBy?: KubeHPA;
  // Finished: a Job that succeeded or a pod of one, listed muted in the namespace's fold.
  done: boolean;
  selected: boolean;
  onInspect: (target: Target) => void;
  depth: Depth;
  // Whether its pods or runs are shown, undefined for a row with none.
  fold?: boolean;
  onFold: (value: string) => void;
  forwarded?: boolean;
  stream?: string;
  shell?: string;
  onForwards: () => void;
  onOpen: (sessionId: string) => void;
}) {
  const f = figure(target);
  const inspect = () => onInspect(target);
  const job = target.workload?.job;
  const reason = target.workload && workloadReason(target.workload);
  return (
    <div
      data-row={target.value}
      className={cn(
        "group grid h-8 grid-cols-(--cols) items-center gap-x-5 px-4 text-[13px] tabular-nums hover:bg-accent focus-within:bg-accent @2xl:grid-cols-(--cols-wide) @2xl:gap-x-8",
        selected && "bg-accent shadow-[inset_2px_0_0_var(--primary)]",
        done && "text-muted-foreground",
      )}
    >
      {/* 12px between the kind and its name, which belong together, rather than a whole gutter. */}
      <KindBadge kind={target.kind} className="-mr-2 @2xl:-mr-5" />
      <span className={cn("flex min-w-0 items-center self-stretch", indent[depth])}>
        {fold !== undefined && (
          <button
            type="button"
            className="-ml-5 flex w-5 shrink-0 text-muted-foreground hover:text-foreground"
            aria-expanded={fold}
            aria-label={`${fold ? "Hide" : "Show"} the ${target.kind === "cron" ? "runs" : "pods"} of ${target.name}`}
            onClick={() => onFold(target.value)}
          >
            {fold ? <CaretDownIcon className="size-3" /> : <CaretRightIcon className="size-3" />}
          </button>
        )}
        <button
          type="button"
          className={cn("truncate text-left text-sm hover:underline", target.workload && !depth && "font-medium")}
          title={target.config ? `What is in ${target.name}?` : target.kind === "svc" ? `Show ${target.name}` : target.ingress ? `What does ${target.name} reach?` : `Why is ${target.name} in this state?`}
          onClick={inspect}
        >
          {target.name}
        </button>
      </span>
      {/* Each kind's one key fact: a workload's replicas, a Job's completions or a pod's containers as squares, as kubectl counts them,
          a pod's restarts beside them once there are any, or the figure of a kind that runs nothing. */}
      {/* One line: the first fact is cut if it must be, and any after it either fits whole or wraps out of sight,
          so "10 restarts" is never read as "1". */}
      <span className="flex h-5 min-w-0 flex-wrap items-center gap-x-2.5 overflow-hidden">
        {/* What is wrong, or on its way, leads the column, so the name keeps the width; a healthy or finished object says nothing. */}
        {target.kind === "pod" && (
          <>
            <ReasonBadge reason={target.reason === "Completed" ? undefined : target.reason} className="cursor-pointer" onClick={inspect} />
            <ReasonBadge reason={pressure} className="cursor-pointer" title="Close to its limit" onClick={inspect} />
          </>
        )}
        {target.workload && <ReasonBadge reason={reason} className="cursor-pointer" title={job?.message || reason} onClick={inspect} />}
        {target.pvc && <ReasonBadge reason={pvcReason(target.pvc)} className="cursor-pointer" onClick={inspect} />}
        {target.hpa && <ReasonBadge reason={target.hpa.problem} className="cursor-pointer" onClick={inspect} />}
        {target.ingress && (
          <ReasonBadge reason={target.ingress.problem} className="cursor-pointer" title="Where the chain to its pods stops" onClick={inspect} />
        )}
        {target.workload?.rollout && (
          <Slots
            ready={target.workload.rollout.ready}
            of={target.workload.rollout.desired}
            max={scaledBy?.max}
            title={`${target.workload.rollout.ready} of ${target.workload.rollout.desired} ready${scaledBy ? `, autoscaled between ${scaledBy.min} and ${scaledBy.max}` : ""}`}
          />
        )}
        {job && (
          <Slots ready={job.succeeded} of={job.completions} title={`${job.succeeded} of ${job.completions} completed`} />
        )}
        {target.kind === "pod" && !isProblem(target) && target.reason !== "Completed" && (
          <Slots ready={target.ready ?? 0} of={target.containers.length} title={`${target.ready ?? 0} of ${target.containers.length} containers ready`} />
        )}
        {!!target.restarts && (
          <span className="shrink-0 text-muted-foreground" title={restartsLabel(target.restarts, target.lastRestart)}>
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
        <RowVerbs facts={{ forwarded, stream, shell }} onForwards={onForwards} onOpen={onOpen} />
      </span>
    </div>
  );
});

const scopeKey = (clusterId: string) => ["namespace-scope", clusterId];

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
    const width = (text: string, as: string) => ((canvas.font = as), canvas.measureText(text).width);
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
