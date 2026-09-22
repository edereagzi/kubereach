import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { EyeIcon, EyeSlashIcon } from "@phosphor-icons/react";
import { ObjectKind, type Cluster } from "@bindings/internal/service";
import { CopyButton } from "@/components/copy-button";
import type { Kind, Target } from "@/components/targets";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { yamlQuery } from "@/queries";

const objectKind: Record<Kind, ObjectKind> = {
  svc: ObjectKind.ObjectService,
  deploy: ObjectKind.ObjectDeployment,
  sts: ObjectKind.ObjectStatefulSet,
  ds: ObjectKind.ObjectDaemonSet,
  cron: ObjectKind.ObjectCronJob,
  pod: ObjectKind.ObjectPod,
  cm: ObjectKind.ObjectConfigMap,
  secret: ObjectKind.ObjectSecret,
  ing: ObjectKind.ObjectIngress,
  node: ObjectKind.ObjectNode,
};

type YamlProps = { cluster: Cluster; kind: Kind; namespace: string; name: string };

// DetailTabs puts a detail's own content and its YAML behind two tabs; the YAML is fetched when its tab opens.
export function DetailTabs({ children, ...props }: YamlProps & { children: ReactNode }) {
  return (
    <Tabs defaultValue="details" className="min-h-0 flex-1 gap-0">
      <TabsList variant="line" className="h-8 w-full justify-start gap-4 border-b">
        <TabsTrigger value="details" className="flex-none px-0">
          Details
        </TabsTrigger>
        <TabsTrigger value="yaml" className="flex-none px-0">
          YAML
        </TabsTrigger>
      </TabsList>
      <TabsContent value="details" className="flex min-h-0 flex-col gap-4 overflow-auto pt-3">
        {children}
      </TabsContent>
      <TabsContent value="yaml" className="flex min-h-0 flex-col pt-3">
        <YamlView {...props} />
      </TabsContent>
    </Tabs>
  );
}

export function YamlView({ cluster, kind, namespace, name }: YamlProps) {
  const secret = kind === "secret";
  // The whole document is revealed at once: a YAML with one value shown and the rest masked is not the object.
  const [reveal, setReveal] = useState(false);
  const q = useQuery(yamlQuery(cluster.id, objectKind[kind], namespace, name, reveal));
  return (
    <div className="min-h-0 flex-1 overflow-auto rounded-md bg-muted/50 px-3 py-2">
      {q.data && (
        <div className="sticky top-0 float-right flex gap-0.5 rounded-md bg-muted">
          {secret && (
            <Button variant="ghost" size="icon-xs" title={reveal ? "Hide values" : "Reveal values"} onClick={() => setReveal(!reveal)}>
              {reveal ? <EyeSlashIcon /> : <EyeIcon />}
            </Button>
          )}
          <CopyButton text={q.data} title="Copy YAML" className="opacity-100" />
        </div>
      )}
      {q.error ? (
        <p className="text-xs text-destructive">{String(q.error)}</p>
      ) : q.data ? (
        <pre className="font-mono text-xs leading-5">{highlight(q.data)}</pre>
      ) : (
        <p className="text-xs text-muted-foreground">Loading…</p>
      )}
    </div>
  );
}

const keyLine = /^(\s*(?:- )?)([^\s#-][^:]*?|-)(:)(?=\s|$)(.*)$/;
const blockStart = /^[|>][-+\d]*$/;

// highlight colours keys and punctuation; the lines of a block scalar are left as one value, colons and all.
function highlight(yaml: string) {
  const out: ReactNode[] = [];
  let blockIndent = -1;
  yaml.trimEnd().split("\n").forEach((line, i) => {
    const indent = line.search(/\S|$/);
    if (blockIndent >= 0 && (indent > blockIndent || line.trim() === "")) {
      out.push(line, "\n");
      return;
    }
    blockIndent = -1;
    const m = keyLine.exec(line);
    if (!m) {
      const item = /^(\s*- )(.*)$/.exec(line);
      out.push(item ? <span key={i}><span className="text-muted-foreground">{item[1]}</span>{item[2]}</span> : line, "\n");
      return;
    }
    const [, lead, key, colon, rest] = m;
    const value = rest.trim();
    if (blockStart.test(value)) blockIndent = lead.length;
    out.push(
      <span key={i}>
        <span className="text-muted-foreground">{lead}</span>
        <span className="text-primary">{key}</span>
        <span className="text-muted-foreground">{colon}</span>
        {blockIndent >= 0 ? <span className="text-muted-foreground">{rest}</span> : rest}
      </span>,
      "\n",
    );
  });
  return out;
}

// YamlDialog is the detail of an object that has nothing to explain beyond its manifest.
export function YamlDialog({ cluster, target, onClose }: { cluster: Cluster; target: Target; onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="flex max-h-[calc(100vh-4rem)] flex-col sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="truncate pr-8">
            {target.namespace}/{target.name}
          </DialogTitle>
          <DialogDescription>{objectKind[target.kind]}</DialogDescription>
        </DialogHeader>
        <YamlView cluster={cluster} kind={target.kind} namespace={target.namespace} name={target.name} />
      </DialogContent>
    </Dialog>
  );
}
