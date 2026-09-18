import { create } from "zustand";

interface UIState {
  selectedClusterId: string | null;
  selectCluster: (id: string | null) => void;
}

export const useUIStore = create<UIState>((set) => ({
  selectedClusterId: null,
  selectCluster: (id) => set({ selectedClusterId: id }),
}));
