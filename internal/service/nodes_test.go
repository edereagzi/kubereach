package service_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func node(name string, conditions ...corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"node-role.kubernetes.io/worker": ""}},
		Status: corev1.NodeStatus{
			NodeInfo:    corev1.NodeSystemInfo{KubeletVersion: "v1.30.2"},
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")},
			Conditions:  conditions,
		},
	}
}

func nodeCondition(t corev1.NodeConditionType, status corev1.ConditionStatus) corev1.NodeCondition {
	return corev1.NodeCondition{Type: t, Status: status, Reason: string(t)}
}

func podOn(nodeName, ns, name string, phase corev1.PodPhase, cpu, memory string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)},
			}}},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func TestListNodes_ReadyAndUnderPressure(t *testing.T) {
	svc, _, cluster := newFakeService(t,
		node("node-a", nodeCondition(corev1.NodeReady, corev1.ConditionTrue), nodeCondition(corev1.NodeMemoryPressure, corev1.ConditionFalse)),
		node("node-b", nodeCondition(corev1.NodeReady, corev1.ConditionTrue), nodeCondition(corev1.NodeMemoryPressure, corev1.ConditionTrue)),
		podOn("node-a", "default", "web", corev1.PodRunning, "500m", "1Gi"),
		podOn("node-a", "payments", "api", corev1.PodRunning, "250m", "512Mi"),
		// A finished pod has given the node back, as in kubectl describe node.
		podOn("node-a", "default", "backup", corev1.PodSucceeded, "1", "2Gi"),
		podOn("node-b", "default", "cache", corev1.PodRunning, "1", "4Gi"),
	)

	got, err := svc.ListNodes(context.Background(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	want := []service.KubeNode{
		{
			Name: "node-a", Roles: []string{"worker"}, Version: "v1.30.2",
			Allocatable: service.ResourceUsage{CPU: 4000, Memory: 8 << 30},
			Requested:   service.ResourceUsage{CPU: 750, Memory: 1536 << 20},
			Pods:        2,
		},
		{
			Name: "node-b", Roles: []string{"worker"}, Version: "v1.30.2", Problem: "MemoryPressure",
			Allocatable: service.ResourceUsage{CPU: 4000, Memory: 8 << 30},
			Requested:   service.ResourceUsage{CPU: 1000, Memory: 4 << 30},
			Pods:        1,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("nodes mismatch (-want +got):\n%s", diff)
	}
}

func TestListNodes_NotReadyOutranksPressure(t *testing.T) {
	svc, _, cluster := newFakeService(t,
		node("node-a", nodeCondition(corev1.NodeReady, corev1.ConditionUnknown), nodeCondition(corev1.NodeDiskPressure, corev1.ConditionTrue)),
	)

	got, err := svc.ListNodes(context.Background(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Problem != "NotReady" {
		t.Errorf("problem = %q, want NotReady", got[0].Problem)
	}
}

func TestListNodes_ForbiddenIsReported(t *testing.T) {
	svc, cs, cluster := newFakeService(t)
	forbid(cs, "list", "nodes", false)

	_, err := svc.ListNodes(context.Background(), cluster)
	if !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("got %v, want ErrForbidden", err)
	}
}

// It is the pods that were refused, not the nodes, so the nodes still list and only their totals go unknown.
func TestListNodes_ForbiddenPodsLeaveTotalsUnknown(t *testing.T) {
	svc, cs, cluster := newFakeService(t, node("node-a", nodeCondition(corev1.NodeReady, corev1.ConditionTrue)))
	forbid(cs, "list", "pods", false)

	got, err := svc.ListNodes(context.Background(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	want := []service.KubeNode{{
		Name: "node-a", Roles: []string{"worker"}, Version: "v1.30.2",
		Allocatable: service.ResourceUsage{CPU: 4000, Memory: 8 << 30},
		Unknown:     true,
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("nodes mismatch (-want +got):\n%s", diff)
	}
}

const nodePodMetricsJSON = `{"kind":"PodMetricsList","apiVersion":"metrics.k8s.io/v1beta1","items":[
  {"metadata":{"namespace":"default","name":"web"},"containers":[{"name":"app","usage":{"cpu":"100m","memory":"200Mi"}}]},
  {"metadata":{"namespace":"payments","name":"api"},"containers":[{"name":"app","usage":{"cpu":"50m","memory":"6Gi"}}]}]}`

func TestDescribeNode_PodsRankedByUsage(t *testing.T) {
	svc, cluster := newMetricsService(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(nodePodMetricsJSON))
	},
		node("node-a", nodeCondition(corev1.NodeReady, corev1.ConditionTrue), nodeCondition(corev1.NodeMemoryPressure, corev1.ConditionTrue)),
		podOn("node-a", "default", "web", corev1.PodRunning, "500m", "1Gi"),
		podOn("node-a", "payments", "api", corev1.PodRunning, "250m", "512Mi"),
		podOn("node-b", "default", "cache", corev1.PodRunning, "1", "4Gi"),
	)

	got, err := svc.DescribeNode(context.Background(), cluster, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	// api uses less CPU but three quarters of the node's memory, so it is what is eating the node.
	want := []service.NodePod{
		{Namespace: "payments", Name: "api", Usage: service.ResourceUsage{CPU: 50, Memory: 6 << 30}, Requests: service.ResourceUsage{CPU: 250, Memory: 512 << 20}},
		{Namespace: "default", Name: "web", Usage: service.ResourceUsage{CPU: 100, Memory: 200 << 20}, Requests: service.ResourceUsage{CPU: 500, Memory: 1 << 30}},
	}
	if diff := cmp.Diff(want, got.Pods); diff != "" {
		t.Errorf("pods mismatch (-want +got):\n%s", diff)
	}
	if !got.UsageAvailable {
		t.Error("UsageAvailable = false, want true")
	}
	if got.Node.Problem != "MemoryPressure" {
		t.Errorf("problem = %q, want MemoryPressure", got.Node.Problem)
	}
	wantConditions := []service.NodeCondition{
		{Type: "Ready", Status: "True", Reason: "Ready"},
		{Type: "MemoryPressure", Status: "True", Reason: "MemoryPressure"},
	}
	if diff := cmp.Diff(wantConditions, got.Conditions); diff != "" {
		t.Errorf("conditions mismatch (-want +got):\n%s", diff)
	}
}

func TestDescribeNode_WithoutMetricsStillListsPods(t *testing.T) {
	svc, cluster := newMetricsService(t, nil,
		node("node-a", nodeCondition(corev1.NodeReady, corev1.ConditionTrue)),
		podOn("node-a", "default", "web", corev1.PodRunning, "500m", "1Gi"),
	)

	got, err := svc.DescribeNode(context.Background(), cluster, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	want := []service.NodePod{{Namespace: "default", Name: "web", Requests: service.ResourceUsage{CPU: 500, Memory: 1 << 30}}}
	if diff := cmp.Diff(want, got.Pods); diff != "" {
		t.Errorf("pods mismatch (-want +got):\n%s", diff)
	}
	// Every Usage is zero because nothing measured it; the detail must not read that as an idle pod.
	if got.UsageAvailable {
		t.Error("UsageAvailable = true, want false")
	}
}

// A pressure outranks a vendor condition, whatever order the API returned them in.
func TestNodeReason_PressureOutranksUnknownCondition(t *testing.T) {
	svc, _, cluster := newFakeService(t, node("node-a",
		corev1.NodeCondition{Type: "KernelDeadlock", Status: corev1.ConditionTrue},
		nodeCondition(corev1.NodeReady, corev1.ConditionTrue),
		nodeCondition(corev1.NodeDiskPressure, corev1.ConditionTrue),
	))

	got, err := svc.ListNodes(context.Background(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Problem != "DiskPressure" {
		t.Errorf("problem = %q, want DiskPressure", got[0].Problem)
	}
}
