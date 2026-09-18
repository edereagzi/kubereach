package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
)

func newService(t *testing.T) (*service.Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubereach.yaml")
	return service.New(path), path
}

func TestLoadConfig_MissingFileIsEmpty(t *testing.T) {
	svc, _ := newService(t)

	got, err := svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := service.Config{Version: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("config mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadConfig_EmptyFileIsEmpty(t *testing.T) {
	svc, path := newService(t)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(service.Config{Version: 1}, got); diff != "" {
		t.Errorf("config mismatch (-want +got):\n%s", diff)
	}
}

func TestSaveConfig_RoundTrip(t *testing.T) {
	svc := service.New(filepath.Join(t.TempDir(), "nested", "kubereach.yaml"))
	want := service.Config{
		Version: 1,
		Clusters: []service.Cluster{{
			ID:         "c1",
			Name:       "staging",
			Kubeconfig: "/home/me/.kube/config",
			Context:    "staging-admin",
			RouteID:    "r1",
			Namespaces: []string{"default", "payments"},
		}},
	}

	if err := svc.SaveConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("config mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadConfig_UnknownVersion(t *testing.T) {
	svc, path := newService(t)
	if err := os.WriteFile(path, []byte("version: 99\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := svc.LoadConfig()
	if !errors.Is(err, service.ErrUnsupportedVersion) {
		t.Fatalf("got %v, want ErrUnsupportedVersion", err)
	}
}
