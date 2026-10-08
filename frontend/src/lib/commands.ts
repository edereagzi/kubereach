import type { RefObject } from "react";
import { detectPlatform, formatForDisplay, useHotkeyRegistrations, useHotkeys, type Hotkey } from "@tanstack/react-hotkeys";
import { keyOwner, type Area, type Group } from "@/lib/key-owner";

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
  // Wails, Base UI, xterm or one element handles the key. The shortcuts list shows it, and it is always available.
  native?: true;
};

// Every key that the frontend handles through a Command is written here only.
export const commands = {
  "focus-cluster-filter": { name: "Filter clusters", group: "anywhere", keys: ["Mod+K"] },
  "show-shortcuts": { name: "Show shortcuts", group: "anywhere", keys: ["?"] },
  "close-dialog": { name: "Close a dialog or menu", group: "anywhere", keys: ["Escape"], native: true },
  quit: { name: "Quit Kubereach", group: "anywhere", keys: ["Mod+Q"], native: true },
  "zoom-in": { name: "Zoom in", group: "anywhere", keys: { mac: ["Mod++"], other: [] }, native: true },
  "zoom-out": { name: "Zoom out", group: "anywhere", keys: { mac: ["Mod+-"], other: [] }, native: true },
  "zoom-reset": { name: "Actual size", group: "anywhere", keys: { mac: ["Mod+0"], other: [] }, native: true },
  "full-screen": { name: "Full screen", group: "anywhere", keys: { mac: ["Control+Meta+F"], other: [] }, native: true },
  "filter-view": { name: "Filter the overview", group: "cluster", keys: ["/"] },
  "next-row": { name: "Next row", group: "detail", keys: ["ArrowDown"] },
  "previous-row": { name: "Previous row", group: "detail", keys: ["ArrowUp"] },
  "close-detail": { name: "Close the detail", group: "detail", keys: ["Escape"] },
  "toggle-panel": { name: "Show or hide the panel", group: "panel", keys: ["Mod+J"] },
  "select-log-lines": { name: "Select every line", group: "logs", keys: ["Mod+A"] },
  // Only macOS terminals clear on Cmd+K; elsewhere Ctrl+K belongs to the shell.
  "clear-terminal": { name: "Clear the screen", group: "terminal", keys: { mac: ["Mod+K"], other: [] }, native: true },
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

// Makes a Command available while the component is mounted. target limits it to keys pressed in that element.
export function useCommand(id: CommandId, run: (e: KeyboardEvent) => void, { enabled = true, target }: { enabled?: boolean; target?: RefObject<HTMLElement | null> } = {}) {
  const { name, group } = commands[id];
  useHotkeys(
    keysOf(id).map((hotkey) => ({
      hotkey,
      callback: (e: KeyboardEvent) => {
        const press = { key: e.key, meta: e.metaKey, ctrl: e.ctrlKey, alt: e.altKey, shift: e.shiftKey, handled: e.defaultPrevented };
        if (keyOwner(press, platform, focusArea(e), group) !== "app") return;
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
