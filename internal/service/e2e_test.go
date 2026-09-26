//go:build e2e

package service_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// e2eKubeconfig is the real cluster the E2E tests run against: the current context at $KUBECONFIG or ~/.kube/config
// (kind in CI).
func e2eKubeconfig() string {
	if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		return kubeconfig
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kube", "config")
}

// e2eCluster imports the current context into svc and returns its Cluster ID beside a clientset of its own.
func e2eCluster(t *testing.T, svc *service.Service) (string, kubernetes.Interface) {
	t.Helper()
	kubeconfig := e2eKubeconfig()
	kc, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	clusters, err := svc.ImportKubeconfigs([]string{kubeconfig})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(clusters, func(c service.Cluster) bool { return c.Context == kc.CurrentContext })
	if i < 0 {
		t.Fatalf("current context %q not imported from %v", kc.CurrentContext, clusters)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return clusters[i].ID, cs
}

func TestE2E_RealClusterReachabilityAndListing(t *testing.T) {
	svc, _ := newService(t)
	forwards := make(chan service.ForwardStatus, 100)
	logs := make(chan service.LogBatch, 100)
	states := make(chan service.LogStatus, 100)
	svc.Emit = func(_ string, data any) {
		switch data := data.(type) {
		case service.ForwardStatus:
			forwards <- data
		case service.LogBatch:
			logs <- data
		case service.LogStatus:
			states <- data
		}
	}
	id, cs := e2eCluster(t, svc)
	ctx := context.Background()

	version, err := svc.CheckReachability(ctx, id)
	if err != nil || version == "" {
		t.Fatalf("reachability: version=%q err=%v", version, err)
	}
	namespaces, err := svc.ListNamespaces(ctx, id)
	if err != nil || !slices.Contains(namespaces, "kube-system") {
		t.Fatalf("namespaces=%v err=%v", namespaces, err)
	}
	services, err := svc.ListServices(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	found := slices.ContainsFunc(services, func(s service.KubeService) bool {
		return s.Namespace == "default" && s.Name == "kubernetes"
	})
	if !found {
		t.Fatalf("default/kubernetes not in %v", services)
	}

	// CoreDNS serves Prometheus metrics on the kube-dns service's metrics port; the forward gets an automatic
	// local port and dials the pod on the first request.
	pf, err := svc.SaveForward(service.PortForward{
		ClusterID:  id,
		Target:     service.ForwardTarget{Kind: service.TargetService, Namespace: "kube-system", Name: "kube-dns"},
		RemotePort: 9153,
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.DeleteForward(pf.ID) }()
	if pf.LocalPort < service.ForwardPortStart {
		t.Fatalf("assigned local port = %d, want one from %d", pf.LocalPort, service.ForwardPortStart)
	}
	waitForwardState(t, forwards, service.StateIdle)
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", pf.LocalPort))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "coredns_") {
		t.Fatalf("metrics through local port: status %d, body %.200q", resp.StatusCode, body)
	}
	waitForwardState(t, forwards, service.StateConnected)

	// CoreDNS logs its Corefile on startup, so a fresh follow always has lines to deliver.
	pods, err := svc.ListPods(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	c := slices.IndexFunc(pods, func(p service.KubePod) bool {
		return p.Namespace == "kube-system" && strings.HasPrefix(p.Name, "coredns-")
	})
	if c < 0 {
		t.Fatalf("no coredns pod in %v", pods)
	}
	coredns := pods[c]
	stream, err := svc.StartLogs(ctx, service.LogSource{ClusterID: id, Namespace: coredns.Namespace, Kind: service.LogSourcePod, Name: coredns.Name})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.StopLogs(stream.ID) }()
	lines := collectLogs(t, logs, stream.ID, 1)
	if lines[0].Pod != coredns.Name || lines[0].Container != "coredns" || lines[0].Time.IsZero() || lines[0].Text == "" {
		t.Fatalf("first log line = %+v, want a timestamped coredns line", lines[0])
	}
	_ = svc.StopLogs(stream.ID)

	// Following the coredns Deployment through a rollout restart: the new pod joins, the old one leaves.
	workloads, err := svc.ListWorkloads(ctx, id)
	if err != nil || !slices.ContainsFunc(workloads, func(w service.KubeWorkload) bool {
		return w.Namespace == "kube-system" && w.Name == "coredns" && w.Kind == service.WorkloadDeployment
	}) {
		t.Fatalf("workloads=%v err=%v", workloads, err)
	}
	deploy, err := svc.StartLogs(ctx, service.LogSource{ClusterID: id, Namespace: "kube-system", Kind: service.LogSourceDeployment, Name: "coredns"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.StopLogs(deploy.ID) }()
	// Every coredns pod must have joined first: a line may come from any of them, whichever joined last.
	var corednsPods []string
	for _, p := range pods {
		if p.Namespace == "kube-system" && strings.HasPrefix(p.Name, "coredns-") {
			corednsPods = append(corednsPods, p.Name)
		}
	}
	before := waitLogStatus(t, states, func(st service.LogStatus) bool {
		return !slices.ContainsFunc(corednsPods, func(p string) bool { return !slices.Contains(st.Pods, p) })
	})
	if l := collectLogs(t, logs, deploy.ID, 1); !slices.Contains(before.Pods, l[0].Pod) {
		t.Fatalf("line from %q, want one of %v", l[0].Pod, before.Pods)
	}
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubereach.test/restartedAt":%q}}}}}`, time.Now().Format(time.RFC3339))
	if _, err := cs.AppsV1().Deployments("kube-system").Patch(ctx, "coredns", types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Minute)
	var after service.LogStatus
	for rolled := false; !rolled; {
		select {
		case after = <-states:
			replaced := len(after.Pods) > 0 && !slices.ContainsFunc(after.Pods, func(p string) bool { return slices.Contains(before.Pods, p) })
			rolled = after.ID == deploy.ID && replaced && after.State == service.StateConnected
		case <-deadline:
			t.Fatal("timed out waiting for the rollout to replace every pod")
		}
	}
	for {
		l := collectLogs(t, logs, deploy.ID, 1)
		if slices.Contains(after.Pods, l[0].Pod) {
			break
		}
	}
}

// An image changed through the editor rolls the Deployment out; an edit of a version that changed meanwhile is refused.
func TestE2E_EditYAMLRollsOutAndRefusesStaleEdits(t *testing.T) {
	svc, _ := newService(t)
	id, cs := e2eCluster(t, svc)
	ctx := context.Background()
	ns := fmt.Sprintf("kubereach-e2e-edit-%d", time.Now().UnixNano())
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{}) })
	labels := map[string]string{"app": "pause"}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "pause"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "pause", Image: "registry.k8s.io/pause:3.9"}}},
			},
		},
	}
	if _, err := cs.AppsV1().Deployments(ns).Create(ctx, d, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// The controller annotates a new Deployment with its revision; an editor opened before that would meet a conflict.
	waitRolledOut(t, cs, ns, "pause", "registry.k8s.io/pause:3.9")

	text, err := svc.EditYAML(ctx, id, service.ObjectDeployment, ns, "pause")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(text, "pause:3.9", "pause:3.10", 1)
	diff, err := svc.DiffYAML(ctx, id, service.ObjectDeployment, ns, "pause", text, edited)
	if err != nil || !strings.Contains(diff, "-      - image: registry.k8s.io/pause:3.9\n+      - image: registry.k8s.io/pause:3.10\n") {
		t.Fatalf("diff = %q, err = %v", diff, err)
	}
	if err := svc.ApplyYAML(ctx, id, service.ObjectDeployment, ns, "pause", text, edited); err != nil {
		t.Fatal(err)
	}
	waitRolledOut(t, cs, ns, "pause", "registry.k8s.io/pause:3.10")

	// The editor loaded the image from before the rollout.
	if err := svc.ApplyYAML(ctx, id, service.ObjectDeployment, ns, "pause", text, edited); service.Describe(err).Code != "conflict" {
		t.Fatalf("stale apply: err = %v, want a conflict", err)
	}
}

// waitRolledOut waits until the Deployment runs its one replica with image.
func waitRolledOut(t *testing.T, cs kubernetes.Interface, ns, name, image string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		got, err := cs.AppsV1().Deployments(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		s := got.Status
		if got.Spec.Template.Spec.Containers[0].Image == image && s.ObservedGeneration == got.Generation &&
			s.UpdatedReplicas == 1 && s.AvailableReplicas == 1 && s.Replicas == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("rollout to %s did not finish: %+v", image, s)
		}
		time.Sleep(time.Second)
	}
}
