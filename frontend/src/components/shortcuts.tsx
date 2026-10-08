import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { commands, formatKey, groups, keysOf, useAvailable, type CommandId } from "@/lib/commands";
import { cn } from "@/lib/utils";

const ids = Object.keys(commands) as CommandId[];

// A row of keys, such as ⌘1 to ⌘9, shows its first and last key.
const shownKeys = (id: CommandId) => {
  const keys = keysOf(id);
  return keys.length > 2 ? [keys[0], "…" as const, keys.at(-1)!] : keys;
};

// Made from the catalog, so every key that works is listed. A key that is not available here and now is dimmed.
export function ShortcutsDialog({ onClose }: { onClose: () => void }) {
  const available = useAvailable();
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
        </DialogHeader>
        {Object.entries(groups).map(([group, where]) => {
          const rows = ids.filter((id) => commands[id].group === group && keysOf(id).length);
          return (
            rows.length > 0 && (
              <section key={group} className="flex flex-col gap-1.5">
                <h3 className="text-xs text-muted-foreground">{where}</h3>
                <dl className="flex flex-col gap-1">
                  {rows.map((id) => (
                    <div key={id} className={cn("flex items-center justify-between gap-4", !available(id) && "opacity-50")}>
                      <dt>{commands[id].name}</dt>
                      <dd className="flex gap-1">
                        {shownKeys(id).map((k) =>
                          k === "…" ? (
                            <span key={k} className="text-muted-foreground">…</span>
                          ) : (
                            <kbd key={k} className="min-w-5 rounded border border-b-2 px-1 text-center font-sans text-[11px] leading-4 text-muted-foreground">
                              {formatKey(k)}
                            </kbd>
                          ),
                        )}
                      </dd>
                    </div>
                  ))}
                </dl>
              </section>
            )
          );
        })}
      </DialogContent>
    </Dialog>
  );
}
