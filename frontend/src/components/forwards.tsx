import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRightIcon, ArrowSquareOutIcon, DotsThreeIcon, PlusIcon } from "@phosphor-icons/react";
import { Browser } from "@wailsio/runtime";
import { ForwardService } from "@bindings/internal/bindings";
import { State, TargetKind, type Cluster, type ForwardStatus, type PortForward } from "@bindings/internal/service";
import { CopyButton } from "@/components/copy-button";
import { StateDot } from "@/components/routes";
import { forwardKind, KindBadge, portsLabel, TargetPicker, targetValue, useTargets, type Target } from "@/components/targets";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { ComboboxInput } from "@/components/ui/combobox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { configQuery, portInUse } from "@/queries";
import { useUIStore } from "@/store";
import { cn } from "@/lib/utils";

const forwardKey = (f: PortForward) =>
  targetValue(f.target.kind === TargetKind.TargetService ? "svc" : "pod", f.target.namespace, f.target.name);

export const forwardsFor = (forwards: PortForward[] | null | undefined, cluster: Cluster) =>
  (forwards ?? []).filter((f) => f.clusterId === cluster.id);

export function PortForwards({ cluster }: { cluster: Cluster }) {
  const { data } = useQuery(configQuery);
  const statuses = useUIStore((s) => s.forwardStatuses);
  const [adding, setAdding] = useState(false);
  const saved = forwardsFor(data?.forwards, cluster);
  // An idle forward is healthy: its port is bound and it connects on first use, so only "on" and "failing" are counted.
  const on = saved.filter((f) => f.enabled).length;
  const failing = saved.filter((f) => f.enabled && statuses[f.id]?.state === State.StateError).length;
  const groups = new Map<string, PortForward[]>();
  for (const f of saved) groups.set(forwardKey(f), [...(groups.get(forwardKey(f)) ?? []), f]);

  if (saved.length === 0) {
    return (
      <>
        {adding && <AddForward cluster={cluster} saved={saved} onClose={() => setAdding(false)} />}
        <Empty className="justify-start border-0 pt-12">
          <EmptyHeader>
            <EmptyTitle>No port forwards</EmptyTitle>
            <EmptyDescription>Add a service or pod to get a stable local port for each of its ports.</EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button size="sm" onClick={() => setAdding(true)}>
              <PlusIcon /> Add forward
            </Button>
          </EmptyContent>
        </Empty>
      </>
    );
  }

  return (
    <div className="flex flex-col pb-4">
      {adding && <AddForward cluster={cluster} saved={saved} onClose={() => setAdding(false)} />}
      <div className="flex items-center gap-2 px-4 py-2.5">
        <Button size="sm" onClick={() => setAdding(true)}>
          <PlusIcon /> Add forward
        </Button>
        <span className="ml-auto text-xs text-muted-foreground">
          {on} of {saved.length} on
          {failing > 0 && <span className="text-destructive"> · {failing} failing</span>}
        </span>
      </div>
      {[...groups].map(([key, forwards]) => (
        <section key={key}>
          <h3 className="flex items-center gap-2 px-4 pt-3 pb-1 text-sm font-medium">
            <KindBadge kind={forwards[0]!.target.kind === TargetKind.TargetService ? "svc" : "pod"} />
            {forwards[0]!.target.namespace}/{forwards[0]!.target.name}
          </h3>
          {forwards.map((f) => (
            <ForwardRow key={f.id} forward={f} status={statuses[f.id]} />
          ))}
        </section>
      ))}
    </div>
  );
}

function stateSentence(forward: PortForward, status?: ForwardStatus) {
  if (!forward.enabled) return "Off, port stays reserved";
  switch (status?.state) {
    case State.StateConnecting:
      return "Connecting…";
    case State.StateConnected:
      return `Connected through ${status.pod}`;
    case State.StateReconnecting:
      return "Reconnecting…";
    case State.StateError:
      return status.error ?? "Failed";
    default:
      return "Port is bound, connects on first use";
  }
}

