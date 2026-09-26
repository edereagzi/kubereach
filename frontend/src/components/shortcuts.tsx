import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { modKey } from "@/lib/utils";

// Written by hand: a handler added elsewhere is listed here too.
const groups: [string, [string, string[]][]][] = [
  [
    "Anywhere",
    [
      ["Filter clusters", [`${modKey}K`]],
      ["Show shortcuts", ["?"]],
      ["Close a dialog or menu", ["Esc"]],
      ["Quit Kubereach", [`${modKey}Q`]],
    ],
  ],
  [
    "In a cluster",
    [
      ["Filter the overview", ["/"]],
      ["Show or hide the panel", [`${modKey}J`]],
    ],
  ],
  [
    "With a detail open",
    [
      ["Next or previous row", ["↓", "↑"]],
      ["Close the detail", ["Esc"]],
    ],
  ],
  ["In logs", [["Select every line", [`${modKey}A`]]]],
  [
    "While typing",
    [
      ["Clear the cluster filter", ["Esc"]],
      ["Cancel a local port change", ["Esc"]],
    ],
  ],
];

export function ShortcutsDialog({ onClose }: { onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
        </DialogHeader>
        {groups.map(([where, rows]) => (
          <section key={where} className="flex flex-col gap-1.5">
            <h3 className="text-xs text-muted-foreground">{where}</h3>
            <dl className="flex flex-col gap-1">
              {rows.map(([what, keys]) => (
                <div key={what} className="flex items-center justify-between gap-4">
                  <dt>{what}</dt>
                  <dd className="flex gap-1">
                    {keys.map((k) => (
                      <kbd key={k} className="min-w-5 rounded border border-b-2 px-1 text-center font-sans text-[11px] leading-4 text-muted-foreground">
                        {k}
                      </kbd>
                    ))}
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        ))}
      </DialogContent>
    </Dialog>
  );
}
