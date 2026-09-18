import { useQuery } from "@tanstack/react-query";
import { CubeIcon, WarningIcon } from "@phosphor-icons/react";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { configQuery } from "@/queries";
import { useUIStore } from "@/store";
import { cn } from "@/lib/utils";

export function Shell() {
  return (
    <div className="flex h-screen bg-background text-foreground">
      <aside className="flex w-64 shrink-0 flex-col border-r">
        <div className="flex h-12 items-center border-b px-4 text-sm font-medium">Clusters</div>
        <ClusterList />
      </aside>
      <main className="flex min-w-0 flex-1 flex-col">
        <ClusterTabs />
      </main>
    </div>
  );
}

function ClusterList() {
  const { data, error } = useQuery(configQuery);
  const { selectedClusterId, selectCluster } = useUIStore();
  const clusters = data?.clusters ?? [];

  if (error) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <WarningIcon />
          </EmptyMedia>
          <EmptyTitle>Configuration could not be loaded</EmptyTitle>
          <EmptyDescription>{String(error)}</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  if (clusters.length === 0) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <CubeIcon />
          </EmptyMedia>
          <EmptyTitle>No clusters</EmptyTitle>
          <EmptyDescription>Import a kubeconfig to add your first cluster.</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  return (
    <ul className="flex flex-col gap-1 p-2">
      {clusters.map((c) => (
        <li key={c.id}>
          <button
            type="button"
            onClick={() => selectCluster(c.id)}
            className={cn(
              "w-full truncate rounded-md px-3 py-2 text-left text-sm hover:bg-accent",
              c.id === selectedClusterId && "bg-accent",
            )}
          >
            {c.name}
          </button>
        </li>
      ))}
    </ul>
  );
}

function ClusterTabs() {
  const selectedClusterId = useUIStore((s) => s.selectedClusterId);

  if (!selectedClusterId) {
    return (
      <Empty className="h-full border-0">
        <EmptyHeader>
          <EmptyTitle>Select a cluster</EmptyTitle>
          <EmptyDescription>Port Forwards, Logs, Shell and Expose live here.</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  return (
    <Tabs defaultValue="forwards" className="h-full">
      <TabsList className="m-2">
        <TabsTrigger value="forwards">Port Forwards</TabsTrigger>
        <TabsTrigger value="logs">Logs</TabsTrigger>
        <TabsTrigger value="shell">Shell</TabsTrigger>
        <TabsTrigger value="expose">Expose Mode</TabsTrigger>
      </TabsList>
    </Tabs>
  );
}
