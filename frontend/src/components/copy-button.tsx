import { useEffect, useState } from "react";
import { CheckIcon, CopyIcon } from "@phosphor-icons/react";
import { Clipboard } from "@wailsio/runtime";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// CopyButton copies text to the clipboard and shows a check for a moment; it stays visible while it does.
export function CopyButton({ text, title, className }: { text: string; title: string; className?: string }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);
  return (
    <Button
      variant="ghost"
      size="icon-xs"
      title={copied ? "Copied" : title}
      className={cn("focus-visible:opacity-100 group-hover:opacity-100", copied ? "text-primary" : "opacity-0", className)}
      onClick={() => Clipboard.SetText(text).then(() => setCopied(true))}
    >
      {copied ? <CheckIcon /> : <CopyIcon />}
    </Button>
  );
}
