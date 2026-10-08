import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowCircleUpIcon, ArrowsClockwiseIcon, DownloadSimpleIcon, FolderOpenIcon, GearIcon, InfoIcon, KeyboardIcon, PathIcon, UploadSimpleIcon } from "@phosphor-icons/react";
import { Browser, System } from "@wailsio/runtime";
import { AppService, ClusterService, ConfigService } from "@bindings/internal/bindings";
import type { ImportPreview } from "@bindings/internal/service";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ShortcutsDialog } from "@/components/shortcuts";
import { UpdateDialog, useUpdateStore } from "@/components/update";
import { aboutQuery, errorText } from "@/queries";
import { useUIStore } from "@/store";
import { AppearanceMenu } from "@/theme";
import { keyLabel, useCommand } from "@/lib/commands";

// Settings is a menu rather than a screen: Kubereach itself, and the Routes that are set up once and then used from their Clusters.
export function SidebarFooter() {
  const preview = useUIStore((s) => s.importPreviews[0]);
  const pushImportPreviews = useUIStore((s) => s.pushImportPreviews);
  const shiftImportPreview = useUIStore((s) => s.shiftImportPreview);
  const exportConfig = useMutation({ mutationFn: () => ConfigService.Export() });
  const { data: exportedTo, reset: clearExported } = exportConfig;
  useEffect(() => {
    if (!exportedTo) return;
    const t = setTimeout(clearExported, 3000);
    return () => clearTimeout(t);
  }, [exportedTo, clearExported]);
  const queryClient = useQueryClient();
  // A kubeconfig picked here is imported the way a dropped one is.
  const inspect = useMutation({
    mutationFn: async () => {
      const p = await ConfigService.InspectImport();
      if (p?.kubeconfig) {
        await ClusterService.ImportPaths([p.path]);
        return null;
      }
      return p;
    },
    onSuccess: (p) => p && pushImportPreviews([p]),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["config"] }),
  });
  // Dev builds have no updater, so no update item is shown.
  const { data: info } = useQuery(aboutQuery);
  const release = useUpdateStore((s) => s.release);
  const updatePhase = useUpdateStore((s) => s.phase);
  const updateProgress = useUpdateStore((s) => s.progress);
  const showUpdate = useUpdateStore((s) => s.show);
  const error = exportConfig.error ?? inspect.error;
  const openRoutes = useUIStore((s) => s.openRoutes);
  const [about, setAbout] = useState(false);
  const [shortcuts, setShortcuts] = useState(false);
  useCommand("show-shortcuts", () => setShortcuts(true));
  const [settingsOpen, setSettingsOpen] = useState(false);
  useCommand("open-settings", () => setSettingsOpen(true));

  return (
    <div>
      {error && <p className="px-4 pb-2 text-xs text-destructive">{errorText(error)}</p>}
      {exportConfig.data && (
        <p className="truncate px-4 pb-2 text-xs text-muted-foreground" title={exportConfig.data}>
          Exported to {exportConfig.data}
        </p>
      )}
      {/* As tall as the dock's tab bar, so their top borders meet in one line while the dock is closed. */}
      <div className="box-content flex h-9 items-center border-t px-2">
        <DropdownMenu open={settingsOpen} onOpenChange={setSettingsOpen}>
          <DropdownMenuTrigger title={`Settings (${keyLabel("open-settings")})`} className="flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-sm text-muted-foreground outline-none hover:bg-sidebar-accent hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring aria-expanded:bg-sidebar-accent aria-expanded:text-foreground [&_svg]:size-4">
            <GearIcon />
            Settings
            {info?.updates && release && <span className="ml-auto size-1.5 rounded-full bg-primary" aria-label="Update available" />}
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" side="top" className="w-56">
            <DropdownMenuItem onClick={openRoutes}>
              <PathIcon /> Routes
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled={exportConfig.isPending} onClick={() => exportConfig.mutate()}>
              <UploadSimpleIcon /> Export configuration…
            </DropdownMenuItem>
            <DropdownMenuItem disabled={inspect.isPending} onClick={() => inspect.mutate()}>
              <DownloadSimpleIcon /> Import configuration…
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <AppearanceMenu />
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => setShortcuts(true)}>
              <KeyboardIcon /> Keyboard shortcuts
              <DropdownMenuShortcut>{keyLabel("show-shortcuts")}</DropdownMenuShortcut>
            </DropdownMenuItem>
            {/* Once a release is found the item says where its update is, with the version or progress on the right,
                as Appearance shows its theme. */}
            {info?.updates &&
              (release ? (
                <DropdownMenuItem onClick={showUpdate}>
                  <ArrowCircleUpIcon className="text-primary" />
                  {updatePhase === "ready" ? "Restart to update" : updatePhase === "downloading" ? "Downloading update" : "Update available"}
                  <span className="ml-auto pl-4 text-muted-foreground">
                    {updatePhase === "downloading" && updateProgress?.total
                      ? `${Math.floor((updateProgress.written / updateProgress.total) * 100)}%`
                      : release.version}
                  </span>
                </DropdownMenuItem>
              ) : (
                <DropdownMenuItem onClick={showUpdate}>
                  <ArrowsClockwiseIcon /> Check for updates…
                </DropdownMenuItem>
              ))}
            {/* macOS has About in its application menu (InstallMenu); elsewhere there is no menu bar. */}
            {(System.IsWindows() || System.IsLinux()) && (
              <>
                <DropdownMenuItem onClick={() => setAbout(true)}>
                  <InfoIcon /> About Kubereach
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {preview && <ImportDialog key={preview.path} preview={preview} onClose={shiftImportPreview} />}
      {about && <AboutDialog onClose={() => setAbout(false)} />}
      {shortcuts && <ShortcutsDialog onClose={() => setShortcuts(false)} />}
      {info && <UpdateDialog version={info.version} />}
    </div>
  );
}

