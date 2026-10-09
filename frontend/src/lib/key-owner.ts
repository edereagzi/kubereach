// Decides who gets a key event: the element that has the focus, or the app's Command. It has no imports, so
// key-owner.check.ts runs it with plain node.

export type Platform = "mac" | "windows" | "linux";
export type Area = "terminal" | "editor" | "text" | "dialog" | "panel" | "other";
export type Group = "anywhere" | "cluster" | "list" | "detail" | "yaml" | "panel" | "logs" | "terminal" | "search" | "typing";
// handled: an element already acted on the key (defaultPrevented), for example a menu that closed on Esc.
export type Press = { key: string; meta: boolean; ctrl: boolean; alt: boolean; shift: boolean; handled: boolean };
export type Owner = "focus" | "app";

export const pressOf = (e: KeyboardEvent): Press => ({ key: e.key, meta: e.metaKey, ctrl: e.ctrlKey, alt: e.altKey, shift: e.shiftKey, handled: e.defaultPrevented });

// On macOS a Terminal keeps these Cmd keys: clear, copy, paste, select all, search, and line editing.
const macTerminalKeys = new Set(["k", "c", "v", "a", "f", "ArrowLeft", "ArrowRight", "Backspace"]);
// On Linux and Windows a Terminal keeps these Ctrl+Shift keys: copy, paste, clear, search, and the shell's Ctrl+_ (undo)
// and Ctrl+@ (set mark).
const otherTerminalKeys = new Set(["c", "v", "k", "f", "_", "@"]);
// Shells do not use these Ctrl keys.
const otherAppKeys = /^([0-9=-]|PageUp|PageDown)$/;

export function keyOwner(p: Press, platform: Platform, area: Area, group: Group): Owner {
  const key = p.key.length === 1 ? p.key.toLowerCase() : p.key;
  if (p.handled) return "focus";
  // A Terminal's and the YAML editor's own Commands run in them.
  if ((area === "terminal" && group === "terminal") || (area === "editor" && group === "yaml")) return "app";
  // An open dialog or menu takes every key first.
  if (area === "dialog") return "focus";
  if (area === "terminal") return terminalKeyOwner(p, key, platform);
  if ((platform === "mac" ? p.meta : p.ctrl) || /^F\d+$/.test(key)) return "app";
  if (area === "text" || area === "editor") return "focus";
  // The panel's lines and a tab list scroll and move with the arrows.
  if (area === "panel" && key.startsWith("Arrow")) return "focus";
  return "app";
}

// The shell gets every key, except the Cmd keys that macOS terminals do not keep. On Linux and Windows, as in GNOME
// Terminal and Konsole, the user adds Shift to an app key.
function terminalKeyOwner(p: Press, key: string, platform: Platform): Owner {
  if (platform === "mac") return p.meta && !macTerminalKeys.has(key) ? "app" : "focus";
  if (key === "F11") return "app";
  // Ctrl+Alt is AltGr on Windows, which types characters.
  if (!p.ctrl || p.alt) return "focus";
  if (p.shift) return otherTerminalKeys.has(key) ? "focus" : "app";
  return otherAppKeys.test(key) ? "app" : "focus";
}
