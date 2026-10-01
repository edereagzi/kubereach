import { createContext, useContext, useEffect, type ComponentProps, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { XIcon } from "@phosphor-icons/react";
import { CopyButton } from "@/components/copy-button";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// The element beside the view that an Inspector opens into.
export const InspectorSlot = createContext<HTMLElement | null>(null);

// An object's detail opens beside the list rather than over it, so the list stays usable while the detail is read.
// It is a plain panel, not a dialog, so a confirmation opened from it is a dialog of its own with its own backdrop.
export function Inspector({ onClose, children }: { onClose: () => void; children: ReactNode }) {
  const slot = useContext(InspectorSlot);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Escape belongs to an open menu, list or dialog before it reaches the panel.
      if (e.key === "Escape" && !e.defaultPrevented && !document.querySelector("[role=dialog], [role=alertdialog], [role=menu], [role=listbox]")) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  if (!slot) return null;
  return createPortal(
    <section data-slot="inspector" className="relative flex h-full flex-col gap-4 overflow-hidden p-4 text-sm">
      {children}
      <Button variant="ghost" size="icon-sm" className="absolute top-2 right-2" title="Close" onClick={onClose}>
        <XIcon />
      </Button>
    </section>,
    slot,
  );
}

export function InspectorHeader({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("flex flex-col gap-2", className)} {...props} />;
}

export function InspectorTitle({ className, ...props }: ComponentProps<"h2">) {
  return <h2 className={cn("font-heading text-base leading-none font-medium", className)} {...props} />;
}

// InspectorName is an object's name in its detail title. The namespace steps back, so the name reads as what the button
// beside it copies: the part kubectl takes.
export function InspectorName({ namespace, name }: { namespace?: string; name: string }) {
  return (
    <>
      <span className="truncate">
        {namespace && <span className="text-muted-foreground">{namespace}/</span>}
        {name}
      </span>
      <CopyButton text={name} title="Copy name" size="icon-sm" className="opacity-100" />
    </>
  );
}

export function InspectorDescription({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("text-sm text-muted-foreground", className)} {...props} />;
}

// While a detail is open, ↑ and ↓ walk the rows as they are shown; each row carries its key in data-row to be scrolled to.
export function useInspectorWalk<T>(rows: RefObject<T[]>, current: T | null, key: (row: T) => string, select: (row: T) => void) {
  useEffect(() => {
    if (!current) return;
    const onKey = (e: KeyboardEvent) => {
      if ((e.key !== "ArrowDown" && e.key !== "ArrowUp") || (e.target instanceof HTMLElement && e.target.closest("input, textarea, [contenteditable], [role=tablist], [role=menu], [role=listbox]"))) return;
      const next = rows.current[rows.current.findIndex((r) => key(r) === key(current)) + (e.key === "ArrowDown" ? 1 : -1)];
      if (!next) return;
      e.preventDefault();
      select(next);
      document.querySelector(`[data-row="${CSS.escape(key(next))}"]`)?.scrollIntoView({ block: "nearest" });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [current]);
}
