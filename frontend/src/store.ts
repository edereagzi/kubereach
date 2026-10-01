import { create } from "zustand";
import type { Kind } from "@/components/targets";
import {
  State,
  type EventBatch,
  type EventStatus,
  type ForwardStatus,
  type HostKeyPrompt,
  type ImportPreview,
  type KubeEvent,
  type LogBatch,
  type LogLine,
  type LogStatus,
  type RouteStatus,
  type ShellStatus,
  type TerminalStatus,
} from "@bindings/internal/service";

// ponytail: one flat buffer per stream, trimmed from the front; a ring buffer if the splice ever shows up in profiles.
const maxLogLines = 50_000;

// Lines are appended in place and version bumps notify subscribers, so a batch never copies the buffer.
type LogBuffer = { lines: LogLine[]; version: number };

// ponytail: the newest events of one stream, trimmed from the end; enough for an incident's hour.
const maxEvents = 5_000;

type EventBuffer = { events: KubeEvent[]; version: number };

// A closed session's stop is still on its way, so its last transitions arrive after it left the store.
const closedSessions = new Set<string>();

export type MainTab = "overview" | "nodes" | "events" | "forwards";
const dockHeightKey = "dockHeight";

// An object another tab asks the Overview to open the detail of.
export type InspectRequest = { clusterId: string; kind: Kind; namespace: string; name: string };

interface UIState {
  selectedClusterId: string | null;
  selectCluster: (id: string | null) => void;
  // The Routes page takes the main area until a Cluster is selected again.
  routesOpen: boolean;
  openRoutes: () => void;
  activeTab: MainTab;
  selectTab: (tab: MainTab) => void;
  // Log streams and shells are sessions that outlive a view, so each is a tab of a dock under whichever view is open.
  // The order is the order they were opened in; a Cluster's picked tab is its latest session until another is picked.
  dockOrder: string[];
  dockPicks: Record<string, string>;
  openDock: (clusterId: string, sessionId: string) => void;
  // A stream just started is its Cluster's tab at once, before its first event, in place of the tab it replaces.
  openLogTab: (status: LogStatus, replaces?: string) => void;
  dockOpen: boolean;
  dockHeight: number;
  setDockOpen: (open: boolean) => void;
  setDockHeight: (height: number) => void;
  // A Route another part of the window asks the sidebar to connect, where its credential and host key prompts live.
  connectRequest: string | null;
  requestConnect: (routeId: string | null) => void;
  // Why a Connect call was refused before the Route started, which its status never carries.
  connectErrors: Record<string, string>;
  setConnectError: (routeId: string, message: string | null) => void;
  routeStatuses: Record<string, RouteStatus>;
  setRouteStatus: (status: RouteStatus) => void;
  forwardStatuses: Record<string, ForwardStatus>;
  setForwardStatus: (status: ForwardStatus) => void;
  logStreams: Record<string, LogStatus>;
  setLogStatus: (status: LogStatus) => void;
  logBuffers: Record<string, LogBuffer>;
  appendLogs: (batch: LogBatch) => void;
  clearLogs: (streamId: string) => void;
  logWrap: boolean;
  toggleLogWrap: () => void;
  logTimestamps: boolean;
  toggleLogTimestamps: () => void;
  eventStreams: Record<string, EventStatus>;
  setEventStatus: (status: EventStatus) => void;
  eventBuffers: Record<string, EventBuffer>;
  appendEvents: (batch: EventBatch) => void;
  inspectRequest: InspectRequest | null;
  requestInspect: (request: InspectRequest | null) => void;
  shellSessions: Record<string, ShellStatus>;
  setShellStatus: (status: ShellStatus) => void;
  // An ended session stays on screen until closed, so its last output can be read.
  closeShell: (id: string) => void;
  terminalSessions: Record<string, TerminalStatus>;
  setTerminalStatus: (status: TerminalStatus) => void;
  // An ended Terminal stays on screen until closed too.
  closeTerminal: (id: string) => void;
  hostKeyPrompts: HostKeyPrompt[];
  addHostKeyPrompt: (prompt: HostKeyPrompt) => void;
  removeHostKeyPrompt: (routeId: string) => void;
  // Export files waiting for the import dialog, shown one at a time.
  importPreviews: ImportPreview[];
  pushImportPreviews: (previews: ImportPreview[]) => void;
  shiftImportPreview: () => void;
}

