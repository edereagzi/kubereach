import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowDownIcon,
  ArrowUpIcon,
  FolderOpenIcon,
  PencilSimpleIcon,
  PlayIcon,
  PlusIcon,
  StopIcon,
  TrashIcon,
} from "@phosphor-icons/react";
import { RouteService } from "@bindings/internal/bindings";
import {
  AuthMethod,
  State,
  type HostKeyPrompt,
  type Route,
  type RouteStatus,
  type SSHServer,
} from "@bindings/internal/service";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { configQuery, credentialRequired } from "@/queries";
import { useUIStore } from "@/store";
import { cn } from "@/lib/utils";

const stateColor: Record<State, string> = {
  [State.$zero]: "bg-muted-foreground",
  [State.StateIdle]: "bg-muted-foreground",
  [State.StateConnecting]: "animate-pulse bg-amber-500",
  [State.StateConnected]: "bg-green-500",
  [State.StateReconnecting]: "animate-pulse bg-amber-500",
  [State.StateStopped]: "bg-muted-foreground",
  [State.StateError]: "bg-red-500",
};

const isUp = (s?: RouteStatus) =>
  s?.state === State.StateConnecting ||
  s?.state === State.StateConnected ||
  s?.state === State.StateReconnecting;

export function routeStatusLabel(status?: RouteStatus) {
  if (!status) return "not connected";
  return status.error ? `${status.state}: ${status.error}` : status.state;
}

export function StateDot({ status }: { status?: RouteStatus }) {
  return (
    <span
      title={routeStatusLabel(status)}
      className={cn("size-2 shrink-0 rounded-full", stateColor[status?.state ?? State.StateIdle])}
    />
  );
}

export function RouteList() {
  const queryClient = useQueryClient();
  const { data } = useQuery(configQuery);
  const routeStatuses = useUIStore((s) => s.routeStatuses);
  const hostKeyPrompt = useUIStore((s) => s.hostKeyPrompts[0]);
  const [editing, setEditing] = useState<Route | null | undefined>();
  const [credential, setCredential] = useState<CredentialRequest | null>(null);
  const routes = data?.routes ?? [];

  // A wrong secret is a plain error and keeps the dialog open; a missing one opens or re-targets it.
  const connect = useMutation({
    mutationFn: ({ route, secret = "" }: { route: Route; secret?: string }) => RouteService.Connect(route.id, secret),
    onSuccess: () => setCredential(null),
    onError: (error, { route }) => {
      const needed = credentialRequired(error);
      if (needed) setCredential({ route, ...needed });
    },
  });
  const stop = useMutation({ mutationFn: (route: Route) => RouteService.Stop(route.id) });
  const importSSHConfig = useMutation({
    mutationFn: () => RouteService.ImportSSHConfig(),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["config"] }),
  });
  const connectError = connect.error && !credentialRequired(connect.error) ? String(connect.error) : null;
  const listError = (!credential && connectError) || (importSSHConfig.error ? String(importSSHConfig.error) : null);

  return (
    <div className="flex flex-col border-t">
      <div className="flex h-10 items-center gap-1 px-4 text-sm font-medium">
        <span className="flex-1">Routes</span>
        <Button
          variant="ghost"
          size="icon-sm"
          title="Import SSH config"
          disabled={importSSHConfig.isPending}
          onClick={() => importSSHConfig.mutate()}
        >
          <FolderOpenIcon />
        </Button>
        <Button variant="ghost" size="icon-sm" title="New route" onClick={() => setEditing(null)}>
          <PlusIcon />
        </Button>
      </div>
      {listError && <p className="px-4 pb-2 text-xs text-destructive">{listError}</p>}
      <ul className="flex flex-col gap-1 px-2 pb-2">
        {routes.map((r) => {
          const status = routeStatuses[r.id];
          const up = isUp(status);
          return (
            <li key={r.id} className="group flex items-center gap-2 rounded-md px-3 py-1 text-sm hover:bg-accent">
              <StateDot status={status} />
              <span className="flex-1 truncate" title={routeStatusLabel(status)}>
                {r.name}
              </span>
              <Button
                variant="ghost"
                size="icon-xs"
                title="Edit route"
                className="opacity-0 group-hover:opacity-100"
                onClick={() => setEditing(r)}
              >
                <PencilSimpleIcon />
              </Button>
              <Button
                variant="ghost"
                size="icon-xs"
                title={up ? "Stop" : "Connect"}
                disabled={connect.isPending || stop.isPending}
                onClick={() => (up ? stop.mutate(r) : connect.mutate({ route: r }))}
              >
                {up ? <StopIcon /> : <PlayIcon />}
              </Button>
            </li>
          );
        })}
      </ul>
      {editing !== undefined && <RouteDialog route={editing} onClose={() => setEditing(undefined)} />}
      {credential && (
        <CredentialDialog
          key={credential.target}
          request={credential}
          error={connectError}
          pending={connect.isPending}
          onSubmit={(secret) => connect.mutate({ route: credential.route, secret })}
          onClose={() => setCredential(null)}
        />
      )}
      {hostKeyPrompt && <HostKeyDialog prompt={hostKeyPrompt} />}
    </div>
  );
}

