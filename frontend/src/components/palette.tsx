import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { MagnifyingGlassIcon } from "@phosphor-icons/react";
import { Autocomplete } from "@base-ui/react/autocomplete";
import { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import type { Cluster } from "@bindings/internal/service";
import { Keys } from "@/components/shortcuts";
import { KindBadge, targetValue, useTargets, type Kind } from "@/components/targets";
import { ReachabilityDot } from "@/components/sidebar";
import { Dialog, DialogPortal } from "@/components/ui/dialog";
import { commands, useCommand, useRunnable, type CommandId } from "@/lib/commands";
import { configQuery, nodesQuery } from "@/queries";
import { mainTabs, useUIStore, type MainTab } from "@/store";
import { cn } from "@/lib/utils";

type Item = { value: string; label: string; run: () => void; cluster?: Cluster; kind?: Kind; namespace?: string; command?: CommandId };
type Group = { label: string; items: Item[] };

const viewNames: Record<MainTab, string> = { overview: "Overview", nodes: "Nodes", events: "Events", forwards: "Port forwards" };

// The Routes page hides the views, so going to a view also leaves it.
const goTo = (clusterId: string, tab: MainTab) => {
  const s = useUIStore.getState();
  if (s.routesOpen || s.selectedClusterId !== clusterId) s.selectCluster(clusterId);
  s.selectTab(tab);
};

// The palette lists actions only. One key is faster than the palette to move through rows, tabs, views and filters,
// and a closed tab must not hide the window. The Clusters replace "Select a cluster", which needs a digit key.
const notListed = new Set<CommandId>([
  "palette",
  "select-cluster",
  "next-row",
  "previous-row",
  "previous-view",
  "next-view",
  "previous-tab",
  "next-tab",
  "details-tab",
  "yaml-tab",
  "filter-view",
  "close-detail",
  "close-tab",
  "cancel-yaml",
  "select-log-lines",
]);

// ponytail: the list shows the first 100 matches, so that a Cluster with thousands of pods stays fast; type more to find the rest.
const limit = 100;

// Finds Clusters, the views and objects of the current Cluster, and the Commands that can run now. The chosen result
// runs when the palette is closed and the focus is back where it was, so a Command acts there.
export function Palette() {
  const [open, setOpen] = useState(false);
  const chosen = useRef<(() => void) | null>(null);
  const returnTo = useRef<Element | null>(null);
  useCommand("palette", () => {
    returnTo.current = document.activeElement;
    setOpen(true);
  });
  const choose = (item: Item) => {
    chosen.current = item.run;
    setOpen(false);
  };
  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      onOpenChangeComplete={(isOpen) => {
        if (isOpen || !chosen.current) return;
        // The dialog gives the focus back only when it unmounts, which is later.
        if (returnTo.current instanceof HTMLElement) returnTo.current.focus();
        chosen.current();
        chosen.current = null;
      }}
    >
      {/* A light shade with no blur: the window stays readable behind the palette. */}
      <DialogPortal>
        <DialogPrimitive.Backdrop className="fixed inset-0 z-50 bg-black/10 duration-100 data-open:animate-in data-open:fade-in-0 data-closed:animate-out data-closed:fade-out-0" />
        <DialogPrimitive.Popup
          aria-label="Command palette"
          className="fixed top-[14vh] left-1/2 z-50 flex w-[min(36rem,calc(100%-2rem))] -translate-x-1/2 flex-col overflow-hidden rounded-xl bg-popover text-popover-foreground shadow-2xl ring-1 ring-foreground/10 outline-none duration-100 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-[0.98] data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-[0.98]"
        >
          <Results open={open} onOpenChange={setOpen} onChoose={choose} />
        </DialogPrimitive.Popup>
      </DialogPortal>
    </Dialog>
  );
}

type ResultsProps = { open: boolean; onOpenChange: (open: boolean) => void; onChoose: (item: Item) => void };
type ListProps = ResultsProps & { query: string; onQueryChange: (query: string) => void };

function Results(props: ResultsProps) {
  const [query, setQuery] = useState("");
  const selectedClusterId = useUIStore((s) => s.selectedClusterId);
  const clusters = useQuery(configQuery).data?.clusters ?? [];
  const runs = useRunnable();
  const cluster = clusters.find((c) => c.id === selectedClusterId);
  const groups: Group[] = [];
  if (cluster) groups.push({ label: "Views", items: mainTabs.map((tab) => ({ value: `view:${tab}`, label: viewNames[tab], run: () => goTo(cluster.id, tab) })) });
  groups.push({ label: "Clusters", items: clusters.map((c) => ({ value: `cluster:${c.id}`, label: c.name, cluster: c, run: () => useUIStore.getState().selectCluster(c.id) })) });
  const commandItems = [...runs].filter(([id]) => !notListed.has(id)).map(([id, run]): Item => ({ value: `command:${id}`, label: commands[id].name, run, command: id }));
  groups.push({ label: "Commands", items: commandItems });
  const list = { ...props, query, onQueryChange: setQuery };
  return cluster ? <WithObjects {...list} cluster={cluster} groups={groups} /> : <List {...list} groups={groups} />;
}