export const useUIStore = create<UIState>((set) => ({
  selectedClusterId: null,
  selectCluster: (id) => set({ selectedClusterId: id, routesOpen: false }),
  routesOpen: false,
  openRoutes: () => set({ routesOpen: true }),
  activeTab: "overview",
  selectTab: (tab) => set({ activeTab: tab }),
  dockOrder: [],
  dockPicks: {},
  openDock: (clusterId, sessionId) => set((s) => ({ dockPicks: { ...s.dockPicks, [clusterId]: sessionId }, dockOpen: true })),
  openLogTab: (status, replaces) =>
    set((s) => {
      const dockOrder = s.dockOrder.filter((id) => id !== status.id);
      const at = replaces ? dockOrder.indexOf(replaces) : -1;
      dockOrder.splice(at < 0 ? dockOrder.length : at, 0, status.id);
      return {
        logStreams: s.logStreams[status.id] ? s.logStreams : { ...s.logStreams, [status.id]: status },
        dockOrder,
        dockPicks: { ...s.dockPicks, [status.source.clusterId]: status.id },
        dockOpen: true,
      };
    }),
  dockOpen: false,
  dockHeight: Number(localStorage.getItem(dockHeightKey)) || 320,
  setDockOpen: (open) => set({ dockOpen: open }),
  setDockHeight: (height) => {
    localStorage.setItem(dockHeightKey, String(height));
    set({ dockHeight: height });
  },
  connectRequest: null,
  requestConnect: (routeId) => set({ connectRequest: routeId }),
  connectErrors: {},
  setConnectError: (routeId, message) =>
    set((s) => {
      const connectErrors = { ...s.connectErrors };
      if (message) connectErrors[routeId] = message;
      else delete connectErrors[routeId];
      return { connectErrors };
    }),
  routeStatuses: {},
  setRouteStatus: (status) =>
    set((s) => ({ routeStatuses: { ...s.routeStatuses, [status.routeId]: status } })),
  forwardStatuses: {},
  // A forward that is switched off is forgotten by the backend, so it leaves the list.
  setForwardStatus: (status) =>
    set((s) => {
      const forwardStatuses = { ...s.forwardStatuses };
      if (status.state === State.StateStopped) delete forwardStatuses[status.forward.id];
      else forwardStatuses[status.forward.id] = status;
      return { forwardStatuses };
    }),
  logStreams: {},
  // A stopped stream is forgotten with its lines; one that ended on its own stays in error until it is stopped.
  setLogStatus: (status) =>
    set((s) => {
      const logStreams = { ...s.logStreams };
      const logBuffers = { ...s.logBuffers };
      if (status.state === State.StateStopped) {
        if (!logStreams[status.id]) return {};
        delete logStreams[status.id];
        delete logBuffers[status.id];
        return { logStreams, logBuffers, dockOrder: s.dockOrder.filter((id) => id !== status.id) };
      }
      logStreams[status.id] = status;
      if (s.logStreams[status.id]) return { logStreams };
      return { logStreams, ...addDockTab(s, status.source.clusterId, status.id) };
    }),
  logBuffers: {},
  logWrap: false,
  toggleLogWrap: () => set((s) => ({ logWrap: !s.logWrap })),
  logTimestamps: false,
  toggleLogTimestamps: () => set((s) => ({ logTimestamps: !s.logTimestamps })),
  appendLogs: ({ streamId, lines }) =>
    set((s) => {
      const buffer = s.logBuffers[streamId] ?? { lines: [], version: 0 };
      buffer.lines.push(...(lines ?? []));
      if (buffer.lines.length > maxLogLines) buffer.lines.splice(0, buffer.lines.length - maxLogLines);
      return { logBuffers: { ...s.logBuffers, [streamId]: { lines: buffer.lines, version: buffer.version + 1 } } };
    }),
  clearLogs: (streamId) =>
    set((s) => ({ logBuffers: { ...s.logBuffers, [streamId]: { lines: [], version: (s.logBuffers[streamId]?.version ?? 0) + 1 } } })),
  eventStreams: {},
  setEventStatus: (status) =>
    set((s) => {
      const eventStreams = { ...s.eventStreams };
      const eventBuffers = { ...s.eventBuffers };
      if (status.state === State.StateStopped) {
        delete eventStreams[status.id];
        delete eventBuffers[status.id];
      } else eventStreams[status.id] = status;
      return { eventStreams, eventBuffers };
    }),
  eventBuffers: {},
  // A repeated event replaces its earlier delivery by ID; the buffer stays newest first.
  appendEvents: ({ streamId, events }) =>
    set((s) => {
      const buffer = s.eventBuffers[streamId] ?? { events: [], version: 0 };
      const byId = new Map(buffer.events.map((e) => [e.id, e]));
      for (const e of events ?? []) byId.set(e.id, e);
      const merged = [...byId.values()].sort((a, b) => Date.parse(b.time) - Date.parse(a.time)).slice(0, maxEvents);
      return { eventBuffers: { ...s.eventBuffers, [streamId]: { events: merged, version: buffer.version + 1 } } };
    }),
  inspectRequest: null,
  requestInspect: (request) => set({ inspectRequest: request }),
  shellSessions: {},
  setShellStatus: (status) =>
    set((s) => {
      if (closedSessions.has(status.id)) return {};
      const shellSessions = { ...s.shellSessions, [status.id]: status };
      if (s.shellSessions[status.id]) return { shellSessions };
      return { shellSessions, ...addDockTab(s, status.target.clusterId, status.id) };
    }),
  closeShell: (id) =>
    set((s) => {
      if (!s.shellSessions[id]) return {};
      closedSessions.add(id);
      const shellSessions = { ...s.shellSessions };
      delete shellSessions[id];
      return { shellSessions, dockOrder: s.dockOrder.filter((x) => x !== id) };
    }),
  terminalSessions: {},
  setTerminalStatus: (status) =>
    set((s) => {
      if (closedSessions.has(status.id)) return {};
      const terminalSessions = { ...s.terminalSessions, [status.id]: status };
      if (s.terminalSessions[status.id]) return { terminalSessions };
      return { terminalSessions, ...addDockTab(s, status.clusterId, status.id) };
    }),
  closeTerminal: (id) =>
    set((s) => {
      if (!s.terminalSessions[id]) return {};
      closedSessions.add(id);
      const terminalSessions = { ...s.terminalSessions };
      delete terminalSessions[id];
      return { terminalSessions, dockOrder: s.dockOrder.filter((x) => x !== id) };
    }),
  hostKeyPrompts: [],
  addHostKeyPrompt: (prompt) => set((s) => ({ hostKeyPrompts: [...s.hostKeyPrompts, prompt] })),
  removeHostKeyPrompt: (routeId) =>
    set((s) => ({ hostKeyPrompts: s.hostKeyPrompts.filter((p) => p.routeId !== routeId) })),
  importPreviews: [],
  pushImportPreviews: (previews) =>
    set((s) => ({
      importPreviews: [...s.importPreviews, ...previews.filter((p) => !s.importPreviews.some((q) => q.path === p.path))],
    })),
  shiftImportPreview: () => set((s) => ({ importPreviews: s.importPreviews.slice(1) })),
}));

// A session that just started becomes its Cluster's tab.
const addDockTab = (s: UIState, clusterId: string, id: string) => ({ dockOrder: [...s.dockOrder, id], dockPicks: { ...s.dockPicks, [clusterId]: id } });

export const sessionEnded = (s: ShellStatus | TerminalStatus) => s.state === State.StateStopped || s.state === State.StateError;
