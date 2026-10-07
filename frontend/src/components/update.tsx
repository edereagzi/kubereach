import { useRef, type ReactNode } from "react";
import { create } from "zustand";
import { Browser } from "@wailsio/runtime";
import { CircleNotchIcon } from "@phosphor-icons/react";
import { AppService, type Update } from "@bindings/internal/bindings";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { errorText } from "@/queries";

// Checking, downloading and installing are each the user's step: a release found in the background only puts a
// badge on Settings, and the dialog downloads it on "Download update" and installs it on "Restart to update".
// A skipped release gets no badge; a check the user asks for still offers it.
const skippedKey = "skippedUpdate";

type Phase = "checking" | "latest" | "available" | "downloading" | "ready" | "check-failed" | "download-failed";

interface UpdateState {
  // The newest release found, by the background check or the dialog's; the badge shows while it is set.
  release: Update | null;
  phase: Phase | null;
  open: boolean;
  progress: { written: number; total: number } | null;
  error: string | null;
  // A release announced in the background while the user is not already past it.
  found: (u: Update) => void;
  setProgress: (written: number, total: number) => void;
  // Opens the dialog on the step the update is at, or checks when nothing was found yet.
  show: () => void;
  check: () => Promise<void>;
  skip: () => void;
  download: () => Promise<void>;
  restart: () => Promise<void>;
  close: () => void;
}

export const useUpdateStore = create<UpdateState>((set, get) => ({
  release: null,
  phase: null,
  open: false,
  progress: null,
  error: null,
  found: (u) => {
    const { phase } = get();
    if (u.version === localStorage.getItem(skippedKey)) return;
    if (phase !== "downloading" && phase !== "ready") set({ release: u, phase: "available" });
  },
  setProgress: (written, total) => set({ progress: { written, total } }),
  show: () => {
    const { release, phase } = get();
    if (release && (phase === "available" || phase === "downloading" || phase === "ready" || phase === "download-failed")) {
      set({ open: true });
    } else {
      void get().check();
    }
  },
  check: async () => {
    set({ open: true, phase: "checking", error: null });
    try {
      const release = await AppService.CheckForUpdates();
      set(release ? { release, phase: "available" } : { release: null, phase: "latest" });
    } catch (err) {
      set({ phase: "check-failed", error: errorText(err) });
    }
  },
  skip: () => {
    const { release } = get();
    if (release) localStorage.setItem(skippedKey, release.version);
    set({ release: null, phase: null, open: false });
  },
  download: async () => {
    set({ phase: "downloading", progress: null, error: null });
    try {
      await AppService.DownloadUpdate();
      set({ phase: "ready" });
    } catch (err) {
      set({ phase: "download-failed", error: errorText(err) });
    }
  },
  restart: async () => {
    try {
      await AppService.RestartToUpdate();
    } catch (err) {
      set({ phase: "download-failed", error: errorText(err) });
    }
  },
  close: () => {
    const { phase } = get();
    // A result nobody acted on is not kept; a release found stays for the badge.
    set(phase === "latest" || phase === "check-failed" || phase === "checking" ? { open: false, phase: null } : { open: false });
  },
}));

const megabytes = (bytes: number) => `${(bytes / 1024 / 1024).toFixed(1)} MB`;

