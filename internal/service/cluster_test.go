package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const twoContextKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: staging
  cluster: {server: https://staging.example:6443}
- name: prod
  cluster: {server: https://prod.example:6443}
users:
- name: me
  user: {token: t}
contexts:
- name: staging-admin
  context: {cluster: staging, user: me}
- name: prod-admin
  context: {cluster: prod, user: me}
current-context: staging-admin
`

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(twoContextKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newFakeService(t *testing.T, objects ...runtime.Object) (*service.Service, *fake.Clientset, string) {
	t.Helper()
	cs := fake.NewClientset(objects...)
	svc := service.New(filepath.Join(t.TempDir(), "kubereach.yaml"), func(service.Cluster) (kubernetes.Interface, error) {
		return cs, nil
	})
	clusters, err := svc.ImportKubeconfigs([]string{writeKubeconfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	return svc, cs, clusters[0].ID
}

func namespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func k8sService(ns, name string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: port}}},
	}
}

// forbid rejects verb on resource; when clusterWideOnly is set, namespaced calls still succeed.
func forbid(cs *fake.Clientset, verb, resource string, clusterWideOnly bool) {
	cs.PrependReactor(verb, resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
		if clusterWideOnly && a.GetNamespace() != "" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("rbac"))
	})
}

func TestImportKubeconfigs_EachContextBecomesCluster(t *testing.T) {
	svc, _ := newService(t)
	path := writeKubeconfig(t)

	got, err := svc.ImportKubeconfigs([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	want := []service.Cluster{
		{Name: "prod-admin", Kubeconfig: path, Context: "prod-admin"},
		{Name: "staging-admin", Kubeconfig: path, Context: "staging-admin"},
	}
	ignoreID := cmpopts.IgnoreFields(service.Cluster{}, "ID")
	if diff := cmp.Diff(want, got, ignoreID); diff != "" {
		t.Errorf("clusters mismatch (-want +got):\n%s", diff)
	}
	if got[0].ID == "" || got[0].ID == got[1].ID {
		t.Errorf("cluster IDs must be unique and non-empty, got %q and %q", got[0].ID, got[1].ID)
	}

	cfg, err := svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, cfg.Clusters, ignoreID); diff != "" {
		t.Errorf("persisted clusters mismatch (-want +got):\n%s", diff)
	}
}

func TestImportKubeconfigs_SkipsAlreadyImportedContexts(t *testing.T) {
	svc, _ := newService(t)
	path := writeKubeconfig(t)
	if _, err := svc.ImportKubeconfigs([]string{path}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ImportKubeconfigs([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("second import added %d clusters, want 0", len(got))
	}
	cfg, err := svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clusters) != 2 {
		t.Errorf("config has %d clusters, want 2", len(cfg.Clusters))
	}
}

func TestImportKubeconfigs_MissingFileSavesNothing(t *testing.T) {
	svc, _ := newService(t)
	paths := []string{writeKubeconfig(t), filepath.Join(t.TempDir(), "nope")}
	if _, err := svc.ImportKubeconfigs(paths); err == nil {
		t.Fatal("expected error for missing kubeconfig")
	}
	cfg, err := svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clusters) != 0 {
		t.Errorf("config has %d clusters after failed import, want 0", len(cfg.Clusters))
	}
}

func TestCheckReachability(t *testing.T) {
	svc, cs, id := newFakeService(t)

	if _, err := svc.CheckReachability(context.Background(), id); err != nil {
		t.Fatalf("reachable cluster reported error: %v", err)
	}

	cs.PrependReactor("get", "version", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	if _, err := svc.CheckReachability(context.Background(), id); err == nil {
		t.Fatal("unreachable cluster reported no error")
	}
}

func TestCheckReachability_UnknownCluster(t *testing.T) {
	svc, _, _ := newFakeService(t)
	if _, err := svc.CheckReachability(context.Background(), "missing"); err == nil {
		t.Fatal("expected error for unknown cluster")
	}
}

func TestListing_AllNamespacesByDefault(t *testing.T) {
	svc, _, id := newFakeService(t,
		namespace("default"), namespace("payments"),
		k8sService("default", "api", 80), k8sService("payments", "db", 5432),
	)
	ctx := context.Background()

	namespaces, err := svc.ListNamespaces(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"default", "payments"}, namespaces); diff != "" {
		t.Errorf("namespaces mismatch (-want +got):\n%s", diff)
	}

	services, err := svc.ListServices(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []service.KubeService{
		{Namespace: "default", Name: "api", Ports: []service.ServicePort{{Name: "http", Port: 80}}},
		{Namespace: "payments", Name: "db", Ports: []service.ServicePort{{Name: "http", Port: 5432}}},
	}
	if diff := cmp.Diff(want, services); diff != "" {
		t.Errorf("services mismatch (-want +got):\n%s", diff)
	}
}

func TestListing_ForbiddenThenExplicitNamespaces(t *testing.T) {
	svc, cs, id := newFakeService(t,
		namespace("default"), namespace("payments"),
		k8sService("default", "api", 80), k8sService("payments", "db", 5432),
	)
	ctx := context.Background()
	forbid(cs, "list", "namespaces", false)
	forbid(cs, "list", "services", true)

	if _, err := svc.ListNamespaces(ctx, id); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("ListNamespaces: got %v, want ErrForbidden", err)
	}
	if _, err := svc.ListServices(ctx, id); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("ListServices: got %v, want ErrForbidden", err)
	}

	if err := svc.SetNamespaces(id, []string{"payments"}); err != nil {
		t.Fatal(err)
	}

	namespaces, err := svc.ListNamespaces(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"payments"}, namespaces); diff != "" {
		t.Errorf("namespaces mismatch (-want +got):\n%s", diff)
	}
	services, err := svc.ListServices(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []service.KubeService{
		{Namespace: "payments", Name: "db", Ports: []service.ServicePort{{Name: "http", Port: 5432}}},
	}
	if diff := cmp.Diff(want, services); diff != "" {
		t.Errorf("services mismatch (-want +got):\n%s", diff)
	}

	cfg, err := svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"payments"}, cfg.Clusters[0].Namespaces); diff != "" {
		t.Errorf("persisted namespaces mismatch (-want +got):\n%s", diff)
	}
}

func TestSetNamespaces_UnknownCluster(t *testing.T) {
	svc, _, _ := newFakeService(t)
	if err := svc.SetNamespaces("missing", []string{"a"}); err == nil {
		t.Fatal("expected error for unknown cluster")
	}
}
