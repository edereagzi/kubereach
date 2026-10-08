// Decides who gets a key event: the element that has the focus, or the app's Command. It has no imports, so
// key-owner.check.ts runs it with plain node.

export type Platform = "mac" | "windows" | "linux";
export type Area = "terminal" | "text" | "dialog" | "panel" | "other";
export type Group = "anywhere" | "cluster" | "detail" | "panel" | "logs" | "terminal" | "typing";
// handled: an element already acted on the key (defaultPrevented), for example a menu that closed on Esc.
export type Press = { key: string; meta: boolean; ctrl: boolean; alt: boolean; shift: boolean; handled: boolean };
export type Owner = "focus" | "app";

// On macOS a Terminal keeps these Cmd keys: clear, copy, paste, select all, and line editing.
const macTerminalKeys = new Set(["k", "c", "v", "a", "ArrowLeft", "ArrowRight", "Backspace"]);

export function keyOwner(p: Press, platform: Platform, area: Area, group: Group): Owner {
  const key = p.key.length === 1 ? p.key.toLowerCase() : p.key;
  if (p.handled) return "focus";
  // A Terminal's own Commands run in it.
  if (area === "terminal" && group === "terminal") return "app";
  // An open dialog or menu takes every key first.
  if (area === "dialog") return "focus";
  // The shell gets every key, except the Cmd keys that macOS terminals do not keep.
  // ponytail: Linux and Windows send no key from a Terminal to the app; ticket 03 adds their Ctrl+Shift rules.
  if (area === "terminal") return platform === "mac" && p.meta && !macTerminalKeys.has(key) ? "app" : "focus";
  if ((platform === "mac" ? p.meta : p.ctrl) || /^F\d+$/.test(key)) return "app";
  if (area === "text") return "focus";
  // The panel's lines and a tab list scroll and move with the arrows.
  if (area === "panel" && key.startsWith("Arrow")) return "focus";
  return "app";
}
