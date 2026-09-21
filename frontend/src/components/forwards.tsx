import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CopyIcon, PencilSimpleIcon, PlusIcon, TrashIcon } from "@phosphor-icons/react";
import { ForwardService } from "@bindings/internal/bindings";
import { TargetKind, type Cluster, type ForwardTarget, type NamedPort, type PortForward } from "@bindings/internal/service";
import { statusLabel, StateDot } from "@/components/routes";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Combobox, ComboboxContent, ComboboxEmpty, ComboboxInput, ComboboxItem, ComboboxList } from "@/components/ui/combobox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { configQuery, podsQuery, portInUse, servicesQuery } from "@/queries";
import { useUIStore } from "@/store";

type Target = ForwardTarget & { ports: NamedPort[] };

const targetLabel = (t: ForwardTarget) =>
  `${t.kind === TargetKind.TargetService ? "svc" : "pod"} ${t.namespace}/${t.name}`;

export function PortForwards({ cluster }: { cluster: Cluster }) {
  const { data } = useQuery(configQuery);
  const [adding, setAdding] = useState(false);
  const saved = (data?.forwards ?? []).filter((f) => f.clusterId === cluster.id);

  return (
    <div className="flex flex-col gap-4 p-4">
      <div>
        <Button size="sm" onClick={() => setAdding(true)}>
          <PlusIcon /> Add forward
        </Button>
      </div>
      {adding && <AddForward cluster={cluster} saved={saved} onClose={() => setAdding(false)} />}
      {saved.length === 0 ? (
        <Empty className="border-0">
          <EmptyHeader>
            <EmptyTitle>No port forwards</EmptyTitle>
            <EmptyDescription>Add a service or pod to get a stable local port for each of its ports.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-8" />
              <TableHead>Target</TableHead>
              <TableHead>Local address</TableHead>
              <TableHead className="w-16">On</TableHead>
              <TableHead className="w-10" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {saved.map((f) => (
              <ForwardRow key={f.id} forward={f} />
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

function ForwardRow({ forward }: { forward: PortForward }) {
  const queryClient = useQueryClient();
  const status = useUIStore((s) => s.forwardStatuses[forward.id]);
  const [editing, setEditing] = useState(false);
  const [localPort, setLocalPort] = useState("");
  const invalidateConfig = () => queryClient.invalidateQueries({ queryKey: configQuery.queryKey });
  const setEnabled = useMutation({
    mutationFn: (enabled: boolean) => ForwardService.SetEnabled(forward.id, enabled),
    onSuccess: invalidateConfig,
  });
  const save = useMutation({
    mutationFn: (port: number) => ForwardService.Save({ ...forward, localPort: port }),
    onSuccess: () => {
      setEditing(false);
      invalidateConfig();
    },
  });
  const remove = useMutation({ mutationFn: () => ForwardService.Delete(forward.id), onSuccess: invalidateConfig });
  const address = `localhost:${forward.localPort}`;
  const error = setEnabled.error ?? remove.error ?? (editing ? null : save.error);
  const busy = portInUse(save.error);
  return (
    <TableRow>
      <TableCell>{forward.enabled && <StateDot status={status} />}</TableCell>
      <TableCell>
        {targetLabel(forward.target)}
        <span className="ml-1 font-mono text-xs text-muted-foreground">:{forward.remotePort}</span>
        {status?.pod && <span className="ml-2 text-xs text-muted-foreground">{status.pod}</span>}
        {(status?.error || error) && (
          <p className="text-xs text-destructive">{error ? String(error) : statusLabel(status)}</p>
        )}
      </TableCell>
      <TableCell className="font-mono text-xs">
        {editing ? (
          <form
            className="flex items-center gap-1"
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate(Number(localPort));
            }}
          >
            <Input
              type="number"
              min={1}
              max={65535}
              autoFocus
              className="h-7 w-24"
              value={localPort}
              onChange={(e) => setLocalPort(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && setEditing(false)}
            />
            <Button type="submit" size="xs" disabled={save.isPending}>
              Save
            </Button>
            {busy && (
              <Button type="button" size="xs" variant="outline" onClick={() => setLocalPort(String(busy.suggested))}>
                {busy.port} in use, use {busy.suggested}
              </Button>
            )}
            {save.error && !busy && <span className="text-destructive">{String(save.error)}</span>}
          </form>
        ) : (
          <span className="inline-flex items-center gap-1">
            {address}
            <Button variant="ghost" size="icon-xs" title="Copy address" onClick={() => navigator.clipboard.writeText(address)}>
              <CopyIcon />
            </Button>
            <Button
              variant="ghost"
              size="icon-xs"
              title="Change local port"
              onClick={() => {
                setLocalPort(String(forward.localPort));
                setEditing(true);
              }}
            >
              <PencilSimpleIcon />
            </Button>
          </span>
        )}
      </TableCell>
      <TableCell>
        <Switch
          checked={forward.enabled}
          disabled={setEnabled.isPending}
          onCheckedChange={(checked) => setEnabled.mutate(checked)}
        />
      </TableCell>
      <TableCell>
        <Button variant="ghost" size="icon-xs" title="Delete forward" onClick={() => remove.mutate()}>
          <TrashIcon />
        </Button>
      </TableCell>
    </TableRow>
  );
}

type PortPick = { checked: boolean; localPort: string };

function AddForward({ cluster, saved, onClose }: { cluster: Cluster; saved: PortForward[]; onClose: () => void }) {
  const queryClient = useQueryClient();
  const services = useQuery(servicesQuery(cluster.id));
  const pods = useQuery(podsQuery(cluster.id));
  const targets: Target[] = [
    ...(services.data ?? []).map((s) => ({ kind: TargetKind.TargetService, namespace: s.namespace, name: s.name, ports: s.ports ?? [] })),
    ...(pods.data ?? []).map((p) => ({ kind: TargetKind.TargetPod, namespace: p.namespace, name: p.name, ports: p.ports ?? [] })),
  ];
  const namespaces = [...new Set(targets.map((t) => t.namespace))].sort();
  const [namespace, setNamespace] = useState("");
  const [kind, setKind] = useState<TargetKind>(TargetKind.TargetService);
  const [name, setName] = useState<string | null>(null);
  const [picks, setPicks] = useState<Record<number, PortPick>>({});
  const names = targets.filter((t) => t.namespace === namespace && t.kind === kind).map((t) => t.name);
  const target = targets.find((t) => t.namespace === namespace && t.kind === kind && t.name === name);
  const existing = (port: number) =>
    saved.find((f) => f.target.kind === kind && f.target.namespace === namespace && f.target.name === name && f.remotePort === port);
  const pick = (port: number): PortPick => picks[port] ?? { checked: true, localPort: "" };
  const ticked = (target?.ports ?? []).filter((p) => !existing(p.port) && pick(p.port).checked);
  const add = useMutation({
    mutationFn: async () => {
      for (const p of ticked) {
        await ForwardService.Save({
          id: "",
          clusterId: cluster.id,
          target: { kind, namespace, name: name! },
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
  const listError = services.error ?? pods.error;

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add forward</DialogTitle>
          <DialogDescription>Each ticked port becomes a Saved Forward on its own local port.</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="flex gap-2">
            <div className="grid gap-1.5">
              <Label>Namespace</Label>
              <Select
                value={namespace}
                items={namespaces.map((ns) => ({ value: ns, label: ns }))}
                onValueChange={(ns) => {
                  setNamespace(ns ?? "");
                  setName(null);
                }}
              >
                <SelectTrigger className="w-44">
                  <SelectValue placeholder="Namespace" />
                </SelectTrigger>
                <SelectContent>
                  {namespaces.map((ns) => (
                    <SelectItem key={ns} value={ns}>
                      {ns}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>Kind</Label>
              <Select
                value={kind}
                items={[
                  { value: TargetKind.TargetService, label: "Service" },
                  { value: TargetKind.TargetPod, label: "Pod" },
                ]}
                onValueChange={(k) => {
                  setKind(k ?? TargetKind.TargetService);
                  setName(null);
                }}
              >
                <SelectTrigger className="w-28">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={TargetKind.TargetService}>Service</SelectItem>
                  <SelectItem value={TargetKind.TargetPod}>Pod</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label>{kind === TargetKind.TargetService ? "Service" : "Pod"}</Label>
            <Combobox
              items={names}
              value={name}
              onValueChange={(v) => {
                setName(v);
                setPicks({});
              }}
            >
              <ComboboxInput placeholder={namespace ? "Search…" : "Pick a namespace first"} disabled={!namespace} />
              <ComboboxContent>
                <ComboboxEmpty>Nothing found.</ComboboxEmpty>
                <ComboboxList>{(n: string) => <ComboboxItem key={n} value={n}>{n}</ComboboxItem>}</ComboboxList>
              </ComboboxContent>
            </Combobox>
          </div>
          {target && (
            <div className="grid gap-1.5">
              <Label>Ports</Label>
              {target.ports.length === 0 && <p className="text-sm text-muted-foreground">This target declares no ports.</p>}
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
                    <span className="w-40 font-mono text-xs">
                      {p.port}
                      {p.name && <span className="ml-1 text-muted-foreground">{p.name}</span>}
                    </span>
                    {already ? (
                      <span className="font-mono text-xs text-muted-foreground">localhost:{already.localPort}</span>
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
