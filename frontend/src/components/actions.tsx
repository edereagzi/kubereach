import { useState, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ClusterService } from "@bindings/internal/bindings";
import { WorkloadKind, type Cluster, type KubeWorkload } from "@bindings/internal/service";
import type { Target } from "@/components/targets";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

type ActionProps = {
  cluster: Cluster;
  label: string;
  title: string;
  description: ReactNode;
  confirm: string;
  destructive?: boolean;
  disabled?: boolean;
  children?: ReactNode;
  run: () => Promise<void>;
  onDone?: () => void;
  onOpen?: () => void;
};

// WriteAction changes the Cluster only after a confirmation that names it.
function WriteAction({ cluster, label, title, description, confirm, destructive, disabled, children, run, onDone, onOpen }: ActionProps) {
  const [open, setOpen] = useState(false);
  const queryClient = useQueryClient();
  const action = useMutation({
    mutationFn: run,
    onSuccess: () => {
      setOpen(false);
      queryClient.invalidateQueries({ queryKey: ["cluster", cluster.id] });
      onDone?.();
    },
  });
  return (
    <>
      <Button
        variant="outline"
        size="xs"
        onClick={() => {
          action.reset();
          onOpen?.();
          setOpen(true);
        }}
      >
        {label}
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={title}
        description={description}
        confirm={confirm}
        destructive={destructive}
        disabled={disabled}
        action={action}
      >
        <p className="border-l-2 border-foreground/40 pl-3 text-sm text-muted-foreground">
          on <span className="text-base font-semibold text-foreground">{cluster.name}</span>
        </p>
        {children}
      </ConfirmDialog>
    </>
  );
}

const objectRef = (w: { namespace: string; name: string }) => (
  <span className="font-mono text-foreground">
    {w.namespace}/{w.name}
  </span>
);

export function WorkloadActions({ cluster, workload: w }: { cluster: Cluster; workload: KubeWorkload }) {
  const [replicas, setReplicas] = useState("");
  if (!w.rollout) return null;
  const current = w.rollout.desired;
  const next = replicas === "" ? NaN : Number(replicas);
  const validNext = Number.isInteger(next) && next >= 0;
  return (
    <div className="flex gap-1.5">
      <WriteAction
        cluster={cluster}
        label="Restart"
        title={`Restart ${w.kind} ${w.name}?`}
        description={<>Every pod of {objectRef(w)} is replaced, as its rollout strategy allows.</>}
        confirm="Restart"
        run={() => ClusterService.RestartWorkload(cluster.id, w.kind, w.namespace, w.name)}
      />
      {(w.kind === WorkloadKind.WorkloadDeployment || w.kind === WorkloadKind.WorkloadStatefulSet) && (
        <WriteAction
          cluster={cluster}
          label="Scale"
          title={`Scale ${w.kind} ${w.name}?`}
          description={<>Sets the replicas of {objectRef(w)}.</>}
          confirm={validNext ? `Scale to ${next}` : "Scale"}
          disabled={!validNext || next === current}
          onOpen={() => setReplicas(String(current))}
          run={() => ClusterService.ScaleWorkload(cluster.id, w.kind, w.namespace, w.name, next)}
        >
          <label className="flex items-center gap-3 text-sm">
            <span className="font-mono text-muted-foreground tabular-nums">{current} →</span>
            <Input
              type="number"
              min={0}
              step={1}
              autoFocus
              aria-label="New replicas"
              className="w-24 font-mono tabular-nums"
              value={replicas}
              onChange={(e) => setReplicas(e.target.value)}
            />
            <span className="text-muted-foreground">replicas</span>
          </label>
        </WriteAction>
      )}
      {/* The first revision has nothing before it to go back to. */}
      {w.kind === WorkloadKind.WorkloadDeployment && Number(w.rollout.revision) > 1 && (
        <WriteAction
          cluster={cluster}
          label="Roll back"
          title={`Roll back deployment ${w.name}?`}
          description={
            <>
              {objectRef(w)} goes back to the pod template of the revision before {w.rollout.revision || "the current one"}, and its pods roll.
            </>
          }
          confirm="Roll back"
          run={() => ClusterService.RollbackDeployment(cluster.id, w.namespace, w.name)}
        />
      )}
    </div>
  );
}

export function DeletePodAction({ cluster, target, onDone }: { cluster: Cluster; target: Target; onDone: () => void }) {
  return (
    <WriteAction
      cluster={cluster}
      label="Delete pod"
      title={`Delete pod ${target.name}?`}
      description={<>{objectRef(target)} stops with its grace period. Its controller, if it has one, creates a replacement.</>}
      confirm="Delete pod"
      destructive
      run={() => ClusterService.DeletePod(cluster.id, target.namespace, target.name)}
      onDone={onDone}
    />
  );
}
