import { create } from "zustand";
import { State, type ForwardStatus, type HostKeyPrompt, type RouteStatus } from "@bindings/internal/service";

interface UIState {
  selectedClusterId: string | null;
  selectCluster: (id: string | null) => void;
  routeStatuses: Record<string, RouteStatus>;
  setRouteStatus: (status: RouteStatus) => void;
  forwardStatuses: Record<string, ForwardStatus>;
  setForwardStatus: (status: ForwardStatus) => void;
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
  hostKeyPrompts: [],
  addHostKeyPrompt: (prompt) => set((s) => ({ hostKeyPrompts: [...s.hostKeyPrompts, prompt] })),
  removeHostKeyPrompt: (routeId) =>
    set((s) => ({ hostKeyPrompts: s.hostKeyPrompts.filter((p) => p.routeId !== routeId) })),
}));
