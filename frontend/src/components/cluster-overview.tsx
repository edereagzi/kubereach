import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ClusterService } from "@bindings/internal/bindings";
import type { Cluster } from "@bindings/internal/service";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { isForbidden, namespacesQuery, servicesQuery } from "@/queries";

export function ClusterOverview({ cluster }: { cluster: Cluster }) {
  const namespaces = useQuery(namespacesQuery(cluster.id));
  const services = useQuery(servicesQuery(cluster.id));
  const [editing, setEditing] = useState(false);
  const explicit = cluster.namespaces ?? [];

  if (editing || isForbidden(namespaces.error) || isForbidden(services.error)) {
    return (
      <NamespacePrompt
        cluster={cluster}
        forbidden={!editing}
        onDone={() => setEditing(false)}
      />
    );
  }
  const error = namespaces.error ?? services.error;
  if (error) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyTitle>Cluster could not be listed</EmptyTitle>
          <EmptyDescription>{String(error)}</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  return (
    <div className="flex flex-col gap-4 p-4">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="mr-1 text-sm text-muted-foreground">
          {explicit.length ? "Namespaces" : "All namespaces"}
        </span>
        {namespaces.data?.map((ns) => (
          <Badge key={ns} variant="secondary">
            {ns}
          </Badge>
        ))}
        <Button variant="ghost" size="xs" onClick={() => setEditing(true)}>
          Edit scope
        </Button>
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Namespace</TableHead>
            <TableHead>Service</TableHead>
            <TableHead>Ports</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {services.data?.map((s) => (
            <TableRow key={`${s.namespace}/${s.name}`}>
              <TableCell className="text-muted-foreground">{s.namespace}</TableCell>
              <TableCell>{s.name}</TableCell>
              <TableCell className="font-mono text-xs">
                {s.ports?.map((p) => (p.name ? `${p.name}:${p.port}` : p.port)).join(", ")}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function NamespacePrompt({
  cluster,
  forbidden,
  onDone,
}: {
  cluster: Cluster;
  forbidden: boolean;
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const [value, setValue] = useState(cluster.namespaces?.join(", ") ?? "");
  const save = useMutation({
    mutationFn: (namespaces: string[]) => ClusterService.SetNamespaces(cluster.id, namespaces),
    onSuccess: async () => {
      await queryClient.invalidateQueries();
      onDone();
    },
  });
  const namespaces = value.split(/[\s,]+/).filter(Boolean);

  return (
    <form
      className="flex max-w-lg flex-col gap-3 p-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate(namespaces);
      }}
    >
      <div>
        <p className="text-sm font-medium">
          {forbidden ? "Cluster-wide listing is forbidden" : "Namespace scope"}
        </p>
        <p className="text-sm text-muted-foreground">
          Enter the namespaces you may use. They are remembered for this cluster.
          {!forbidden && " Leave empty to list all namespaces."}
        </p>
      </div>
      <div className="flex gap-2">
        <Input
          autoFocus
          placeholder="default, payments"
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
        <Button type="submit" disabled={(forbidden && namespaces.length === 0) || save.isPending}>
          Save
        </Button>
        {!forbidden && (
          <Button type="button" variant="ghost" onClick={onDone}>
            Cancel
          </Button>
        )}
      </div>
      {save.error && <p className="text-sm text-destructive">{String(save.error)}</p>}
    </form>
  );
}
