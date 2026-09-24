import { useEffect, useState } from "react";
import { ArrowsClockwiseIcon } from "@phosphor-icons/react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// A fetch that answers in milliseconds would otherwise show no sign it ran, so the icon always makes one full turn.
export function RefreshButton({ title = "Refresh", fetching, onRefresh }: { title?: string; fetching: boolean; onRefresh: () => void }) {
  const [turning, setTurning] = useState(false);
  useEffect(() => {
    if (!turning) return;
    const t = setTimeout(() => setTurning(false), 1000);
    return () => clearTimeout(t);
  }, [turning]);
  const busy = fetching || turning;
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      title={title}
      disabled={busy}
      onClick={() => {
        setTurning(true);
        onRefresh();
      }}
    >
      <ArrowsClockwiseIcon className={cn(busy && "animate-spin")} />
    </Button>
  );
}
