import type { RefObject } from "react";
import { detectPlatform, formatForDisplay, useHotkeyRegistrations, useHotkeys, type Hotkey } from "@tanstack/react-hotkeys";
import { keyOwner, pressOf, type Area, type Group } from "@/lib/key-owner";

declare module "@tanstack/react-hotkeys" {
  interface HotkeyMeta {
    command?: CommandId;
  }
}

export const groups: Record<Group, string> = {
  anywhere: "Anywhere",
  cluster: "In a cluster",
  detail: "With a detail open",
  panel: "Panel",
  logs: "In logs",
  terminal: "In a Terminal or Shell",
  typing: "While typing",
};

type Command = {
  name: string;
  group: Group;
  // The same keys on every platform (Mod is ⌘ on macOS and Ctrl elsewhere), or different ones. A platform with no key does not have the Command.
  keys: Hotkey[] | { mac: Hotkey[]; other: Hotkey[] };
  // Wails, Base UI, xterm or one element handles the key, at least on macOS. The shortcuts list shows it, and it is always available.
  native?: true;
};

// Every key that the frontend handles through a Command is written here only.
export const commands = {
  "focus-cluster-filter": { name: "Filter clusters", group: "anywhere", keys: ["Mod+K"] },
  "show-shortcuts": { name: "Show shortcuts", group: "anywhere", keys: ["?"] },
  "close-dialog": { name: "Close a dialog or menu", group: "anywhere", keys: ["Escape"], native: true },
  quit: { name: "Quit Kubereach", group: "anywhere", keys: ["Mod+Q"], native: true },
  "select-cluster": { name: "Select a cluster (9 is the last)", group: "anywhere", keys: ["Mod+1", "Mod+2", "Mod+3", "Mod+4", "Mod+5", "Mod+6", "Mod+7", "Mod+8", "Mod+9"] },
  refresh: { name: "Refresh data", group: "anywhere", keys: ["Mod+R"] },
  "open-settings": { name: "Open Settings", group: "anywhere", keys: ["Mod+,"] },
  // The macOS View menu handles these; elsewhere the frontend calls the Wails runtime.
  "zoom-in": { name: "Zoom in", group: "anywhere", keys: { mac: ["Mod++"], other: ["Mod+="] }, native: true },
  "zoom-out": { name: "Zoom out", group: "anywhere", keys: ["Mod+-"], native: true },
  "zoom-reset": { name: "Actual size", group: "anywhere", keys: ["Mod+0"], native: true },
  "full-screen": { name: "Full screen", group: "anywhere", keys: { mac: ["Control+Meta+F"], other: ["F11"] }, native: true },
  "filter-view": { name: "Filter the overview", group: "cluster", keys: ["/"] },
  "next-row": { name: "Next row", group: "detail", keys: ["ArrowDown"] },
  "previous-row": { name: "Previous row", group: "detail", keys: ["ArrowUp"] },
  "close-detail": { name: "Close the detail", group: "detail", keys: ["Escape"] },
  "toggle-panel": { name: "Show or hide the panel", group: "panel", keys: ["Mod+J"] },
  "new-terminal": { name: "New Terminal", group: "panel", keys: ["Mod+T"] },
  "close-tab": { name: "Close the panel tab, or hide the window", group: "panel", keys: ["Mod+W"] },
  "previous-tab": { name: "Previous panel tab", group: "panel", keys: { mac: ["Mod+Shift+["], other: ["Mod+PageUp"] } },
  "next-tab": { name: "Next panel tab", group: "panel", keys: { mac: ["Mod+Shift+]"], other: ["Mod+PageDown"] } },
  "select-log-lines": { name: "Select every line", group: "logs", keys: ["Mod+A"] },
  // The xterm key handler runs these. On Linux and Windows the Ctrl keys belong to the shell, so these keys have Shift.
  "clear-terminal": { name: "Clear the screen", group: "terminal", keys: { mac: ["Mod+K"], other: ["Mod+Shift+K"] }, native: true },
  "copy-terminal": { name: "Copy", group: "terminal", keys: { mac: ["Mod+C"], other: ["Mod+Shift+C"] }, native: true },
  "paste-terminal": { name: "Paste", group: "terminal", keys: { mac: ["Mod+V"], other: ["Mod+Shift+V"] }, native: true },
  "clear-cluster-filter": { name: "Clear the cluster filter", group: "typing", keys: ["Escape"], native: true },
  "cancel-port-edit": { name: "Cancel a local port change", group: "typing", keys: ["Escape"], native: true },
} satisfies Record<string, Command>;

export type CommandId = keyof typeof commands;

export const platform = detectPlatform();

export const keysOf = (id: CommandId): Hotkey[] => {
  const keys: Command["keys"] = commands[id].keys;
  return Array.isArray(keys) ? keys : platform === "mac" ? keys.mac : keys.other;
};

export const formatKey = (key: Hotkey) => formatForDisplay(key, { platform, separatorToken: platform === "mac" ? "" : null });

// A Command's first key for a tooltip or a menu row.
export const keyLabel = (id: CommandId) => {
  const [key] = keysOf(id);
  return key ? formatKey(key) : "";
};

function focusArea(e: KeyboardEvent): Area {
  // A Base UI Select keeps its closed list in the page, marked data-closed.
  if ([...document.querySelectorAll("[role=dialog], [role=alertdialog], [role=menu], [role=listbox]")].some((el) => !el.closest("[data-closed]"))) return "dialog";
  const el = e.target instanceof HTMLElement ? e.target : null;
  if (el?.closest(".xterm")) return "terminal";
  if (el?.closest("input, textarea, select") || el?.isContentEditable) return "text";
  if (el?.closest("[data-panel], [role=tablist]")) return "panel";
  return "other";
}

// On Linux and Windows Ctrl+Shift+key also runs the Command for Ctrl+key, when no Command has Ctrl+Shift+key. In a
// Terminal, where Ctrl+key goes to the shell, this is how the user gets the app key.
const catalogKeys = new Set((Object.keys(commands) as CommandId[]).flatMap(keysOf));
const withShift = (keys: Hotkey[]) => {
  if (platform === "mac") return keys;
  const shifted = keys.filter((k) => k.startsWith("Mod+") && !k.includes("Shift")).map((k) => k.replace("Mod+", "Mod+Shift+") as Hotkey);
  return [...keys, ...shifted.filter((k) => !catalogKeys.has(k))];
};

// Makes a Command available while the component is mounted. target limits it to keys pressed in that element.
export function useCommand(id: CommandId, run: (e: KeyboardEvent) => void, { enabled = true, target }: { enabled?: boolean; target?: RefObject<HTMLElement | null> } = {}) {
  const { name, group } = commands[id];
  useHotkeys(
    withShift(keysOf(id)).map((hotkey) => ({
      hotkey,
      callback: (e: KeyboardEvent) => {
        if (keyOwner(pressOf(e), platform, focusArea(e), group) !== "app") return;
        e.preventDefault();
        run(e);
      },
    })),
    { enabled, target, meta: { name, command: id }, preventDefault: false, stopPropagation: false, ignoreInputs: false, conflictBehavior: "allow" },
  );
}

// Tells whether a Command can run now: it is native, or a mounted component made it available and enabled it.
export function useAvailable() {
  const on = new Set(useHotkeyRegistrations().hotkeys.flatMap((h) => (h.options.enabled ? [h.options.meta?.command] : [])));
  return (id: CommandId) => "native" in commands[id] || on.has(id);
}
