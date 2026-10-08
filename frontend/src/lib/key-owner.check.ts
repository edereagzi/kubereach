/// <reference types="node" />
// Run with `pnpm test`.
import assert from "node:assert/strict";
import { keyOwner, type Area, type Group, type Owner, type Platform, type Press } from "./key-owner.ts";

const press = (key: string, mods = ""): Press => ({
  key,
  meta: mods.includes("meta"),
  ctrl: mods.includes("ctrl"),
  alt: mods.includes("alt"),
  shift: mods.includes("shift"),
  handled: mods.includes("handled"),
});

const cases: [string, Press, Platform, Area, Group, Owner][] = [
  // An open dialog or menu takes its keys first.
  ["↓ with a confirmation open", press("ArrowDown"), "mac", "dialog", "detail", "focus"],
  ["Esc with a menu open", press("Escape"), "linux", "dialog", "detail", "focus"],
  ["⌘J with a dialog open", press("j", "meta"), "mac", "dialog", "panel", "focus"],
  ["? with a dialog open", press("?", "shift"), "mac", "dialog", "anywhere", "focus"],
  ["Esc that closed a menu", press("Escape", "handled"), "mac", "other", "detail", "focus"],

  // Text fields keep plain keys and Esc; Cmd or Ctrl keys go to the app.
  ["/ in a text field", press("/"), "mac", "text", "cluster", "focus"],
  ["? in a text field", press("?", "shift"), "windows", "text", "anywhere", "focus"],
  ["↓ in a text field", press("ArrowDown"), "mac", "text", "detail", "focus"],
  ["Esc in the log filter", press("Escape"), "mac", "text", "detail", "focus"],
  // The YAML editor is contenteditable, so it is a text field.
  ["Esc in the YAML editor", press("Escape"), "mac", "text", "detail", "focus"],
  ["⌘K in a text field", press("k", "meta"), "mac", "text", "anywhere", "app"],
  ["Ctrl+J in a text field", press("j", "ctrl"), "linux", "text", "panel", "app"],

  // Elsewhere the app gets the key.
  ["Esc on the list", press("Escape"), "mac", "other", "detail", "app"],
  ["↓ on the list", press("ArrowDown"), "linux", "other", "detail", "app"],
  ["? on the list", press("?", "shift"), "mac", "other", "anywhere", "app"],
  ["/ on the list", press("/"), "windows", "other", "cluster", "app"],
  ["⌃A on macOS on the list", press("a", "ctrl"), "mac", "other", "logs", "app"],

  // The panel keeps the arrows for its lines; Esc still closes the detail.
  ["↓ in the logs", press("ArrowDown"), "mac", "panel", "detail", "focus"],
  ["Esc in the logs", press("Escape"), "mac", "panel", "detail", "app"],
  ["⌘A in the logs", press("a", "meta"), "mac", "panel", "logs", "app"],

  // A Terminal on macOS keeps its own Cmd keys and every key without Cmd.
  ["⌘K in a Terminal on macOS", press("k", "meta"), "mac", "terminal", "anywhere", "focus"],
  ["⌘C in a Terminal on macOS", press("c", "meta"), "mac", "terminal", "detail", "focus"],
  ["⌘⌫ in a Terminal on macOS", press("Backspace", "meta"), "mac", "terminal", "detail", "focus"],
  ["⌥← in a Terminal on macOS", press("ArrowLeft", "alt"), "mac", "terminal", "anywhere", "focus"],
  ["⌘J in a Terminal on macOS", press("j", "meta"), "mac", "terminal", "panel", "app"],
  ["Esc in a Terminal", press("Escape"), "mac", "terminal", "detail", "focus"],
  ["⇧J in a Terminal", press("J", "shift"), "mac", "terminal", "detail", "focus"],

  // A Terminal on Linux and Windows keeps every key.
  ["Ctrl+J in a Terminal on Linux", press("j", "ctrl"), "linux", "terminal", "panel", "focus"],
  ["Ctrl+K in a Terminal on Windows", press("k", "ctrl"), "windows", "terminal", "anywhere", "focus"],
  ["Alt+← in a Terminal", press("ArrowLeft", "alt"), "linux", "terminal", "anywhere", "focus"],

  // An area's own Commands run in it.
  ["⌘K clears a Terminal", press("k", "meta"), "mac", "terminal", "terminal", "app"],
];

for (const [name, p, platform, area, group, owner] of cases) assert.equal(keyOwner(p, platform, area, group), owner, name);
console.log(`${cases.length} key rules pass`);
