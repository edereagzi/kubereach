//go:build e2e

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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
}
