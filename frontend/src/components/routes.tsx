import { useEffect, useState } from "react";
import { useMutation, useMutationState, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDownIcon, ArrowUpIcon, PencilSimpleIcon, PlusIcon, TrashIcon } from "@phosphor-icons/react";
import { RouteService } from "@bindings/internal/bindings";
import {
  AuthMethod,
  State,
  type Cluster,
  type HostKeyPrompt,
  type Route,
  type RouteStatus,
  type SSHServer,
} from "@bindings/internal/service";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { configQuery, credentialRequired, errorText } from "@/queries";
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

type Status = { state: State; error?: string };

export function statusLabel(status?: Status) {
  if (!status) return "not connected";
  return status.error || status.state;
}

// Hollow is the deliberate off state, distinct from idle's filled grey.
export function StateDot({ status, hollow = false }: { status?: Status; hollow?: boolean }) {
  return (
    <span
      title={hollow ? "off" : statusLabel(status)}
      className={cn(
        "size-2 shrink-0 rounded-full",
        hollow ? "border-[1.5px] border-muted-foreground/70" : stateColor[status?.state ?? State.StateIdle],
      )}
    />
  );
}

export const isUp = (s?: RouteStatus) => s?.state === State.StateConnecting || s?.state === State.StateConnected || s?.state === State.StateReconnecting;

// Why a Route is not working: a Connect refused before it started, or the error it stopped on.
export function useRouteProblem(routeId: string) {
  return useUIStore((s) => s.connectErrors[routeId] ?? (s.routeStatuses[routeId]?.state === State.StateError ? s.routeStatuses[routeId]?.error : undefined));
}

// A mutation's own state only follows its latest call, so each Route's pending call is looked up among all of them.
function usePendingRoutes() {
  return useMutationState({
    filters: { mutationKey: ["route"], status: "pending" },
    select: (m) => (m.state.variables as { route: Route }).route.id,
  });
}

function useStopRoute() {
  return useMutation({ mutationKey: ["route"], mutationFn: ({ route }: { route: Route }) => RouteService.Stop(route.id) });
}

// RouteConnector runs every Connect the window asks for, so a Route's credential and host key prompts have one home.
export function RouteConnector() {
  const { data } = useQuery(configQuery);
  const connectRequest = useUIStore((s) => s.connectRequest);
  const requestConnect = useUIStore((s) => s.requestConnect);
  const setConnectError = useUIStore((s) => s.setConnectError);
  const connectErrors = useUIStore((s) => s.connectErrors);
  const hostKeyPrompt = useUIStore((s) => s.hostKeyPrompts[0]);
  const [credential, setCredential] = useState<CredentialRequest | null>(null);
  const pending = usePendingRoutes();

  // A wrong secret is a plain error and keeps the dialog open; a missing one opens or re-targets it.
  const connect = useMutation({
    mutationKey: ["route"],
    mutationFn: ({ route, secret = "" }: { route: Route; secret?: string }) => RouteService.Connect(route.id, secret),
    onMutate: ({ route }) => setConnectError(route.id, null),
    onSuccess: () => setCredential(null),
    onError: (error, { route }) => {
      const needed = credentialRequired(error);
      if (needed) setCredential({ route, ...needed });
      else setConnectError(route.id, errorText(error));
    },
  });

  useEffect(() => {
    if (!connectRequest) return;
    const route = data?.routes?.find((r) => r.id === connectRequest);
    if (route) connect.mutate({ route });
    requestConnect(null);
  }, [connectRequest, data?.routes, connect.mutate, requestConnect]);

  return (
    <>
      {credential && (
        <CredentialDialog
          key={credential.target}
          request={credential}
          error={connectErrors[credential.route.id] ?? null}
          pending={pending.includes(credential.route.id)}
          onSubmit={(secret) => connect.mutate({ route: credential.route, secret })}
          onClose={() => setCredential(null)}
        />
      )}
      {hostKeyPrompt && <HostKeyDialog prompt={hostKeyPrompt} />}
    </>
  );
}

function ConnectButton({ route }: { route: Route }) {
  const status = useUIStore((s) => s.routeStatuses[route.id]);
  const requestConnect = useUIStore((s) => s.requestConnect);
  const problem = useRouteProblem(route.id);
  const pending = usePendingRoutes();
  const stop = useStopRoute();
  return (
    <Button
      variant={isUp(status) ? "ghost" : "outline"}
      size="xs"
      disabled={pending.includes(route.id)}
      onClick={() => (isUp(status) ? stop.mutate({ route }) : requestConnect(route.id))}
    >
      {isUp(status) ? "Disconnect" : problem ? "Retry" : "Connect"}
    </Button>
  );
}

// RouteChip names the Route a Cluster is reached through and holds what can be done to it from the Cluster.
export function RouteChip({ cluster }: { cluster: Cluster }) {
  const { data } = useQuery(configQuery);
  const status = useUIStore((s) => s.routeStatuses[cluster.route]);
  const requestConnect = useUIStore((s) => s.requestConnect);
  const openRoutes = useUIStore((s) => s.openRoutes);
  const problem = useRouteProblem(cluster.route);
  const pending = usePendingRoutes();
  const stop = useStopRoute();
  const [editing, setEditing] = useState(false);
  const route = data?.routes?.find((r) => r.id === cluster.route);
  if (!route) return null;
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          title={problem ?? statusLabel(status)}
          className="inline-flex h-7 max-w-48 items-center gap-1.5 rounded-full border px-2.5 text-xs text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 aria-expanded:bg-muted"
        >
          <StateDot status={status} />
          <span className="truncate">via {route.name}</span>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-52">
          <DropdownMenuItem
            disabled={pending.includes(route.id)}
            onClick={() => (isUp(status) ? stop.mutate({ route }) : requestConnect(route.id))}
          >
            {isUp(status) ? "Disconnect" : problem ? "Retry" : "Connect"}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => setEditing(true)}>Edit {route.name}…</DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={openRoutes}>All routes</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {editing && <RouteDialog route={route} onClose={() => setEditing(false)} />}
    </>
  );
}

