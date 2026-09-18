import { create } from "zustand";
import {
  State,
  type ForwardStatus,
  type HostKeyPrompt,
  type LogBatch,
  type LogLine,
  type LogStatus,
  type RouteStatus,
} from "@bindings/internal/service";

// ponytail: one flat buffer per stream, trimmed from the front; a ring buffer if the splice ever shows up in profiles.
const maxLogLines = 50_000;

// Lines are appended in place and version bumps notify subscribers, so a batch never copies the buffer.
type LogBuffer = { lines: LogLine[]; version: number };

interface UIState {
  selectedClusterId: string | null;
  selectCluster: (id: string | null) => void;
  routeStatuses: Record<string, RouteStatus>;
  setRouteStatus: (status: RouteStatus) => void;
  forwardStatuses: Record<string, ForwardStatus>;
  setForwardStatus: (status: ForwardStatus) => void;
  logStreams: Record<string, LogStatus>;
  setLogStatus: (status: LogStatus) => void;
  logBuffers: Record<string, LogBuffer>;
  appendLogs: (batch: LogBatch) => void;
  clearLogs: (streamId: string) => void;
  hostKeyPrompts: HostKeyPrompt[];
  addHostKeyPrompt: (prompt: HostKeyPrompt) => void;
  removeHostKeyPrompt: (routeId: string) => void;
}

export const useUIStore = create<UIState>((set) => ({
  selectedClusterId: null,
  selectCluster: (id) => set({ selectedClusterId: id }),
  routeStatuses: {},
  setRouteStatus: (status) =>
    set((s) => ({ routeStatuses: { ...s.routeStatuses, [status.routeId]: status } })),
  forwardStatuses: {},
  // A stopped forward is forgotten by the backend, so it leaves the list.
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
  hostKeyPrompts: [],
  addHostKeyPrompt: (prompt) => set((s) => ({ hostKeyPrompts: [...s.hostKeyPrompts, prompt] })),
  removeHostKeyPrompt: (routeId) =>
    set((s) => ({ hostKeyPrompts: s.hostKeyPrompts.filter((p) => p.routeId !== routeId) })),
}));