function ForwardRow({ forward, status }: { forward: PortForward; status?: ForwardStatus }) {
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [localPort, setLocalPort] = useState("");
  const invalidateConfig = () => queryClient.invalidateQueries({ queryKey: configQuery.queryKey });
  const setEnabled = useMutation({
    mutationFn: (enabled: boolean) => ForwardService.SetEnabled(forward.id, enabled),
    onSuccess: invalidateConfig,
  });
  const save = useMutation({
    mutationFn: (patch: Partial<PortForward>) => ForwardService.Save({ ...forward, ...patch }),
    onSuccess: () => {
      setEditing(false);
      setEnabled.reset();
      invalidateConfig();
    },
  });
  const remove = useMutation({ mutationFn: () => ForwardService.Delete(forward.id), onSuccess: invalidateConfig });
  const address = `localhost:${forward.localPort}`;
  const url = `${forward.remotePort === 443 || forward.remotePort === 8443 ? "https" : "http"}://${address}`;
  const error = setEnabled.error ?? remove.error ?? save.error;
  const busy = portInUse(error);
  const failed = !!error || status?.state === State.StateError;

  return (
    <div className="group flex h-9 items-center gap-3 px-4 hover:bg-accent">
      <StateDot status={forward.enabled ? status : undefined} hollow={!forward.enabled} />
      <span className={cn("flex w-64 shrink-0 items-center gap-1.5 font-mono text-xs", !forward.enabled && "text-muted-foreground")}>
        {editing ? (
          <form
            className="flex items-center gap-1"
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate({ localPort: Number(localPort) });
            }}
          >
            <span>localhost:</span>
            <Input
              type="number"
              min={1}
              max={65535}
              autoFocus
              className="h-6 w-20 px-1.5 font-mono text-xs"
              value={localPort}
              onChange={(e) => setLocalPort(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && setEditing(false)}
            />
            <Button type="submit" size="xs" disabled={save.isPending}>
              Save
            </Button>
          </form>
        ) : (
          <>
            {address}
            <ArrowRightIcon className="size-3 text-muted-foreground" />
            {forward.remotePort}
            <CopyButton text={address} title="Copy address" />
            {forward.enabled && (
              <Button variant="ghost" size="icon-xs" title={`Open ${url} in the browser`} className="text-muted-foreground" onClick={() => Browser.OpenURL(url)}>
                <ArrowSquareOutIcon />
              </Button>
            )}
          </>
        )}
      </span>
      <span className={cn("flex min-w-0 flex-1 items-center gap-2 truncate text-xs", failed ? "text-destructive" : "text-muted-foreground")}>
        {error && !busy ? String(error) : busy ? `Port ${busy.port} is in use by another program` : stateSentence(forward, status)}
        {busy && (
          <Button
            variant="outline"
            size="xs"
            disabled={save.isPending}
            onClick={() => save.mutate({ localPort: busy.suggested, enabled: true })}
          >
            Use {busy.suggested} instead
          </Button>
        )}
      </span>
      <Switch
        checked={forward.enabled}
        disabled={setEnabled.isPending}
        aria-label={forward.enabled ? "Forward on" : "Forward off"}
        onCheckedChange={(checked) => setEnabled.mutate(checked)}
      />
      <DropdownMenu>
        <DropdownMenuTrigger
          render={<Button variant="ghost" size="icon-xs" title="More" className="text-muted-foreground" />}
        >
          <DotsThreeIcon weight="bold" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            onClick={() => {
              setLocalPort(String(forward.localPort));
              setEditing(true);
            }}
          >
            Change local port…
          </DropdownMenuItem>
          <DropdownMenuItem variant="destructive" onClick={() => remove.mutate()}>
            Delete forward
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

type PortPick = { checked: boolean; localPort: string };

// AddForward starts from a picked target, or from one handed in by the Overview.
export function AddForward({
  cluster,
  saved,
  initial = null,
  onClose,
}: {
  cluster: Cluster;
  saved: PortForward[];
  initial?: Target | null;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { groups, error: listError } = useTargets(cluster);
  const [target, setTarget] = useState<Target | null>(initial);
  const [picks, setPicks] = useState<Record<number, PortPick>>({});
  const forwardable = groups.filter((g) => g.label !== "Workloads");
  const existing = (port: number) => target && saved.find((f) => forwardKey(f) === target.value && f.remotePort === port);
  const pick = (port: number): PortPick => picks[port] ?? { checked: true, localPort: "" };
  const ticked = (target?.ports ?? []).filter((p) => !existing(p.port) && pick(p.port).checked);
  const add = useMutation({
    mutationFn: async () => {
      for (const p of ticked) {
        await ForwardService.Save({
          id: "",
          clusterId: cluster.id,
          target: { kind: forwardKind(target!.kind), namespace: target!.namespace, name: target!.name },
          remotePort: p.port,
          localPort: Number(pick(p.port).localPort) || 0,
          enabled: true,
        });
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: configQuery.queryKey });
      onClose();
    },
  });
  const busy = portInUse(add.error);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Add forward</DialogTitle>
          <DialogDescription>Each ticked port gets its own local port, kept until you delete the forward.</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label>Service or pod</Label>
            <TargetPicker
              groups={forwardable}
              value={target}
              inline
              onPick={(t) => {
                setTarget(t);
                setPicks({});
              }}
            >
              <ComboboxInput placeholder="Search services and pods" autoFocus={!initial} />
            </TargetPicker>
          </div>
          {target && (
            <div className="grid gap-1.5">
              <Label>Ports</Label>
              {target.ports.length === 0 && <p className="text-sm text-muted-foreground">{target.label} declares no ports.</p>}
              {target.ports.map((p) => {
                const already = existing(p.port);
                const current = pick(p.port);
                return (
                  <div key={p.port} className="flex h-8 items-center gap-2">
                    <Checkbox
                      checked={already ? true : current.checked}
                      disabled={!!already}
                      onCheckedChange={(checked) => setPicks({ ...picks, [p.port]: { ...current, checked } })}
                    />
                    <span className="w-40 font-mono text-xs">{portsLabel([p])}</span>
                    {already ? (
                      <span className="font-mono text-xs text-muted-foreground">already on localhost:{already.localPort}</span>
                    ) : (
                      <Input
                        type="number"
                        min={1}
                        max={65535}
                        placeholder="auto"
                        className="h-7 w-24"
                        disabled={!current.checked}
                        value={current.localPort}
                        onChange={(e) => setPicks({ ...picks, [p.port]: { ...current, localPort: e.target.value } })}
                      />
                    )}
                  </div>
                );
              })}
            </div>
          )}
          {(listError || add.error) && (
            <p className="text-sm text-destructive">
              {String(listError ?? add.error)}
              {busy && ` — ${busy.suggested} is free`}
            </p>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={ticked.length === 0 || add.isPending} onClick={() => add.mutate()}>
            Add {ticked.length > 1 ? `${ticked.length} forwards` : "forward"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
