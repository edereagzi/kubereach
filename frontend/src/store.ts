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
} from "@bindings/internal/service";

// ponytail: one flat buffer per stream, trimmed from the front; a ring buffer if the splice ever shows up in profiles.
const maxLogLines = 50_000;

// Lines are appended in place and version bumps notify subscribers, so a batch never copies the buffer.
type LogBuffer = { lines: LogLine[]; version: number };

// ponytail: the newest events of one stream, trimmed from the end; enough for an incident's hour.
const maxEvents = 5_000;

type EventBuffer = { events: KubeEvent[]; version: number };

export type ClusterTab = "overview" | "nodes" | "events" | "forwards" | "logs" | "shell";

// An object another tab asks the Overview to open the detail of.
export type InspectRequest = { clusterId: string; kind: Kind; namespace: string; name: string };

interface UIState {
  selectedClusterId: string | null;
  selectCluster: (id: string | null) => void;
  activeTab: ClusterTab;
  selectTab: (tab: ClusterTab) => void;
  routeStatuses: Record<string, RouteStatus>;
  setRouteStatus: (status: RouteStatus) => void;
  forwardStatuses: Record<string, ForwardStatus>;
  setForwardStatus: (status: ForwardStatus) => void;
  logStreams: Record<string, LogStatus>;
  setLogStatus: (status: LogStatus) => void;
  logBuffers: Record<string, LogBuffer>;
  appendLogs: (batch: LogBatch) => void;
  clearLogs: (streamId: string) => void;
  eventStreams: Record<string, EventStatus>;
  setEventStatus: (status: EventStatus) => void;
  eventBuffers: Record<string, EventBuffer>;
  appendEvents: (batch: EventBatch) => void;
  inspectRequest: InspectRequest | null;
  requestInspect: (request: InspectRequest | null) => void;
  shellSessions: Record<string, ShellStatus>;
  activeShellId: string | null;
  setShellStatus: (status: ShellStatus) => void;
  selectShell: (id: string) => void;
  // An ended session stays on screen until closed, so its last output can be read.
  closeShell: (id: string) => void;
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
  selectCluster: (id) => set({ selectedClusterId: id }),
  activeTab: "overview",
  selectTab: (tab) => set({ activeTab: tab }),
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
        delete logStreams[status.id];
        delete logBuffers[status.id];
      } else logStreams[status.id] = status;
      return { logStreams, logBuffers };
    }),
  logBuffers: {},
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
  activeShellId: null,
  setShellStatus: (status) =>
    set((s) => ({
      shellSessions: { ...s.shellSessions, [status.id]: status },
      activeShellId: s.shellSessions[status.id] ? s.activeShellId : status.id,
    })),
  selectShell: (id) => set({ activeShellId: id }),
  closeShell: (id) =>
    set((s) => {
      const shellSessions = { ...s.shellSessions };
      delete shellSessions[id];
      const activeShellId = s.activeShellId === id ? (Object.keys(shellSessions).at(-1) ?? null) : s.activeShellId;
      return { shellSessions, activeShellId };
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
