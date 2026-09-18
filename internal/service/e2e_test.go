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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Runs against the current context of the real cluster at $KUBECONFIG or ~/.kube/config (kind in CI).
func TestE2E_RealClusterReachabilityAndListing(t *testing.T) {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	kc, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
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
	clusters, err := svc.ImportKubeconfigs([]string{kubeconfig})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(clusters, func(c service.Cluster) bool { return c.Context == kc.CurrentContext })
	if i < 0 {
		t.Fatalf("current context %q not imported from %v", kc.CurrentContext, clusters)
	}
	ctx := context.Background()
	id := clusters[i].ID

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

	// CoreDNS serves Prometheus metrics on the kube-dns service's metrics port.
	local := freePort(t)
	pf, err := svc.StartForward(ctx, service.PortForward{
		ClusterID:  id,
		Target:     service.ForwardTarget{Kind: service.TargetService, Namespace: "kube-system", Name: "kube-dns"},
		RemotePort: 9153,
		LocalPort:  local,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.StopForward(pf.ID) }()
	waitForwardState(t, forwards, service.StateConnected)
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", local))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "coredns_") {
		t.Fatalf("metrics through local port: status %d, body %.200q", resp.StatusCode, body)
	}

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
	if err != nil || !slices.Contains(workloads, service.KubeWorkload{Namespace: "kube-system", Name: "coredns", Kind: service.LogSourceDeployment}) {
		t.Fatalf("workloads=%v err=%v", workloads, err)
	}
	deploy, err := svc.StartLogs(ctx, service.LogSource{ClusterID: id, Namespace: "kube-system", Kind: service.LogSourceDeployment, Name: "coredns"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.StopLogs(deploy.ID) }()
	before := waitLogStatus(t, states, func(st service.LogStatus) bool { return slices.Contains(st.Pods, coredns.Name) })
	if l := collectLogs(t, logs, deploy.ID, 1); !slices.Contains(before.Pods, l[0].Pod) {
		t.Fatalf("line from %q, want one of %v", l[0].Pod, before.Pods)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
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
