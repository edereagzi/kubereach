package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	k8stesting "k8s.io/client-go/testing"
)

func clusterChanges(svc *service.Service) <-chan service.ClusterChange {
	changes := make(chan service.ClusterChange, 100)
	emit := svc.Emit
	svc.Emit = func(name string, data any) {
		if c, ok := data.(service.ClusterChange); ok {
			changes <- c
			return
		}
		emit(name, data)
	}
	return changes
}

func waitChange(t *testing.T, changes <-chan service.ClusterChange, want service.ClusterChange) {
	t.Helper()
	select {
	case got := <-changes:
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("change mismatch (-want +got):\n%s", diff)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %+v", want)
	}
}

func TestWatchCache_ChangesAfterTheFirstListShowAndAreAnnounced(t *testing.T) {
	ready := nodeCondition(corev1.NodeReady, corev1.ConditionTrue)
	svc, cs, cluster := newFakeService(t, podOn("node-a", "default", "web", corev1.PodRunning, "100m", "64Mi"), node("node-a", ready))
	changes := clusterChanges(svc)
	ctx := context.Background()

	if pods, err := svc.ListPods(ctx, cluster); err != nil || len(pods) != 1 {
		t.Fatalf("ListPods = %v, %v; want web", pods, err)
	}
	if _, err := svc.ListNodes(ctx, cluster); err != nil {
		t.Fatal(err)
	}

	pending := podOn("", "default", "worker", corev1.PodPending, "100m", "64Mi")
	if _, err := cs.CoreV1().Pods("default").Create(ctx, pending, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	web, err := cs.CoreV1().Pods("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	web.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 3, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}
	if _, err := cs.CoreV1().Pods("default").UpdateStatus(ctx, web, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitChange(t, changes, service.ClusterChange{ClusterID: cluster, Kinds: []string{"nodes", "pods"}})

	pods, err := svc.ListPods(ctx, cluster)
	if err != nil {
		t.Fatal(err)
	}
	var reasons []string
	for _, p := range pods {
		reasons = append(reasons, p.Name+":"+p.Reason)
	}
	if diff := cmp.Diff([]string{"web:CrashLoopBackOff", "worker:Pending"}, reasons); diff != "" {
		t.Errorf("pods mismatch (-want +got):\n%s", diff)
	}

	n, err := cs.CoreV1().Nodes().Get(ctx, "node-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n.Status.Conditions = append(n.Status.Conditions, nodeCondition(corev1.NodeDiskPressure, corev1.ConditionTrue))
	if _, err := cs.CoreV1().Nodes().UpdateStatus(ctx, n, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitChange(t, changes, service.ClusterChange{ClusterID: cluster, Kinds: []string{"nodes"}})
	nodes, err := svc.ListNodes(ctx, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if nodes[0].Problem != "DiskPressure" {
		t.Errorf("node problem = %q, want DiskPressure", nodes[0].Problem)
	}
}

func TestWatchCache_NodeTotalsOfANamespaceScopedClusterFollowEveryNamespace(t *testing.T) {
	svc, cs, cluster := newFakeService(t, node("node-a", nodeCondition(corev1.NodeReady, corev1.ConditionTrue)))
	if err := svc.SetNamespaces(cluster, []string{"default"}); err != nil {
		t.Fatal(err)
	}
	changes := clusterChanges(svc)
	ctx := context.Background()
	if _, err := svc.ListNodes(ctx, cluster); err != nil {
		t.Fatal(err)
	}

	if _, err := cs.CoreV1().Pods("payments").Create(ctx, podOn("node-a", "payments", "api", corev1.PodRunning, "250m", "512Mi"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitChange(t, changes, service.ClusterChange{ClusterID: cluster, Kinds: []string{"nodes"}})
	nodes, err := svc.ListNodes(ctx, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if nodes[0].Pods != 1 || nodes[0].Requested.CPU != 250 {
		t.Errorf("node totals = %d pods, %dm CPU; want the payments pod", nodes[0].Pods, nodes[0].Requested.CPU)
	}
}

func TestWatchCache_AFailingWatchAfterSyncIsReportedUntilItRecovers(t *testing.T) {
	svc, cs, cluster := newFakeService(t, podOn("node-a", "default", "web", corev1.PodRunning, "100m", "64Mi"))
	first := watch.NewFake()
	var calls atomic.Int32
	var failing atomic.Bool
	cs.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
		switch {
		case calls.Add(1) == 1:
			return true, first, nil
		case failing.Load():
			return true, nil, errors.New("connection reset")
		}
		return false, nil, nil
	})
	changes := clusterChanges(svc)
	ctx := context.Background()
	if _, err := svc.ListPods(ctx, cluster); err != nil {
		t.Fatal(err)
	}

	failing.Store(true)
	first.Stop()
	waitChange(t, changes, service.ClusterChange{ClusterID: cluster, Kinds: []string{"nodes", "pods"}})
	if _, err := svc.ListPods(ctx, cluster); err == nil {
		t.Fatal("a Cluster whose watch fails still served its last known pods")
	}

	failing.Store(false)
	waitChange(t, changes, service.ClusterChange{ClusterID: cluster, Kinds: []string{"nodes", "pods"}})
	if pods, err := svc.ListPods(ctx, cluster); err != nil || len(pods) != 1 {
		t.Fatalf("after recovery ListPods = %v, %v; want web", pods, err)
	}
}

func TestWatchCache_TheRestOfTheOverviewFollowsChanges(t *testing.T) {
	progressing := appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated"}
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
		Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{ingressPath("a.example", "/", "api", 80)}},
	}
	svc, cs, cluster := newFakeService(t,
		rolloutDeployment("api", "2", 2, 1, 2, 2, 3, progressing),
		ing, k8sService("default", "api", 80), endpointSlice("default", "api", map[string]bool{"api-0": true}),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app"}},
	)
	changes := clusterChanges(svc)
	ctx := context.Background()
	if _, err := svc.ListWorkloads(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.ListIngresses(ctx, cluster); err != nil || got[0].Problem != "" {
		t.Fatalf("ListIngresses = %+v, %v; want a healthy web", got, err)
	}
	if _, err := svc.ListServices(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListConfigMaps(ctx, cluster); err != nil {
		t.Fatal(err)
	}

	d, err := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d.Status.Conditions[0].Status, d.Status.Conditions[0].Reason = corev1.ConditionFalse, "ProgressDeadlineExceeded"
	if _, err := cs.AppsV1().Deployments("default").UpdateStatus(ctx, d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitChange(t, changes, service.ClusterChange{ClusterID: cluster, Kinds: []string{"workloads"}})
	if got, err := svc.ListWorkloads(ctx, cluster); err != nil || got[0].Rollout.State != service.RolloutStuck {
		t.Errorf("ListWorkloads = %+v, %v; want api stuck", got, err)
	}

	if _, err := cs.DiscoveryV1().EndpointSlices("default").Update(ctx, endpointSlice("default", "api", map[string]bool{"api-0": false}), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitChange(t, changes, service.ClusterChange{ClusterID: cluster, Kinds: []string{"ingresses"}})
	if got, err := svc.ListIngresses(ctx, cluster); err != nil || got[0].Problem != "no ready endpoints" {
		t.Errorf("ListIngresses = %+v, %v; want web without ready endpoints", got, err)
	}

	if _, err := cs.CoreV1().Services("default").Create(ctx, k8sService("default", "db", 5432), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitChange(t, changes, service.ClusterChange{ClusterID: cluster, Kinds: []string{"ingresses", "services"}})
	if got, err := svc.ListServices(ctx, cluster); err != nil || len(got) != 2 {
		t.Errorf("ListServices = %+v, %v; want api and db", got, err)
	}

	if err := cs.CoreV1().ConfigMaps("default").Delete(ctx, "app", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitChange(t, changes, service.ClusterChange{ClusterID: cluster, Kinds: []string{"configmaps"}})
	if got, err := svc.ListConfigMaps(ctx, cluster); err != nil || len(got) != 0 {
		t.Errorf("ListConfigMaps = %+v, %v; want none", got, err)
	}
}