export function UpdateDialog({ version }: { version: string }) {
  const { release, phase, open, progress, error, check, skip, download, restart, close } = useUpdateStore();
  const primary = useRef<HTMLButtonElement>(null);
  if (!open || !phase) return null;

  let title: string;
  let description: ReactNode;
  let actions: ReactNode;
  const later = (
    <Button variant="outline" onClick={close}>
      Later
    </Button>
  );
  switch (phase) {
    case "checking":
      title = "Checking for updates…";
      description = (
        <span className="flex items-center gap-2">
          <CircleNotchIcon className="size-4 animate-spin" /> Asking GitHub for the latest release.
        </span>
      );
      actions = (
        <Button variant="outline" onClick={close}>
          Close
        </Button>
      );
      break;
    case "latest":
      title = "Kubereach is up to date";
      description = `${version} is the latest release.`;
      actions = (
        <Button ref={primary} onClick={close}>
          OK
        </Button>
      );
      break;
    case "check-failed":
      title = "Could not check for updates";
      description = error;
      actions = (
        <>
          <Button variant="outline" onClick={close}>
            Close
          </Button>
          <Button ref={primary} onClick={check}>
            Try again
          </Button>
        </>
      );
      break;
    case "available":
    case "download-failed":
      title = `Kubereach ${release?.version} is available`;
      description = phase === "download-failed" ? error : `You have ${version}. The download is ${megabytes(release?.size ?? 0)}.`;
      actions = (
        <>
          {phase === "available" && (
            <Button variant="ghost" className="sm:mr-auto" onClick={skip}>
              Skip this version
            </Button>
          )}
          {later}
          <Button ref={primary} onClick={download}>
            {phase === "download-failed" ? "Try again" : "Download update"}
          </Button>
        </>
      );
      break;
    case "downloading": {
      const total = progress?.total || release?.size || 0;
      const written = progress?.written ?? 0;
      title = `Downloading Kubereach ${release?.version}…`;
      description =
        total > 0 && written >= total ? "Checking the download's signature…" : `${megabytes(written)} of ${megabytes(total)}`;
      actions = (
        <>
          <div className="mr-auto h-1.5 w-40 self-center overflow-hidden rounded-full bg-muted" role="progressbar" aria-valuemax={total} aria-valuenow={written}>
            <div className="h-full bg-primary transition-[width]" style={{ width: total > 0 ? `${Math.min(100, (written / total) * 100)}%` : "0%" }} />
          </div>
          <Button variant="outline" onClick={close}>
            Hide
          </Button>
        </>
      );
      break;
    }
    case "ready":
      title = `Kubereach ${release?.version} is ready to install`;
      description = "Kubereach restarts to finish. Open Shells and Terminals close; Saved Forwards come back on their own.";
      actions = (
        <>
          {later}
          <Button ref={primary} onClick={restart}>
            Restart to update
          </Button>
        </>
      );
      break;
  }

  const notes = release && (phase === "available" || phase === "downloading" || phase === "ready" || phase === "download-failed");
  return (
    <Dialog open onOpenChange={(o) => !o && close()}>
      <DialogContent initialFocus={primary} className={notes ? "sm:max-w-lg" : undefined}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {notes && release.notes && <ReleaseNotes markdown={release.notes} />}
        <DialogFooter>{actions}</DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ReleaseNotes draws what CHANGELOG.md sections hold: ### headings, "- " items and [text](url) links.
function ReleaseNotes({ markdown }: { markdown: string }) {
  const blocks: ReactNode[] = [];
  let items: string[] = [];
  const flush = () => {
    if (items.length) {
      blocks.push(
        <ul key={blocks.length} className="mb-3 list-disc space-y-1 pl-5 marker:text-muted-foreground">
          {items.map((item, i) => (
            <li key={i}>{inline(item)}</li>
          ))}
        </ul>,
      );
    }
    items = [];
  };
  for (const line of markdown.split("\n")) {
    const heading = /^#{1,6}\s+(.*)/.exec(line);
    const item = /^[-*]\s+(.*)/.exec(line);
    if (item) {
      items.push(item[1]);
      continue;
    }
    flush();
    if (heading) {
      blocks.push(
        <h3 key={blocks.length} className="mb-1 font-medium">
          {heading[1]}
        </h3>,
      );
    } else if (line.trim()) {
      blocks.push(
        <p key={blocks.length} className="mb-3">
          {inline(line)}
        </p>,
      );
    }
  }
  flush();
  return <div className="-mx-4 max-h-72 overflow-y-auto border-t px-4 pt-3 text-sm [&>:last-child]:mb-0">{blocks}</div>;
}

// inline turns [text](https://…) into links that open in the browser; the rest stays text.
function inline(text: string): ReactNode[] {
  return text.split(/(\[[^\]]+\]\(https?:[^)]+\))/).map((part, i) => {
    const link = /^\[([^\]]+)\]\((https?:[^)]+)\)$/.exec(part);
    return link ? (
      <button key={i} type="button" className="text-primary underline-offset-4 hover:underline" onClick={() => Browser.OpenURL(link[2])}>
        {link[1]}
      </button>
    ) : (
      part
    );
  });
}
