import { create } from "zustand";
import { MonitorIcon, MoonIcon, SunIcon } from "@phosphor-icons/react";
import {
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from "@/components/ui/dropdown-menu";

export type Theme = "system" | "light" | "dark";

const storageKey = "theme";
const osDark = window.matchMedia("(prefers-color-scheme: dark)");
const isDark = (theme: Theme) => theme === "dark" || (theme === "system" && osDark.matches);

interface ThemeState {
  theme: Theme;
  dark: boolean;
  setTheme: (theme: Theme) => void;
}

export const useTheme = create<ThemeState>((set, get) => {
  const stored = localStorage.getItem(storageKey);
  const apply = (theme: Theme) => {
    const dark = isDark(theme);
    document.documentElement.classList.toggle("dark", dark);
    return { theme, dark };
  };
  osDark.addEventListener("change", () => set(apply(get().theme)));
  return {
    ...apply(stored === "light" || stored === "dark" ? stored : "system"),
    setTheme: (theme) => {
      if (theme === "system") localStorage.removeItem(storageKey);
      else localStorage.setItem(storageKey, theme);
      set(apply(theme));
    },
  };
});

const options: { value: Theme; label: string; icon: typeof SunIcon }[] = [
  { value: "system", label: "System", icon: MonitorIcon },
  { value: "light", label: "Light", icon: SunIcon },
  { value: "dark", label: "Dark", icon: MoonIcon },
];

// Appearance lives inside the app menu; the current choice shows on the submenu trigger.
export function AppearanceMenu() {
  const theme = useTheme((s) => s.theme);
  const setTheme = useTheme((s) => s.setTheme);
  const current = options.find((o) => o.value === theme)!;
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>
        <current.icon />
        Appearance
        <span className="ml-auto pl-4 text-muted-foreground">{current.label}</span>
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent>
        <DropdownMenuRadioGroup value={theme} onValueChange={(v) => setTheme(v as Theme)}>
          {options.map((o) => (
            <DropdownMenuRadioItem key={o.value} value={o.value}>
              <o.icon />
              {o.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  );
}
