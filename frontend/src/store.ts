import { create } from "zustand";
import type { HostKeyPrompt, RouteStatus } from "@bindings/internal/service";

interface UIState {
  selectedClusterId: string | null;
  selectCluster: (id: string | null) => void;
  routeStatuses: Record<string, RouteStatus>;
  setRouteStatus: (status: RouteStatus) => void;
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
  hostKeyPrompts: [],
  addHostKeyPrompt: (prompt) => set((s) => ({ hostKeyPrompts: [...s.hostKeyPrompts, prompt] })),
  removeHostKeyPrompt: (routeId) =>
    set((s) => ({ hostKeyPrompts: s.hostKeyPrompts.filter((p) => p.routeId !== routeId) })),
}));
