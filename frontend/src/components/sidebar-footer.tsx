import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { DownloadSimpleIcon, FolderOpenIcon, GearIcon, UploadSimpleIcon } from "@phosphor-icons/react";
import { ConfigService } from "@bindings/internal/bindings";
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
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { AppearanceMenu } from "@/theme";

// Settings is a menu rather than a screen: everything about Kubereach itself rather than a Cluster or a Route.
export function SidebarFooter() {
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const exportConfig = useMutation({ mutationFn: () => ConfigService.Export() });
  const inspect = useMutation({
    mutationFn: () => ConfigService.InspectImport(),
    onSuccess: (p) => setPreview(p),
  });
  const error = exportConfig.error ?? inspect.error;

  return (
    <div className="flex flex-col border-t p-2">
      <DropdownMenu>
        <DropdownMenuTrigger className="flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-sm text-muted-foreground outline-none hover:bg-sidebar-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 aria-expanded:bg-sidebar-accent aria-expanded:text-foreground [&_svg]:size-4">
          <GearIcon />
          Settings
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" side="top" className="w-56">
          <DropdownMenuItem disabled={exportConfig.isPending} onClick={() => exportConfig.mutate()}>
            <UploadSimpleIcon /> Export configuration…
          </DropdownMenuItem>
          <DropdownMenuItem disabled={inspect.isPending} onClick={() => inspect.mutate()}>
            <DownloadSimpleIcon /> Import configuration…
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <AppearanceMenu />
        </DropdownMenuContent>
      </DropdownMenu>
      {error && <p className="px-2 pb-1 text-xs text-destructive">{String(error)}</p>}
      {exportConfig.data && (
        <p className="truncate px-2 pb-1 text-xs text-muted-foreground" title={exportConfig.data}>
          Exported to {exportConfig.data}
        </p>
      )}
      {preview && <ImportDialog preview={preview} onClose={() => setPreview(null)} />}
    </div>
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
        {importConfig.error && <p className="text-xs text-destructive">{String(importConfig.error)}</p>}
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
