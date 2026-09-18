import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CopyIcon, FloppyDiskIcon, PlayIcon, StopIcon, TrashIcon } from "@phosphor-icons/react";
import { ForwardService } from "@bindings/internal/bindings";
import { TargetKind, type Cluster, type ForwardStatus, type ForwardTarget, type NamedPort, type PortForward } from "@bindings/internal/service";
import { statusLabel, StateDot } from "@/components/routes";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { configQuery, podsQuery, portInUse, servicesQuery } from "@/queries";
import { useUIStore } from "@/store";

type Target = ForwardTarget & { ports: NamedPort[] };

const targetKey = (t: ForwardTarget) => `${t.kind}/${t.namespace}/${t.name}`;
const targetLabel = (t: ForwardTarget) =>
  `${t.kind === TargetKind.TargetService ? "svc" : "pod"} ${t.namespace}/${t.name}`;

type Row = { forward: PortForward; status?: ForwardStatus; saved: boolean };

export function PortForwards({ cluster }: { cluster: Cluster }) {
  const { data } = useQuery(configQuery);
  const statuses = useUIStore((s) => s.forwardStatuses);
  const saved = (data?.forwards ?? []).filter((f) => f.clusterId === cluster.id);
  const rows: Row[] = [
    ...saved.map((f) => ({ forward: f, status: statuses[f.id], saved: true })),
    ...Object.values(statuses)
      .filter((s) => s.forward.clusterId === cluster.id && !saved.some((f) => f.id === s.forward.id))
      .map((s) => ({ forward: s.forward, status: s, saved: false })),
  ];

  return (
    <div className="flex flex-col gap-4 p-4">
      <NewForward cluster={cluster} />
      {rows.length === 0 ? (
        <Empty className="border-0">
          <EmptyHeader>
            <EmptyTitle>No port forwards</EmptyTitle>
            <EmptyDescription>Pick a service or pod above to bind a local port to it.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-8" />
              <TableHead>Target</TableHead>
              <TableHead>Pod</TableHead>
              <TableHead>Remote</TableHead>
              <TableHead>Local address</TableHead>
              <TableHead className="w-24" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <ForwardRow key={row.forward.id} row={row} />
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

function ForwardRow({ row: { forward, status, saved } }: { row: Row }) {
  const queryClient = useQueryClient();
  const invalidateConfig = () => queryClient.invalidateQueries({ queryKey: configQuery.queryKey });
  const start = useMutation({ mutationFn: () => ForwardService.Start(forward) });
  const stop = useMutation({ mutationFn: () => ForwardService.Stop(forward.id) });
  const save = useMutation({ mutationFn: () => ForwardService.Save(forward), onSuccess: invalidateConfig });
  const remove = useMutation({ mutationFn: () => ForwardService.Delete(forward.id), onSuccess: invalidateConfig });
  const address = `127.0.0.1:${forward.localPort}`;
  const error = start.error ?? save.error ?? remove.error;
  return (
    <TableRow>
      <TableCell>
        <StateDot status={status} />
      </TableCell>
      <TableCell>
        {targetLabel(forward.target)}
        {(status?.error || error) && (
          <p className="text-xs text-destructive">{error ? String(error) : statusLabel(status)}</p>
        )}
      </TableCell>
      <TableCell className="text-muted-foreground">{status?.pod}</TableCell>
      <TableCell className="font-mono text-xs">{forward.remotePort}</TableCell>
      <TableCell className="font-mono text-xs">
        <span className="inline-flex items-center gap-1">
          {address}
          <Button variant="ghost" size="icon-xs" title="Copy address" onClick={() => navigator.clipboard.writeText(address)}>
            <CopyIcon />
          </Button>
        </span>
      </TableCell>
      <TableCell>
        <span className="inline-flex gap-1">
          {status ? (
            <Button variant="ghost" size="icon-xs" title="Stop" onClick={() => stop.mutate()}>
              <StopIcon />
            </Button>
          ) : (
            <Button variant="ghost" size="icon-xs" title="Start" disabled={start.isPending} onClick={() => start.mutate()}>
              <PlayIcon />
            </Button>
          )}
          {saved ? (
            <Button variant="ghost" size="icon-xs" title="Delete saved forward" onClick={() => remove.mutate()}>
              <TrashIcon />
            </Button>
          ) : (
            <Button variant="ghost" size="icon-xs" title="Save forward" disabled={save.isPending} onClick={() => save.mutate()}>
              <FloppyDiskIcon />
            </Button>
          )}
        </span>
      </TableCell>
    </TableRow>
  );
}

function NewForward({ cluster }: { cluster: Cluster }) {
  const services = useQuery(servicesQuery(cluster.id));
  const pods = useQuery(podsQuery(cluster.id));
  const targets: Target[] = [
    ...(services.data ?? []).map((s) => ({ kind: TargetKind.TargetService, namespace: s.namespace, name: s.name, ports: s.ports ?? [] })),
    ...(pods.data ?? []).map((p) => ({ kind: TargetKind.TargetPod, namespace: p.namespace, name: p.name, ports: p.ports ?? [] })),
  ];
  const [targetId, setTargetId] = useState("");
  const [remotePort, setRemotePort] = useState("");
  const [localPort, setLocalPort] = useState("");
  const target = targets.find((t) => targetKey(t) === targetId);
  const start = useMutation({
    mutationFn: () =>
      ForwardService.Start({
        id: "",
        clusterId: cluster.id,
        target: { kind: target!.kind, namespace: target!.namespace, name: target!.name },
        remotePort: Number(remotePort),
        localPort: Number(localPort),
      }),
  });
  const busy = portInUse(start.error);
  const selectTarget = (id: string | null) => {
    setTargetId(id ?? "");
    const first = targets.find((t) => targetKey(t) === id)?.ports[0];
    setRemotePort(first ? String(first.port) : "");
    setLocalPort(first ? String(first.port) : "");
  };
  const listError = services.error ?? pods.error;

  return (
    <form
      className="flex flex-wrap items-end gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        start.mutate();
      }}
    >
      <div className="grid gap-1.5">
        <Label>Target</Label>
        <Select
          value={targetId}
          items={targets.map((t) => ({ value: targetKey(t), label: targetLabel(t) }))}
          onValueChange={selectTarget}
        >
          <SelectTrigger className="w-72">
            <SelectValue placeholder="Service or pod" />
          </SelectTrigger>
          <SelectContent>
            {targets.map((t) => (
              <SelectItem key={targetKey(t)} value={targetKey(t)}>
                {targetLabel(t)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="grid gap-1.5">
        <Label>Remote port</Label>
        <Input
          type="number"
          min={1}
          max={65535}
          required
          className="w-28"
          list="remote-ports"
          value={remotePort}
          onChange={(e) => setRemotePort(e.target.value)}
        />
        <datalist id="remote-ports">
          {target?.ports.map((p) => (
            <option key={`${p.name}-${p.port}`} value={p.port}>
              {p.name}
            </option>
          ))}
        </datalist>
      </div>
      <div className="grid gap-1.5">
        <Label>Local port</Label>
        <Input
          type="number"
          min={1}
          max={65535}
          required
          className="w-28"
          value={localPort}
          onChange={(e) => setLocalPort(e.target.value)}
        />
      </div>
      <Button type="submit" disabled={!target || start.isPending}>
        Start
      </Button>
      {busy && (
        <Button type="button" variant="outline" onClick={() => setLocalPort(String(busy.suggested))}>
          Port {busy.port} is in use, use {busy.suggested}
        </Button>
      )}
      {(listError || (start.error && !busy)) && (
        <p className="basis-full text-sm text-destructive">{String(listError ?? start.error)}</p>
      )}
    </form>
  );
}
