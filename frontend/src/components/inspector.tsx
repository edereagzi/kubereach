import { createContext, Fragment, useContext, type ComponentProps, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { CaretRightIcon } from "@phosphor-icons/react";
import { CopyButton } from "@/components/copy-button";
import { KindBadge, kindName, type Kind } from "@/components/targets";
import { Button } from "@/components/ui/button";
import { keyLabel, useCommand } from "@/lib/commands";
import { cn } from "@/lib/utils";

// The element beside the view that an Inspector opens into.
export const InspectorSlot = createContext<HTMLElement | null>(null);

// An object's detail opens beside the list rather than over it, so the list stays usable while the detail is read.
// It is a plain panel, not a dialog, so a confirmation opened from it is a dialog of its own with its own backdrop.
export function Inspector({ onClose, children }: { onClose: () => void; children: ReactNode }) {
  const slot = useContext(InspectorSlot);
  useCommand("close-detail", onClose);
  if (!slot) return null;
  // Close belongs to the panel, not to what it shows, so it sits on the panel's edge, across the rule between list and
  // detail, level with the title: in the title's row of icons it read as one more thing done to the object.
  return createPortal(
    <>
      <Button
        variant="outline"
        size="icon-xs"
        className="absolute top-[18px] -left-3 z-20 rounded-full bg-background text-muted-foreground dark:bg-background"
        title={`Close (${keyLabel("close-detail")})`}
        onClick={onClose}
      >
        <CaretRightIcon />
      </Button>
      <section data-slot="inspector" className="relative flex h-full flex-col gap-4 overflow-hidden py-4 pr-4 pl-6 text-sm">
        {children}
      </section>
    </>,
    slot,
  );
}

export function InspectorHeader({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("flex flex-col gap-2", className)} {...props} />;
}

export function InspectorTitle({ className, ...props }: ComponentProps<"h2">) {
  // Top-aligned rather than centred: a long name wraps, and its icons and badges stay level with its first line,
  // each nudged down to sit in that line's 28px, the height of the icon buttons.
  return <h2 className={cn("flex items-start gap-2 font-heading text-base leading-none font-medium [&>[data-slot=badge]]:mt-[5px]", className)} {...props} />;
}

// InspectorName is an object's name in its detail title. The namespace steps back, so the name reads as what the button
// beside it copies: the part kubectl takes. It wraps rather than truncates: a detail is where the whole name is read,
// and a pod's is long, its distinguishing suffix last. The kind leads it as the same badge its row in the list wears, so the detail
// says what it shows without a line of its own; its tooltip spells the kind out.
export function InspectorName({ kind, namespace, name }: { kind: Kind; namespace?: string; name: string }) {
  return (
    <>
      <KindBadge kind={kind} title={kindName[kind]} className="mt-[5px] w-auto" />
      <span className="min-w-0 flex-1 py-[3px] leading-snug [overflow-wrap:anywhere]">
        {namespace && <span className="text-muted-foreground">{namespace}/</span>}
        {name}
      </span>
      <CopyButton text={name} title="Copy name" size="icon-sm" className="opacity-100" />
    </>
  );
}

// InspectorActions is a detail's buttons in two groups: what opens something here (Forward, Logs, Shell) on the left, and
// what changes the Cluster (Restart, Scale, Cordon…) pushed to the right; a rule between them read as a stray "|". A group with nothing in it takes no room, and neither does the row;
// Delete is not here but in the title, beside Copy and Refresh, so a detail with nothing else to do has no row for it alone.
// It sits right under the kind, above the facts, so it is at the same height in every detail however many facts follow.
export function InspectorActions({ open, change }: { open?: ReactNode; change?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-1.5 [&:not(:has(>:not(:empty)))]:hidden">
      <div className="flex flex-wrap items-center gap-1.5 empty:hidden">{open}</div>
      <div className="flex flex-wrap items-center gap-1.5 empty:hidden [:not(:empty)+&]:ml-auto">{change}</div>
    </div>
  );
}

// InspectorFacts is what a detail says at a glance, one labelled row each, laid like a container's State and Resources:
// a label says what a bare value is, and a row never breaks mid-phrase the way a run-on line did. A fact with no value is left out.
export function InspectorFacts({ facts }: { facts: [string, ReactNode][] }) {
  return (
    <dl className="mt-2 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-0.5 text-xs">
      {facts
        .filter(([, value]) => value !== undefined && value !== null && value !== false && value !== "")
        .map(([label, value]) => (
          <Fragment key={label}>
            <dt className="text-muted-foreground">{label}</dt>
            <dd className="min-w-0 break-words">{value}</dd>
          </Fragment>
        ))}
    </dl>
  );
}

// ↑/↓ and K/J walk the rows as they are shown. With no detail open, the next-row key opens the first row.
// Each row carries its key in data-row to be scrolled to.
export function useInspectorWalk<T>(rows: RefObject<T[]>, current: T | null, key: (row: T) => string, select: (row: T) => void) {
  const walk = (step: number) => () => {
    const next = current ? rows.current[rows.current.findIndex((r) => key(r) === key(current)) + step] : step > 0 && rows.current[0];
    if (!next) return;
    select(next);
    document.querySelector(`[data-row="${CSS.escape(key(next))}"]`)?.scrollIntoView({ block: "nearest" });
  };
  useCommand("next-row", walk(1));
  useCommand("previous-row", walk(-1), { enabled: !!current });
}
