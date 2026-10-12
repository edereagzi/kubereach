import { beforeEach, describe, expect, test, vi } from "vite-plus/test";
import {
  LogSourceKind,
  State,
  type KubeEvent,
  type LogLine,
  type LogStatus,
  type ShellStatus,
  type TerminalStatus,
} from "@bindings/internal/service";
import type { useUIStore as Store } from "@/store";

// The store keeps the closed Shells and Terminals in the module, so each test loads a new store.
let useUIStore: typeof Store;
beforeEach(async () => {
  vi.resetModules();
  ({ useUIStore } = await import("@/store"));
});

const store = () => useUIStore.getState();

const logStatus = (id: string, state = State.StateConnected, error?: string): LogStatus => ({
  id,
  source: { clusterId: "c1", namespace: "shop", kind: LogSourceKind.LogSourcePod, name: "web" },
  pods: null,
  containers: null,
  allContainers: null,
  state,
  error,
});

const shellStatus = (id: string, state = State.StateConnected): ShellStatus => ({
  id,
  target: { clusterId: "c1", namespace: "shop", pod: "web", container: "app" },
  shell: "sh",
  state,
});

const terminalStatus = (id: string, state = State.StateConnected): TerminalStatus => ({
  id,
  clusterId: "c1",
  shell: "zsh",
  namespace: "",
  state,
});

const lines = (from: number, count: number): LogLine[] =>
  Array.from({ length: count }, (_, i) => ({
    pod: "web",
    container: "app",
    time: "",
    text: `line ${from + i}`,
  }));

const event = (id: string, minute: number, count = 1): KubeEvent => ({
  id,
  kind: "Pod",
  namespace: "shop",
  name: "web",
  type: "Normal",
  reason: "Pulled",
  message: "",
  count,
  time: new Date(Date.UTC(2026, 0, 1, 0, minute)).toISOString(),
});

describe("log buffer", () => {
  test("keeps only the newest lines up to the limit", () => {
    store().appendLogs({ streamId: "l1", lines: lines(0, 50_000) });
    store().appendLogs({ streamId: "l1", lines: lines(50_000, 10) });
    const kept = store().logBuffers["l1"]!.lines;
    expect(kept).toHaveLength(50_000);
    expect(kept[0]!.text).toBe("line 10");
    expect(kept.at(-1)!.text).toBe("line 50009");
  });
});

describe("event buffer", () => {
  test("shows a repeated event one time, with its last delivery", () => {
    store().appendEvents({ streamId: "e1", events: [event("a", 1), event("b", 2)] });
    store().appendEvents({ streamId: "e1", events: [event("a", 3, 2)] });
    expect(store().eventBuffers["e1"]!.events.map((e) => [e.id, e.count])).toEqual([
      ["a", 2],
      ["b", 1],
    ]);
  });

  test("keeps the newest first", () => {
    store().appendEvents({ streamId: "e1", events: [event("a", 1), event("c", 3)] });
    store().appendEvents({ streamId: "e1", events: [event("b", 2)] });
    expect(store().eventBuffers["e1"]!.events.map((e) => e.id)).toEqual(["c", "b", "a"]);
  });

  test("keeps no more than the limit, newest first", () => {
    const events = Array.from({ length: 5_010 }, (_, i) => event(`e${i}`, i));
    store().appendEvents({ streamId: "e1", events });
    const kept = store().eventBuffers["e1"]!.events;
    expect(kept).toHaveLength(5_000);
    expect(kept[0]!.id).toBe("e5009");
    expect(kept.at(-1)!.id).toBe("e10");
  });
});

describe("log stream", () => {
  test("that is stopped goes away with its lines and its panel tab", () => {
    store().setLogStatus(logStatus("l1"));
    store().appendLogs({ streamId: "l1", lines: lines(0, 3) });
    store().setLogStatus(logStatus("l1", State.StateStopped));
    expect(store().logStreams["l1"]).toBeUndefined();
    expect(store().logBuffers["l1"]).toBeUndefined();
    expect(store().dockOrder).toEqual([]);
  });

  test("that ended on its own stays with its error", () => {
    store().setLogStatus(logStatus("l1"));
    store().setLogStatus(logStatus("l1", State.StateError, "pod deleted"));
    expect(store().logStreams["l1"]!.error).toBe("pod deleted");
    expect(store().dockOrder).toEqual(["l1"]);
  });
});

describe("a closed Shell or Terminal", () => {
  test("a Shell stays closed when its last status arrives later", () => {
    store().setShellStatus(shellStatus("s-closed"));
    store().closeShell("s-closed");
    store().setShellStatus(shellStatus("s-closed", State.StateStopped));
    expect(store().shellSessions["s-closed"]).toBeUndefined();
    expect(store().dockOrder).toEqual([]);
  });

  test("a Terminal stays closed when its last status arrives later", () => {
    store().setTerminalStatus(terminalStatus("t-closed"));
    store().closeTerminal("t-closed");
    store().setTerminalStatus(terminalStatus("t-closed", State.StateStopped));
    expect(store().terminalSessions["t-closed"]).toBeUndefined();
    expect(store().dockOrder).toEqual([]);
  });
});

describe("panel tab", () => {
  test("a new Shell, Terminal or log stream becomes the selected tab of its Cluster", () => {
    store().setShellStatus(shellStatus("s1"));
    expect(store().dockPicks["c1"]).toBe("s1");
    store().setTerminalStatus(terminalStatus("t1"));
    expect(store().dockPicks["c1"]).toBe("t1");
    store().openLogTab(logStatus("l1"));
    expect(store().dockPicks["c1"]).toBe("l1");
    store().setLogStatus(logStatus("l2"));
    expect(store().dockPicks["c1"]).toBe("l2");
    expect(store().dockOrder).toEqual(["s1", "t1", "l1", "l2"]);
  });

  test("a log stream that replaces another takes the place of the old tab", () => {
    store().setShellStatus(shellStatus("s1"));
    store().openLogTab(logStatus("old"));
    store().setTerminalStatus(terminalStatus("t1"));
    store().openLogTab(logStatus("new"), "old");
    store().setLogStatus(logStatus("old", State.StateStopped));
    expect(store().dockOrder).toEqual(["s1", "new", "t1"]);
    expect(store().dockPicks["c1"]).toBe("new");
  });
});