// AboutDialog is macOS's native About, drawn here for Windows and Linux.
function AboutDialog({ onClose }: { onClose: () => void }) {
  const { data: info } = useQuery(aboutQuery);
  const ok = useRef<HTMLButtonElement>(null);
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent initialFocus={ok}>
        <DialogHeader>
          <DialogTitle>Kubereach {info?.version}</DialogTitle>
          <DialogDescription>Configuration file:</DialogDescription>
          <p className="break-all font-mono text-xs">{info?.configPath}</p>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={() => AppService.Reveal()}>
            Reveal
          </Button>
          <Button variant="outline" disabled={!info} onClick={() => info && Browser.OpenURL(info.repoURL)}>
            GitHub
          </Button>
          <Button ref={ok} onClick={onClose}>
            OK
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// remap holds the local path per missing one; an empty string skips the entries that reference it.
function ImportDialog({ preview, onClose }: { preview: ImportPreview; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [remap, setRemap] = useState<Record<string, string>>({});
  const [skipped, setSkipped] = useState<Record<string, boolean>>({});
  const missing = preview.missingPaths ?? [];
  const resolved = missing.every((p) => skipped[p] || remap[p]);
  const importConfig = useMutation({
    mutationFn: () =>
      ConfigService.Import(
        preview.path,
        Object.fromEntries(missing.map((p) => [p, skipped[p] ? "" : remap[p]])),
      ),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["config"] });
      onClose();
    },
  });
  const pick = useMutation({
    mutationFn: async (from: string) => [from, await ConfigService.PickPath(`Replace ${from}`)] as const,
    onSuccess: ([from, to]) => to && setRemap((r) => ({ ...r, [from]: to })),
  });
  const summary = [
    `${preview.routes?.length ?? 0} routes`,
    `${preview.clusters?.length ?? 0} clusters`,
    `${preview.forwards?.length ?? 0} saved forwards`,
    preview.duplicates > 0 && `${preview.duplicates} already present`,
  ]
    .filter(Boolean)
    .join(", ");

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Import configuration</DialogTitle>
          <DialogDescription>{summary}.</DialogDescription>
        </DialogHeader>
        {missing.length > 0 && (
          <div className="flex flex-col gap-3">
            <p className="text-sm text-muted-foreground">
              These files do not exist on this machine. Pick a local replacement or skip what references them.
            </p>
            {missing.map((from) => (
              <div key={from} className="flex flex-col gap-1">
                <Label className="truncate font-mono text-xs" title={from}>
                  {from}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    value={remap[from] ?? ""}
                    placeholder="Local path"
                    disabled={skipped[from]}
                    onChange={(e) => setRemap((r) => ({ ...r, [from]: e.target.value }))}
                  />
                  <Button variant="outline" size="icon-sm" title="Browse" disabled={skipped[from]} onClick={() => pick.mutate(from)}>
                    <FolderOpenIcon />
                  </Button>
                  <Label className="gap-1.5 text-xs">
                    <Checkbox checked={!!skipped[from]} onCheckedChange={(v) => setSkipped((s) => ({ ...s, [from]: !!v }))} />
                    Skip
                  </Label>
                </div>
              </div>
            ))}
          </div>
        )}
        {importConfig.error && <p className="text-xs text-destructive">{errorText(importConfig.error)}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={!resolved || importConfig.isPending} onClick={() => importConfig.mutate()}>
            Import
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
