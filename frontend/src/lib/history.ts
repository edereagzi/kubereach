import { useUIStore, type Detail, type MainTab } from "@/store";

// A place is what the user saw: the Cluster, its view and the open detail.
type Place = { clusterId: string; tab: MainTab; detail: Detail | null };

// In memory only, as in a browser tab.
const max = 50;
let places: Place[] = [];
let at = -1;
let mode: "push" | "replace" | "none" = "push";

const same = (a: Place, b: Place) =>
  a.clusterId === b.clusterId && a.tab === b.tab && a.detail?.kind === b.detail?.kind && a.detail?.namespace === b.detail?.namespace && a.detail?.name === b.detail?.name;

// Each change of the place adds a new place and removes the forward places.
useUIStore.subscribe((s) => {
  if (mode === "none" || !s.selectedClusterId) return;
  const place = { clusterId: s.selectedClusterId, tab: s.activeTab, detail: s.detail };
  if (places[at] && same(places[at], place)) return;
  if (mode === "replace" && at >= 0) {
    places[at] = place;
    return;
  }
  places = [...places.slice(0, at + 1), place].slice(-max);
  at = places.length - 1;
});

// Walking the rows replaces the current place, so one back undoes the walk.
export const replacePlace = (change: () => void) => withMode("replace", change);

function withMode(m: typeof mode, change: () => void) {
  mode = m;
  try {
    change();
  } finally {
    mode = "push";
  }
}

// Goes back (-1) or forward (1), past the places whose Cluster is gone. A detail whose object is gone does not open.
// The Routes page is not a place, so back from it goes to the current place.
export function go(step: -1 | 1, exists: (clusterId: string) => boolean) {
  let i = useUIStore.getState().routesOpen && step < 0 ? at : at + step;
  while (places[i] && !exists(places[i]!.clusterId)) i += step;
  const place = places[i];
  if (!place) return;
  at = i;
  withMode("none", () => useUIStore.setState({ selectedClusterId: place.clusterId, activeTab: place.tab, detail: place.detail, routesOpen: false }));
}