export function RoutesPage() {
  const queryClient = useQueryClient();
  const { data } = useQuery(configQuery);
  const [editing, setEditing] = useState<Route | null | undefined>();
  const importSSHConfig = useMutation({
    mutationFn: () => RouteService.ImportSSHConfig(),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["config"] }),
  });
  const routes = data?.routes ?? [];
  return (
    <>
      <div data-drag className="flex h-13 shrink-0 items-center gap-2 px-4 select-none">
        <span className="flex-1 text-base font-semibold">Routes</span>
        <Button variant="ghost" size="sm" disabled={importSSHConfig.isPending} onClick={() => importSSHConfig.mutate()}>
          Import from SSH config…
        </Button>
        <Button variant="outline" size="sm" onClick={() => setEditing(null)}>
          <PlusIcon /> New route
        </Button>
      </div>
      {importSSHConfig.error && <p className="px-4 pb-2 text-xs text-destructive">{errorText(importSSHConfig.error)}</p>}
      <div className="min-h-0 flex-1 overflow-auto border-t">
        {routes.length === 0 ? (
          <Empty className="border-0">
            <EmptyHeader>
              <EmptyTitle>No routes</EmptyTitle>
              <EmptyDescription>A route reaches a cluster this machine cannot reach directly, through one or more SSH servers.</EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <ul className="divide-y">
            {routes.map((r) => (
              <RouteRow key={r.id} route={r} clusters={(data?.clusters ?? []).filter((c) => c.route === r.id)} onEdit={() => setEditing(r)} />
            ))}
          </ul>
        )}
      </div>
      {editing !== undefined && <RouteDialog route={editing} onClose={() => setEditing(undefined)} />}
    </>
  );
}

function RouteRow({ route, clusters, onEdit }: { route: Route; clusters: Cluster[]; onEdit: () => void }) {
  const status = useUIStore((s) => s.routeStatuses[route.id]);
  const selectCluster = useUIStore((s) => s.selectCluster);
  const problem = useRouteProblem(route.id);
  return (
    <li className="group flex items-start gap-3 px-4 py-3">
      <span className="flex h-5 items-center">
        <StateDot status={status} />
      </span>
      <div className="grid min-w-0 flex-1 gap-0.5">
        <div className="flex min-w-0 items-baseline gap-3">
          <span className="text-sm font-medium">{route.name}</span>
          <span className="truncate text-xs text-muted-foreground">
            {(route.servers ?? []).map((s) => (s.user ? `${s.user}@${s.host}` : s.host)).join(" → ")}
          </span>
        </div>
        {problem && (
          <p className="truncate text-xs text-destructive" title={problem}>
            {problem}
          </p>
        )}
        <p className="text-xs text-muted-foreground">
          {clusters.length === 0
            ? "No cluster uses it"
            : clusters.map((c, i) => (
                <span key={c.id}>
                  {i > 0 && ", "}
                  <button type="button" className="text-foreground/80 underline-offset-2 outline-none hover:underline focus-visible:underline" onClick={() => selectCluster(c.id)}>
                    {c.name}
                  </button>
                </span>
              ))}
        </p>
      </div>
      <Button variant="ghost" size="icon-xs" title={`Edit ${route.name}`} onClick={onEdit}>
        <PencilSimpleIcon />
      </Button>
      <ConnectButton route={route} />
    </li>
  );
}

export type CredentialRequest = { route: Route; code: "passphrase" | "password"; target: string };

const emptyServer: SSHServer = { host: "", port: 22, user: "", auth: AuthMethod.AuthAgent, keyFile: "" };

export function RouteDialog({ route, onClose, onSaved }: { route: Route | null; onClose: () => void; onSaved?: (route: Route) => void }) {
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
    onSuccess: (saved) => {
      onSaved?.(saved);
      return done();
    },
  });
  const remove = useMutation({ mutationFn: () => RouteService.Delete(route!.id), onSuccess: done });
  const [deleting, setDeleting] = useState(false);
  const { data } = useQuery(configQuery);
  const clusters = (data?.clusters ?? []).filter((c) => route && c.route === route.id).map((c) => c.name);

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
          {save.error && <p className="text-sm text-destructive">{errorText(save.error)}</p>}
          <DialogFooter>
            {route && (
              <Button
                type="button"
                variant="destructive"
                className="mr-auto"
                onClick={() => {
                  remove.reset();
                  setDeleting(true);
                }}
              >
                Delete…
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
        {route && (
          <ConfirmDialog
            open={deleting}
            onOpenChange={setDeleting}
            title={`Delete route ${route.name}?`}
            description={
              clusters.length > 0
                ? (
                  <>
                    {clusters.length > 1 ? "Clusters" : "Cluster"} <span className="font-medium text-foreground">{clusters.join(", ")}</span>{" "}
                    {clusters.length > 1 ? "use" : "uses"} this route. Switch {clusters.length > 1 ? "them" : "it"} to another route or Direct first.
                  </>
                )
                : "Kubereach forgets this route and its SSH servers, and disconnects it if connected. Your SSH config and keys stay as they are."
            }
            confirm="Delete route"
            destructive
            disabled={clusters.length > 0}
            action={remove}
          />
        )}
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

export function CredentialDialog({
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

export function HostKeyDialog({ prompt }: { prompt: HostKeyPrompt }) {
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
