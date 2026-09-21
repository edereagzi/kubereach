import { create } from "zustand";
import {
  State,
  type ForwardStatus,
  type HostKeyPrompt,
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

type ClusterTab = "overview" | "forwards" | "logs" | "shell";

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
  shellSessions: Record<string, ShellStatus>;
  activeShellId: string | null;
  setShellStatus: (status: ShellStatus) => void;
  selectShell: (id: string) => void;
  // An ended session stays on screen until closed, so its last output can be read.
  closeShell: (id: string) => void;
  hostKeyPrompts: HostKeyPrompt[];
  addHostKeyPrompt: (prompt: HostKeyPrompt) => void;
  removeHostKeyPrompt: (routeId: string) => void;
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
}));
