import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vite-plus/test";
import { ClusterService } from "@bindings/internal/bindings";
import {
  ObjectKind,
  RolloutState,
  WorkloadKind,
  type Cluster,
  type KubeNode,
  type KubeWorkload,
} from "@bindings/internal/service";
import { DeleteAction, NodeActions, WorkloadActions } from "@/components/actions";
import { backend, callOf, failure } from "@/test/setup";

const cluster: Cluster = {
  id: "c1",
  name: "prod-eu",
  kubeconfig: "",
  remote: "",
  context: "",
  route: "",
  namespaces: null,
};

const workload = (kind: WorkloadKind, more: Partial<KubeWorkload> = {}): KubeWorkload => ({
  namespace: "shop",
  name: "web",
  kind,
  containers: null,
  created: "",
  ...more,
});

const rollout = {
  desired: 2,
  updated: 2,
  ready: 2,
  available: 2,
  revision: "3",
  state: RolloutState.$zero,
};
const deployment = workload(WorkloadKind.WorkloadDeployment, { rollout });

const node = (unschedulable: boolean): KubeNode => ({
  name: "node-1",
  version: "",
  unschedulable,
  allocatable: { cpu: 0, memory: 0 },
  requested: { cpu: 0, memory: 0 },
  pods: 0,
  podCapacity: 0,
});

const cronJob = (suspend: boolean) =>
  workload(WorkloadKind.WorkloadCronJob, {
    cronJob: { schedule: "@hourly", suspend, lastScheduled: "" },
  });

const podTarget = {
  value: "",
  label: "",
  kind: "pod" as const,
  namespace: "shop",
  name: "web-1",
  ports: [],
  containers: [],
};

function renderWithQueries(ui: ReactNode) {
  render(<QueryClientProvider client={new QueryClient()}>{ui}</QueryClientProvider>);
  return userEvent.setup();
}

const button = (name: string) => screen.getByRole("button", { name });
const dialog = () => screen.queryByRole("alertdialog");
const inDialog = () => within(screen.getByRole("alertdialog"));
const dialogButton = (name: string | RegExp) =>
  inDialog().getByRole<HTMLButtonElement>("button", { name });
// A backend call starts some ticks after the click, so a check that nothing was sent must wait for them.
const settle = () => new Promise((resolve) => setTimeout(resolve));

const restart = () =>
  callOf(() =>
    ClusterService.RestartWorkload("c1", WorkloadKind.WorkloadDeployment, "shop", "web"),
  );

async function openRestart() {
  const user = renderWithQueries(<WorkloadActions cluster={cluster} workload={deployment} />);
  await user.click(button("Restart"));
  await settle();
  return user;
}

describe("a write action", () => {
  test("asks first and names the Cluster", async () => {
    await openRestart();
    expect(inDialog().getByText("prod-eu")).not.toBeNull();
    expect(backend.calls).toEqual([]);
  });

  test("sends nothing on Cancel", async () => {
    const user = await openRestart();
    await user.click(dialogButton("Cancel"));
    await waitFor(() => expect(dialog()).toBeNull());
    await settle();
    expect(backend.calls).toEqual([]);
  });

  test("stays open with Cancel disabled while the call runs", async () => {
    backend.answer = () => new Promise(() => {});
    const user = await openRestart();
    await user.click(dialogButton("Restart"));
    await waitFor(() => expect(dialogButton("Cancel").disabled).toBe(true));
    await user.keyboard("{Escape}");
    await settle();
    expect(dialog()).not.toBeNull();
    expect(backend.calls).toEqual([restart()]);
  });

  test("stays open and shows the reason when the call fails", async () => {
    backend.answer = () => {
      throw failure("You cannot restart deployments in shop.", "forbidden");
    };
    const user = await openRestart();
    await user.click(dialogButton("Restart"));
    await inDialog().findByText("You cannot restart deployments in shop.");
    await settle();
    expect(dialog()).not.toBeNull();
    expect(backend.calls).toEqual([restart()]);
  });
});

test.each(["", "-1", "1.5", "2"])("Scale sends nothing for the value %j", async (value) => {
  const user = renderWithQueries(<WorkloadActions cluster={cluster} workload={deployment} />);
  await user.click(button("Scale"));
  const input = screen.getByRole("spinbutton", { name: "New replicas" });
  await user.clear(input);
  if (value) await user.type(input, value);
  const confirm = dialogButton(/^Scale/);
  expect(confirm.disabled).toBe(true);
  await user.click(confirm);
  await settle();
  expect(backend.calls).toEqual([]);
});

const workloadActions = (w: KubeWorkload) => <WorkloadActions cluster={cluster} workload={w} />;

// Each action: the button that opens it, what it shows, its confirm button, and the call that it must send.
const actions: [string, ReactNode, string, () => unknown][] = [
  [
    "Restart",
    workloadActions(deployment),
    "Restart",
    () => ClusterService.RestartWorkload("c1", WorkloadKind.WorkloadDeployment, "shop", "web"),
  ],
  [
    "Scale",
    workloadActions(workload(WorkloadKind.WorkloadStatefulSet, { rollout })),
    "Scale to 5",
    () => ClusterService.ScaleWorkload("c1", WorkloadKind.WorkloadStatefulSet, "shop", "web", 5),
  ],
  [
    "Roll back",
    workloadActions(deployment),
    "Roll back",
    () => ClusterService.RollbackDeployment("c1", "shop", "web"),
  ],
  [
    "Run now",
    workloadActions(cronJob(false)),
    "Run now",
    () => ClusterService.RunCronJob("c1", "shop", "web"),
  ],
  [
    "Suspend",
    workloadActions(cronJob(false)),
    "Suspend",
    () => ClusterService.SuspendCronJob("c1", "shop", "web", true),
  ],
  [
    "Resume",
    workloadActions(cronJob(true)),
    "Resume",
    () => ClusterService.SuspendCronJob("c1", "shop", "web", false),
  ],
  [
    "Cordon",
    <NodeActions cluster={cluster} node={node(false)} />,
    "Cordon",
    () => ClusterService.CordonNode("c1", "node-1", true),
  ],
  [
    "Uncordon",
    <NodeActions cluster={cluster} node={node(true)} />,
    "Uncordon",
    () => ClusterService.CordonNode("c1", "node-1", false),
  ],
  [
    "Delete",
    <DeleteAction cluster={cluster} target={podTarget} onDone={() => {}} />,
    "Delete pod",
    () => ClusterService.DeleteObject("c1", ObjectKind.ObjectPod, "shop", "web-1"),
  ],
];

test.each(actions)(
  "%s sends one call only after confirm, then closes",
  async (open, ui, confirm, send) => {
    const user = renderWithQueries(ui);
    await user.click(button(open));
    expect(inDialog().getByText("prod-eu")).not.toBeNull();
    const input = screen.queryByRole("spinbutton");
    if (input) {
      await user.clear(input);
      await user.type(input, "5");
    }
    await settle();
    expect(backend.calls).toEqual([]);
    await user.click(dialogButton(confirm));
    await waitFor(() => expect(dialog()).toBeNull());
    expect(backend.calls).toEqual([callOf(send)]);
  },
);