// The objects that Overview and Nodes already have, in the current namespace scope. The palette asks the cluster for
// nothing. They show only while the user types: there are too many to look through.
function WithObjects({ cluster, groups, ...props }: ListProps & { cluster: Cluster; groups: Group[] }) {
  const { groups: targets } = useTargets(cluster, true, true);
  const nodes = useQuery({ ...nodesQuery(cluster.id), enabled: false }).data ?? [];
  if (!props.query.trim()) return <List {...props} groups={groups} />;
  const inspect = (kind: Kind, namespace: string, name: string) => () => {
    useUIStore.getState().requestInspect({ clusterId: cluster.id, kind, namespace, name });
    goTo(cluster.id, kind === "node" ? "nodes" : "overview");
  };
  const objects: Item[] = [
    // Workloads come first: they are what the user looks for most, also when a Service or Ingress has the same name.
    ...[...targets].sort((a, b) => +(b.label === "Workloads") - +(a.label === "Workloads")).flatMap((g) => g.items).map((t) => ({ value: t.value, label: t.name, kind: t.kind, namespace: t.namespace, run: inspect(t.kind, t.namespace, t.name) })),
    ...nodes.map((n) => ({ value: targetValue("node", "", n.name), label: n.name, kind: "node" as const, run: inspect("node", "", n.name) })),
  ];
  return <List {...props} groups={[...groups, { label: "Objects", items: objects }]} />;
}

// Autocomplete, not Combobox: it always highlights the first result, so Enter goes to it immediately.
function List({ open, onOpenChange, onChoose, query, onQueryChange, groups }: ListProps & { groups: Group[] }) {
  const current = useUIStore((s) => s.selectedClusterId);
  return (
    <Autocomplete.Root
      inline
      open={open}
      onOpenChange={onOpenChange}
      value={query}
      onValueChange={onQueryChange}
      items={groups.filter((g) => g.items.length)}
      itemToStringValue={(item: Item) => item.label}
      autoHighlight="always"
      keepHighlight
      limit={limit}
    >
      <label className="flex h-11 shrink-0 items-center gap-2.5 border-b px-3.5">
        <MagnifyingGlassIcon className="size-4 shrink-0 text-muted-foreground" />
        <Autocomplete.Input placeholder="Search clusters, views, objects and commands" className="h-full min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground" />
      </label>
      <Autocomplete.Empty>
        <p className="px-4 py-8 text-center text-sm text-muted-foreground">Nothing matches.</p>
      </Autocomplete.Empty>
      <Autocomplete.List className="max-h-[min(23rem,55vh)] scroll-py-1.5 overflow-y-auto overscroll-contain p-1.5 data-empty:hidden">
        {(group: Group) => (
          <Autocomplete.Group key={group.label} items={group.items} className="not-first:mt-1.5">
            <Autocomplete.GroupLabel className="px-2 pt-1 pb-1 text-[11px] font-semibold text-muted-foreground">{group.label}</Autocomplete.GroupLabel>
            <Autocomplete.Collection>
              {(item: Item) => (
                <Autocomplete.Item
                  key={item.value}
                  value={item}
                  onClick={() => onChoose(item)}
                  className={cn(
                    "flex h-8 cursor-default items-center gap-2.5 rounded-md px-2 text-sm outline-none select-none data-highlighted:bg-accent data-highlighted:text-accent-foreground",
                    item.cluster?.id === current && "font-medium text-primary data-highlighted:text-primary",
                  )}
                >
                  {item.cluster && <ReachabilityDot cluster={item.cluster} />}
                  {item.kind && <KindBadge kind={item.kind} />}
                  <span className="min-w-0 flex-1 truncate">{item.label}</span>
                  {item.namespace && <span className="max-w-[40%] truncate text-xs text-muted-foreground">{item.namespace}</span>}
                  {item.command && (
                    <span className="flex shrink-0 gap-1">
                      <Keys id={item.command} />
                    </span>
                  )}
                </Autocomplete.Item>
              )}
            </Autocomplete.Collection>
          </Autocomplete.Group>
        )}
      </Autocomplete.List>
    </Autocomplete.Root>
  );
}
