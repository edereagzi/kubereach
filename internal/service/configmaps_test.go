package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestConfigMaps_ListedWithoutValuesDecodedOnGet(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app-config"},
		Data:       map[string]string{"LOG_LEVEL": "debug", "config.yaml": "a: 1\n"},
		BinaryData: map[string][]byte{"logo.png": {0x89, 'P', 'N', 'G'}},
	}
	svc, _, id := newFakeService(t, cm, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "empty"}})
	ctx := context.Background()

	got, err := svc.ListConfigMaps(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []service.KubeConfigObject{
		{Namespace: "apps", Name: "empty"},
		{Namespace: "default", Name: "app-config", Keys: []string{"LOG_LEVEL", "config.yaml", "logo.png"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("configmaps (-want +got):\n%s", diff)
	}

	one, err := svc.GetConfigMap(ctx, id, "default", "app-config")
	if err != nil {
		t.Fatal(err)
	}
	want[1].Data = map[string]string{"LOG_LEVEL": "debug", "config.yaml": "a: 1\n", "logo.png": "(binary, 4 bytes)"}
	if diff := cmp.Diff(want[1], one); diff != "" {
		t.Errorf("configmap (-want +got):\n%s", diff)
	}
}

func TestSecrets_ListedByNameOnlyReadOnGet(t *testing.T) {
	const secret = `{"kind":"Secret","apiVersion":"v1","metadata":{"namespace":"default","name":"db","resourceVersion":"1"},"type":"Opaque","data":{"password":"aHVudGVyMg==","cert":"//4="}}`
	var fullList atomic.Bool
	done := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/secrets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if holdWatch(w, r, done) {
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "as=PartialObjectMetadataList") {
			fullList.Store(true)
			_, _ = w.Write([]byte(`{"kind":"SecretList","apiVersion":"v1","metadata":{"resourceVersion":"1"},"items":[` + secret + `]}`))
			return
		}
		_, _ = w.Write([]byte(`{"kind":"PartialObjectMetadataList","apiVersion":"meta.k8s.io/v1","metadata":{"resourceVersion":"1"},"items":[{"metadata":{"namespace":"default","name":"db","resourceVersion":"1"}}]}`))
	})
	mux.HandleFunc("/api/v1/namespaces/default/secrets/db", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(secret))
	})
	svc, id := kubeconfigService(t, httptest.NewServer(mux))
	t.Cleanup(func() { close(done) })
	ctx := context.Background()

	secrets, err := svc.ListSecrets(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]service.KubeConfigObject{{Namespace: "default", Name: "db"}}, secrets); diff != "" {
		t.Errorf("secrets (-want +got):\n%s", diff)
	}
	if fullList.Load() {
		t.Error("secrets were listed with their values")
	}

	s, err := svc.GetSecret(ctx, id, "default", "db")
	if err != nil {
		t.Fatal(err)
	}
	want := service.KubeConfigObject{Namespace: "default", Name: "db", Type: "Opaque", Keys: []string{"cert", "password"}, Data: map[string]string{"password": "hunter2", "cert": "(binary, 2 bytes)"}}
	if diff := cmp.Diff(want, s); diff != "" {
		t.Errorf("secret (-want +got):\n%s", diff)
	}
	if _, err := svc.GetSecret(ctx, id, "default", "missing"); err == nil {
		t.Error("missing secret read without error")
	}
}

func TestSecrets_ForbiddenLeavesConfigMaps(t *testing.T) {
	done := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/secrets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`))
	})
	mux.HandleFunc("/api/v1/configmaps", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if holdWatch(w, r, done) {
			return
		}
		_, _ = w.Write([]byte(`{"kind":"ConfigMapList","apiVersion":"v1","metadata":{"resourceVersion":"1"},"items":[]}`))
	})
	svc, id := kubeconfigService(t, httptest.NewServer(mux))
	t.Cleanup(func() { close(done) })
	ctx := context.Background()

	if _, err := svc.ListSecrets(ctx, id); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden secrets err = %v; want ErrForbidden", err)
	}
	if _, err := svc.ListConfigMaps(ctx, id); err != nil {
		t.Errorf("configmaps after forbidding secrets: %v", err)
	}
}

// holdWatch keeps a watch request open without events until release closes, which must happen before the server's Close,
// as nothing stops a Cluster's watches when a test ends.
func holdWatch(w http.ResponseWriter, r *http.Request, release <-chan struct{}) bool {
	if r.URL.Query().Get("watch") != "true" {
		return false
	}
	w.(http.Flusher).Flush()
	<-release
	return true
}
