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

	"github.com/edereagzi/kubereach/internal/service"
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
	svc.Emit = func(_ string, data any) {
		if status, ok := data.(service.ForwardStatus); ok {
			forwards <- status
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
}
