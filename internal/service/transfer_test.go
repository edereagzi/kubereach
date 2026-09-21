package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
)

func TestExportImport_RoundTripWithRemap(t *testing.T) {
	src, _ := newService(t)
	dir := t.TempDir()
	theirKey := filepath.Join(dir, "their-key")
	theirKubeconfig := filepath.Join(dir, "their-kubeconfig")
	route := service.Route{ID: "r1", Name: "bastion", Servers: []service.SSHServer{
		{Host: "bastion.example.com", Port: 22, User: "ops", Auth: service.AuthKeyFile, KeyFile: theirKey},
	}}
	cluster := service.Cluster{ID: "c1", Name: "staging", Kubeconfig: theirKubeconfig, Context: "staging", RouteID: "r1"}
	forward := service.PortForward{ID: "f1", ClusterID: "c1", Target: service.ForwardTarget{Kind: service.TargetService, Namespace: "default", Name: "web"}, RemotePort: 80, LocalPort: 20000}
	if err := src.SaveConfig(service.Config{Routes: []service.Route{route}, Clusters: []service.Cluster{cluster}, Forwards: []service.PortForward{forward}}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "export.yaml")
	if err := src.ExportConfig(file); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"password", "passphrase", "token", "certificate"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("export mentions %q:\n%s", secret, data)
		}
	}

	dst, _ := newService(t)
	preview, err := dst.InspectImport(file)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{theirKey, theirKubeconfig}, preview.MissingPaths); diff != "" {
		t.Errorf("missing paths mismatch (-want +got):\n%s", diff)
	}
	if len(preview.Routes) != 1 || len(preview.Clusters) != 1 || len(preview.Forwards) != 1 || preview.Duplicates != 0 {
		t.Errorf("preview = %+v", preview)
	}

	if err := dst.ImportConfig(file, nil); err == nil {
		t.Fatal("import with unresolved paths succeeded")
	}

	myKey := filepath.Join(dir, "my-key")
	myKubeconfig := filepath.Join(dir, "my-kubeconfig")
	for _, p := range []string{myKey, myKubeconfig} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := dst.ImportConfig(file, map[string]string{theirKey: myKey, theirKubeconfig: myKubeconfig}); err != nil {
		t.Fatal(err)
	}
	got, err := dst.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	route.Servers[0].KeyFile = myKey
	cluster.Kubeconfig = myKubeconfig
	want := service.Config{Version: 1, Routes: []service.Route{route}, Clusters: []service.Cluster{cluster}, Forwards: []service.PortForward{forward}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("imported config mismatch (-want +got):\n%s", diff)
	}

	preview, err = dst.InspectImport(file)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Duplicates != 3 || len(preview.Routes)+len(preview.Clusters)+len(preview.Forwards) != 0 {
		t.Errorf("second preview = %+v, want 3 duplicates and nothing new", preview)
	}
}

func TestImportConfig_SkipDropsDependents(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	file := filepath.Join(dir, "export.yaml")
	src, _ := newService(t)
	if err := src.SaveConfig(service.Config{
		Routes:   []service.Route{{ID: "r1", Name: "bastion", Servers: []service.SSHServer{{Host: "h", Port: 22, User: "u", Auth: service.AuthKeyFile, KeyFile: missing}}}},
		Clusters: []service.Cluster{{ID: "c1", Name: "gone", Kubeconfig: missing, Context: "x", RouteID: "r1"}, {ID: "c2", Name: "routed", Kubeconfig: file, Context: "y", RouteID: "r1"}, {ID: "c3", Name: "kept", Kubeconfig: file, Context: "z"}},
		Forwards: []service.PortForward{{ID: "f1", ClusterID: "c1", Target: service.ForwardTarget{Kind: service.TargetPod, Namespace: "default", Name: "p"}, RemotePort: 80, LocalPort: 20000}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := src.ExportConfig(file); err != nil {
		t.Fatal(err)
	}

	dst, _ := newService(t)
	if err := dst.ImportConfig(file, map[string]string{missing: dir}); err == nil {
		t.Fatal("directory accepted as a replacement path")
	}
	if err := dst.ImportConfig(file, map[string]string{missing: ""}); err != nil {
		t.Fatal(err)
	}
	got, err := dst.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := service.Config{Version: 1, Clusters: []service.Cluster{{ID: "c3", Name: "kept", Kubeconfig: file, Context: "z"}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("imported config mismatch (-want +got):\n%s", diff)
	}
}

func TestInspectImport_RejectsUnknownVersion(t *testing.T) {
	file := filepath.Join(t.TempDir(), "export.yaml")
	if err := os.WriteFile(file, []byte("version: 99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, _ := newService(t)
	if _, err := svc.InspectImport(file); err == nil {
		t.Fatal("unknown version accepted")
	}
	if _, err := svc.InspectImport(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("absent file accepted")
	}
}
