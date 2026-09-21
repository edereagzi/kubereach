package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

const podMetricsJSON = `{"kind":"PodMetricsList","apiVersion":"metrics.k8s.io/v1beta1","items":[
  {"metadata":{"namespace":"default","name":"web-1"},"containers":[
    {"name":"app","usage":{"cpu":"120m","memory":"300Mi"}},
    {"name":"sidecar","usage":{"cpu":"5m","memory":"20Mi"}}]}]}`

const nodeMetricsJSON = `{"kind":"NodeMetricsList","apiVersion":"metrics.k8s.io/v1beta1","items":[
  {"metadata":{"name":"node-a"},"usage":{"cpu":"1500m","memory":"4Gi"}}]}`

// newMetricsService lists with the fake clientset and reads metrics.k8s.io from an in-test API server; a nil handler has no metrics API group.
func newMetricsService(t *testing.T, handler http.HandlerFunc) (*service.Service, string) {
	t.Helper()
	mux := http.NewServeMux()
	if handler != nil {
		mux.HandleFunc("/apis/metrics.k8s.io/v1beta1/", handler)
	}
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)
	cs := fake.NewClientset()
	svc := service.New(filepath.Join(t.TempDir(), "kubereach.yaml"), func(service.Cluster, service.DialFunc) (kubernetes.Interface, *rest.Config, error) {
		return cs, &rest.Config{Host: api.URL, Timeout: fixtureTimeout}, nil
	})
	clusters, err := svc.ImportKubeconfigs([]string{writeKubeconfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	return svc, clusters[0].ID
}

func TestPodMetrics_SumsContainers(t *testing.T) {
	svc, cluster := newMetricsService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/metrics.k8s.io/v1beta1/pods" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(podMetricsJSON))
	})

	got, err := svc.PodMetrics(context.Background(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	want := service.PodMetrics{Available: true, Pods: []service.PodUsage{{
		Namespace:  "default",
		Name:       "web-1",
		Usage:      service.ResourceUsage{CPU: 125, Memory: 320 << 20},
		Containers: map[string]service.ResourceUsage{"app": {CPU: 120, Memory: 300 << 20}, "sidecar": {CPU: 5, Memory: 20 << 20}},
	}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("metrics mismatch (-want +got):\n%s", diff)
	}
}

func TestPodMetrics_ScopedNamespaces(t *testing.T) {
	var paths []string
	svc, cluster := newMetricsService(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"PodMetricsList","apiVersion":"metrics.k8s.io/v1beta1","items":[]}`))
	})
	if err := svc.SetNamespaces(cluster, []string{"payments"}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.PodMetrics(context.Background(), cluster); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"/apis/metrics.k8s.io/v1beta1/namespaces/payments/pods"}, paths); diff != "" {
		t.Errorf("paths mismatch (-want +got):\n%s", diff)
	}
}

func TestNodeMetrics(t *testing.T) {
	svc, cluster := newMetricsService(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(nodeMetricsJSON))
	})

	got, err := svc.NodeMetrics(context.Background(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	want := service.NodeMetrics{Available: true, Nodes: []service.NodeUsage{{Name: "node-a", Usage: service.ResourceUsage{CPU: 1500, Memory: 4 << 30}}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("metrics mismatch (-want +got):\n%s", diff)
	}
}

func TestMetrics_MissingAPIGroupIsUnavailable(t *testing.T) {
	svc, cluster := newMetricsService(t, nil)

	pods, err := svc.PodMetrics(context.Background(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(service.PodMetrics{}, pods); diff != "" {
		t.Errorf("pod metrics mismatch (-want +got):\n%s", diff)
	}
	nodes, err := svc.NodeMetrics(context.Background(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(service.NodeMetrics{}, nodes); diff != "" {
		t.Errorf("node metrics mismatch (-want +got):\n%s", diff)
	}
}

func TestListPods_RequestsAndLimits(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	limited := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
	}
	svc, _, cluster := newFakeService(t,
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "both"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Resources: limited}, {Name: "b", Resources: limited}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "open"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Resources: limited}, {Name: "b"}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "sidecar"}, Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "a", Resources: limited}},
			InitContainers: []corev1.Container{{Name: "setup", Resources: limited}, {Name: "proxy", Resources: limited, RestartPolicy: &always}},
		}},
	)

	pods, err := svc.ListPods(context.Background(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]service.ResourceUsage{}
	for _, p := range pods {
		got[p.Name] = [2]service.ResourceUsage{p.Requests, p.Limits}
	}
	want := map[string][2]service.ResourceUsage{
		"both":    {{CPU: 200, Memory: 256 << 20}, {CPU: 1000, Memory: 1 << 30}},
		"open":    {{CPU: 100, Memory: 128 << 20}, {}},
		"sidecar": {{CPU: 200, Memory: 256 << 20}, {CPU: 1000, Memory: 1 << 30}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("resources mismatch (-want +got):\n%s", diff)
	}
}