type CredentialRequest = { route: Route; code: "passphrase" | "password"; target: string };

const emptyServer: SSHServer = { host: "", port: 22, user: "", auth: AuthMethod.AuthAgent, keyFile: "" };

function RouteDialog({ route, onClose }: { route: Route | null; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState(route?.name ?? "");
  const [servers, setServers] = useState<SSHServer[]>(route?.servers?.length ? route.servers : [emptyServer]);
  const patch = (i: number, p: Partial<SSHServer>) =>
    setServers((list) => list.map((s, j) => (j === i ? { ...s, ...p } : s)));
  const move = (i: number, to: number) =>
    setServers((list) => {
      const next = [...list];
      [next[i], next[to]] = [next[to]!, next[i]!];
      return next;
    });
  const done = async () => {
    await queryClient.invalidateQueries({ queryKey: ["config"] });
    onClose();
  };
  const save = useMutation({
    mutationFn: () => RouteService.Save({ id: route?.id ?? "", name, servers }),
    onSuccess: done,
  });
  const remove = useMutation({ mutationFn: () => RouteService.Delete(route!.id), onSuccess: done });
  const error = save.error ?? remove.error;

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <form
          className="contents"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>{route ? "Edit route" : "New route"}</DialogTitle>
            <DialogDescription>
              SSH servers in the order they are reached; each one is dialled through the previous.
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-4">
            <Field label="Name">
              <Input autoFocus required value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
            {servers.map((server, i) => (
              <ServerFields
                key={i}
                index={i}
                server={server}
                canRemove={servers.length > 1}
                canMoveUp={i > 0}
                canMoveDown={i < servers.length - 1}
                onChange={(p) => patch(i, p)}
                onMoveUp={() => move(i, i - 1)}
                onMoveDown={() => move(i, i + 1)}
                onRemove={() => setServers((list) => list.filter((_, j) => j !== i))}
              />
            ))}
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="justify-self-start"
              onClick={() => setServers((list) => [...list, emptyServer])}
            >
              <PlusIcon /> Add SSH server
            </Button>
          </div>
          {error && <p className="text-sm text-destructive">{String(error)}</p>}
          <DialogFooter>
            {route && (
              <Button
                type="button"
                variant="destructive"
                className="mr-auto"
                disabled={remove.isPending}
                onClick={() => remove.mutate()}
              >
                Delete
              </Button>
            )}
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending}>
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ServerFields({
  index,
  server,
  canRemove,
  canMoveUp,
  canMoveDown,
  onChange,
  onMoveUp,
  onMoveDown,
  onRemove,
}: {
  index: number;
  server: SSHServer;
  canRemove: boolean;
  canMoveUp: boolean;
  canMoveDown: boolean;
  onChange: (p: Partial<SSHServer>) => void;
  onMoveUp: () => void;
  onMoveDown: () => void;
  onRemove: () => void;
}) {
  const pickKey = async () => {
    const path = await RouteService.PickKeyFile();
    if (path) onChange({ keyFile: path });
  };
  return (
    <fieldset className="grid gap-3 rounded-md border p-3">
      <div className="flex items-center gap-1">
        <legend className="flex-1 text-sm font-medium">SSH server {index + 1}</legend>
        <Button type="button" variant="ghost" size="icon-xs" title="Move up" disabled={!canMoveUp} onClick={onMoveUp}>
          <ArrowUpIcon />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          title="Move down"
          disabled={!canMoveDown}
          onClick={onMoveDown}
        >
          <ArrowDownIcon />
        </Button>
        <Button type="button" variant="ghost" size="icon-xs" title="Remove" disabled={!canRemove} onClick={onRemove}>
          <TrashIcon />
        </Button>
      </div>
      <div className="grid grid-cols-[1fr_5rem] gap-2">
        <Field label="Host">
          <Input required value={server.host} onChange={(e) => onChange({ host: e.target.value })} />
        </Field>
        <Field label="Port">
          <Input
            type="number"
            min={1}
            max={65535}
            required
            value={server.port}
            onChange={(e) => onChange({ port: Number(e.target.value) })}
          />
        </Field>
      </div>
      <Field label="Username">
        <Input required value={server.user} onChange={(e) => onChange({ user: e.target.value })} />
      </Field>
      <Field label="Authentication">
        <Select value={server.auth} onValueChange={(auth) => onChange({ auth: auth ?? AuthMethod.AuthAgent })}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={AuthMethod.AuthAgent}>SSH agent</SelectItem>
            <SelectItem value={AuthMethod.AuthKeyFile}>Key file</SelectItem>
            <SelectItem value={AuthMethod.AuthPassword}>Password (asked on connect)</SelectItem>
          </SelectContent>
        </Select>
      </Field>
      {server.auth === AuthMethod.AuthKeyFile && (
        <Field label="Key file">
          <div className="flex gap-2">
            <Input required value={server.keyFile} onChange={(e) => onChange({ keyFile: e.target.value })} />
            <Button type="button" variant="outline" onClick={pickKey}>
              Browse
            </Button>
          </div>
        </Field>
      )}
    </fieldset>
  );
}

function CredentialDialog({
  request: { code, target },
  error,
  pending,
  onSubmit,
  onClose,
}: {
  request: CredentialRequest;
  error: string | null;
  pending: boolean;
  onSubmit: (secret: string) => void;
  onClose: () => void;
}) {
  const [secret, setSecret] = useState("");
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <form
          className="contents"
          onSubmit={(e) => {
            e.preventDefault();
            onSubmit(secret);
          }}
        >
          <DialogHeader>
            <DialogTitle>{code === "passphrase" ? "Key passphrase" : "Password"}</DialogTitle>
            <DialogDescription>
              {code === "passphrase" ? `${target} is protected.` : `${target} asks for a password.`} It is kept in
              memory for this session only.
            </DialogDescription>
          </DialogHeader>
          <Input autoFocus type="password" value={secret} onChange={(e) => setSecret(e.target.value)} />
          {error && <p className="text-sm text-destructive">{error}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !secret}>
              Connect
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function HostKeyDialog({ prompt }: { prompt: HostKeyPrompt }) {
  const removeHostKeyPrompt = useUIStore((s) => s.removeHostKeyPrompt);
  const answer = useMutation({
    mutationFn: (accept: boolean) => RouteService.AnswerHostKey(prompt.routeId, accept),
    onSettled: () => removeHostKeyPrompt(prompt.routeId),
  });
  return (
    <Dialog open onOpenChange={(open) => !open && answer.mutate(false)}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Unknown host key</DialogTitle>
          <DialogDescription>
            {prompt.address} is not in your known_hosts. Accepting adds this key so it is checked on every later
            connection.
          </DialogDescription>
        </DialogHeader>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Key type</dt>
          <dd>{prompt.keyType}</dd>
          <dt className="text-muted-foreground">Fingerprint</dt>
          <dd className="font-mono break-all">{prompt.fingerprint}</dd>
        </dl>
        <DialogFooter>
          <Button variant="ghost" disabled={answer.isPending} onClick={() => answer.mutate(false)}>
            Reject
          </Button>
          <Button disabled={answer.isPending} onClick={() => answer.mutate(true)}>
            Accept
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label>{label}</Label>
      {children}
    </div>
  );
}
