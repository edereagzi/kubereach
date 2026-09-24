package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	"k8s.io/client-go/rest"
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
	svc := service.New(filepath.Join(t.TempDir(), "kubereach.yaml"), func(service.Cluster, service.DialFunc) (kubernetes.Interface, *rest.Config, error) {
		return cs, nil, nil
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
		{Namespace: "default", Name: "api", Ports: []service.NamedPort{{Name: "http", Port: 80}}},
		{Namespace: "payments", Name: "db", Ports: []service.NamedPort{{Name: "http", Port: 5432}}},
	}
	if diff := cmp.Diff(want, services); diff != "" {
		t.Errorf("services mismatch (-want +got):\n%s", diff)
	}
}

func TestListNamespaces_ListsBeyondTheScope(t *testing.T) {
	svc, _, id := newFakeService(t, namespace("default"), namespace("payments"))
	if err := svc.SetNamespaces(id, []string{"payments"}); err != nil {
		t.Fatal(err)
	}
	namespaces, err := svc.ListNamespaces(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"default", "payments"}, namespaces); diff != "" {
		t.Errorf("namespaces mismatch (-want +got):\n%s", diff)
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

	if _, err := svc.ListNamespaces(ctx, id); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("ListNamespaces with a scope: got %v, want ErrForbidden", err)
	}
	services, err := svc.ListServices(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []service.KubeService{
		{Namespace: "payments", Name: "db", Ports: []service.NamedPort{{Name: "http", Port: 5432}}},
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

func TestListing_ForbiddenNamespaceInScopeIsNamed(t *testing.T) {
	svc, cs, id := newFakeService(t, namespace("payments"), namespace("locked"))
	cs.PrependReactor("list", "services", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() != "locked" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", errors.New("rbac"))
	})
	if err := svc.SetNamespaces(id, []string{"payments", "locked"}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.ListServices(context.Background(), id)
	want := service.ErrorInfo{Code: "forbidden", Message: "Your role may not read namespace locked"}
	if diff := cmp.Diff(want, service.Describe(err)); diff != "" {
		t.Errorf("error mismatch (-want +got):\n%s", diff)
	}
}

func TestSetNamespaces_UnknownCluster(t *testing.T) {
	svc, _, _ := newFakeService(t)
	if err := svc.SetNamespaces("missing", []string{"a"}); err == nil {
		t.Fatal("expected error for unknown cluster")
	}
}

func TestDeleteCluster_DropsItsForwards(t *testing.T) {
	svc, _ := newService(t)
	clusters, err := svc.ImportKubeconfigs([]string{writeKubeconfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	target := service.ForwardTarget{Kind: service.TargetPod, Namespace: "default", Name: "p"}
	cfg.Forwards = []service.PortForward{
		{ID: "f1", ClusterID: clusters[0].ID, Target: target, RemotePort: 80, LocalPort: 20000},
		{ID: "f2", ClusterID: clusters[1].ID, Target: target, RemotePort: 80, LocalPort: 20001},
	}
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteCluster(clusters[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err := svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Clusters) != 1 || got.Clusters[0].ID != clusters[1].ID {
		t.Errorf("clusters = %+v, want only %s", got.Clusters, clusters[1].ID)
	}
	if len(got.Forwards) != 1 || got.Forwards[0].ID != "f2" {
		t.Errorf("forwards = %+v, want only f2", got.Forwards)
	}
	if err := svc.DeleteCluster(clusters[0].ID); err == nil {
		t.Fatal("deleting an unknown cluster succeeded")
	}
}

func TestClusterClient_BuiltOnceUntilTheClusterOrItsKubeconfigChanges(t *testing.T) {
	builds := 0
	cs := fake.NewClientset()
	svc := service.New(filepath.Join(t.TempDir(), "kubereach.yaml"), func(service.Cluster, service.DialFunc) (kubernetes.Interface, *rest.Config, error) {
		builds++
		return cs, nil, nil
	})
	kubeconfig := writeKubeconfig(t)
	clusters, err := svc.ImportKubeconfigs([]string{kubeconfig})
	if err != nil {
		t.Fatal(err)
	}
	id := clusters[0].ID
	reach := func(want int) {
		t.Helper()
		if _, err := svc.CheckReachability(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if builds != want {
			t.Fatalf("builds = %d, want %d", builds, want)
		}
	}

	reach(1)
	reach(1)
	if err := svc.SetNamespaces(id, []string{"team-a"}); err != nil {
		t.Fatal(err)
	}
	reach(2)
	if err := os.WriteFile(kubeconfig, []byte(twoContextKubeconfig+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reach(3)
	reach(3)
}

// kubeconfigService uses the real client factory against srv, with a header timeout short enough to outlive in a test.
func kubeconfigService(t *testing.T, srv *httptest.Server) (*service.Service, string) {
	t.Helper()
	t.Cleanup(srv.Close)
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: `+srv.URL+`}}]
users: [{name: u, user: {token: t}}]
contexts: [{name: ctx, context: {cluster: c, user: u}}]
current-context: ctx
`), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := service.New(filepath.Join(t.TempDir(), "kubereach.yaml"), nil)
	svc.ResponseHeaderTimeout = 100 * time.Millisecond
	clusters, err := svc.ImportKubeconfigs([]string{kubeconfig})
	if err != nil {
		t.Fatal(err)
	}
	return svc, clusters[0].ID
}

func TestKubeconfigClient_SlowBodyFlowsButLateHeadersFail(t *testing.T) {
	const podList = `{"kind":"PodList","apiVersion":"v1","metadata":{},"items":[{"metadata":{"namespace":"ns","name":"web"}}]}`
	var lateHeaders atomic.Bool
	var accept atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept.Store(r.Header.Get("Accept"))
		if lateHeaders.Load() {
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		for chunk := range slices.Chunk([]byte(podList), len(podList)/5+1) {
			_, _ = w.Write(chunk)
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond)
		}
	}))
	svc, id := kubeconfigService(t, srv)

	pods, err := svc.ListPods(context.Background(), id)
	if err != nil {
		t.Fatalf("a body streaming for longer than the header timeout was cut: %v", err)
	}
	if len(pods) != 1 || pods[0].Name != "web" {
		t.Fatalf("pods = %+v", pods)
	}
	if got := accept.Load(); got != "application/vnd.kubernetes.protobuf,application/json" {
		t.Fatalf("Accept = %q, want protobuf with JSON as fallback", got)
	}

	lateHeaders.Store(true)
	start := time.Now()
	if _, err := svc.ListNamespaces(context.Background(), id); err == nil {
		t.Fatal("headers arriving after the timeout did not fail the request")
	}
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Fatalf("late headers failed after %v, want about the timeout", waited)
	}
}

// The kubelet sends a follow's headers with its first line, so a quiet container must not trip the header timeout.
func TestKubeconfigClient_QuietLogFollowIsNotCut(t *testing.T) {
	svc, id := kubeconfigService(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/log") {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
			_, _ = w.Write([]byte("2026-01-01T00:00:00Z hello\n"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"Pod","apiVersion":"v1","metadata":{"namespace":"ns","name":"web"},"spec":{"containers":[{"name":"app"}]}}`))
	})))
	batches := make(chan service.LogBatch, 100)
	svc.Emit = func(_ string, data any) {
		if b, ok := data.(service.LogBatch); ok {
			batches <- b
		}
	}
	stream, err := svc.StartLogs(context.Background(), service.LogSource{ClusterID: id, Namespace: "ns", Kind: service.LogSourcePod, Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.StopLogs(stream.ID) }()
	if lines := collectLogs(t, batches, stream.ID, 1); lines[0].Text != "hello" {
		t.Fatalf("line = %+v", lines[0])
	}
}
