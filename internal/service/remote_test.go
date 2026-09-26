package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
)

const readKubeconfig = "cat ~/.kube/config"

// newRemoteFixture is a connected Route whose SSH server prints kubeconfig, with the staging server pointed at the test API.
func newRemoteFixture(t *testing.T, kubeconfig string) *routeFixture {
	t.Helper()
	priv, pub := newKeyPair(t)
	startAgent(t, priv)
	f := newRouteFixture(t, "", pub)
	f.ssh.outputs = map[string]string{readKubeconfig: strings.ReplaceAll(kubeconfig, "https://staging.example:6443", f.api.URL)}
	f.connect(t)
	return f
}

func (s *testSSH) ran() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.commands)
}

func TestRemoteKubeconfig_ImportReadsOverSSHAndRereadsOnReconnect(t *testing.T) {
	f := newRemoteFixture(t, twoContextKubeconfig)
	ctx := context.Background()

	contexts, err := f.svc.RemoteContexts(f.route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"prod-admin", "staging-admin"}, contexts); diff != "" {
		t.Errorf("contexts (-want +got):\n%s", diff)
	}
	c, err := f.svc.ImportRemoteCluster(f.route.ID, "staging-admin")
	if err != nil {
		t.Fatal(err)
	}
	want := service.Cluster{ID: c.ID, Name: "127.0.0.1", Remote: "~/.kube/config", Context: "staging-admin", RouteID: f.route.ID}
	if diff := cmp.Diff(want, c); diff != "" {
		t.Errorf("cluster (-want +got):\n%s", diff)
	}
	if v, err := f.svc.CheckReachability(ctx, c.ID); err != nil || v != "v1.30.0-test" {
		t.Fatalf("reachability = %q, %v", v, err)
	}
	if got := f.ssh.ran(); len(got) != 1 || got[0] != readKubeconfig {
		t.Errorf("commands = %q, want one read for the whole connection", got)
	}
	if _, err := f.svc.ImportRemoteCluster(f.route.ID, "staging-admin"); err == nil {
		t.Error("importing the same context twice should fail")
	}

	// Nothing of the kubeconfig reaches the disk: only the reference is saved.
	saved, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), f.api.URL) || strings.Contains(string(saved), "token") {
		t.Errorf("configuration holds kubeconfig content:\n%s", saved)
	}

	f.ssh.dropConnections()
	f.waitState(t, service.StateReconnecting)
	f.waitState(t, service.StateConnected)
	if _, err := f.svc.CheckReachability(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if got := len(f.ssh.ran()); got != 2 {
		t.Errorf("reads after reconnect = %d, want 2", got)
	}
}

func TestRemoteKubeconfig_NeedsConnectedRoute(t *testing.T) {
	_, pub := newKeyPair(t)
	f := newRouteFixture(t, "", pub)
	if _, err := f.svc.RemoteContexts(f.route.ID); !errors.Is(err, service.ErrRouteDown) {
		t.Errorf("err = %v, want ErrRouteDown", err)
	}
}

func TestRemoteKubeconfig_MissingFileNamesThePath(t *testing.T) {
	f := newRemoteFixture(t, twoContextKubeconfig)
	f.ssh.mu.Lock()
	f.ssh.outputs = nil
	f.ssh.mu.Unlock()
	_, err := f.svc.RemoteContexts(f.route.ID)
	if err == nil || !strings.Contains(err.Error(), "~/.kube/config") {
		t.Errorf("err = %v, want one naming ~/.kube/config", err)
	}
}

// A remote kubeconfig must not make Kubereach run a local program or send a local file to the server.
func TestRemoteKubeconfig_RefusesLocalProgramsAndFiles(t *testing.T) {
	edits := map[string][2]string{
		"exec":       {"{token: t}", "{exec: {apiVersion: client.authentication.k8s.io/v1, command: /bin/sh}}"},
		"token file": {"{token: t}", "{tokenFile: /home/me/.ssh/id_ed25519}"},
		"client key": {"{token: t}", "{client-key: /home/me/.ssh/id_ed25519}"},
		"CA file":    {"{server: https://staging.example:6443}", "{server: https://staging.example:6443, certificate-authority: /etc/ca.crt}"},
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			f := newRemoteFixture(t, strings.Replace(twoContextKubeconfig, edit[0], edit[1], 1))
			if _, err := f.svc.ImportRemoteCluster(f.route.ID, "staging-admin"); err == nil {
				t.Error("import should fail")
			}
		})
	}
}

func TestRemoteKubeconfig_StaysOnItsRouteAndSurvivesExport(t *testing.T) {
	f := newRemoteFixture(t, twoContextKubeconfig)
	c, err := f.svc.ImportRemoteCluster(f.route.ID, "staging-admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetClusterRoute(c.ID, ""); err == nil {
		t.Error("a remote Cluster should not be moved off its Route")
	}

	exported := filepath.Join(t.TempDir(), "export.yaml")
	if err := f.svc.ExportConfig(exported); err != nil {
		t.Fatal(err)
	}
	other, _ := newService(t)
	if err := other.ImportConfig(exported, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := other.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(cfg.Clusters, func(o service.Cluster) bool { return o.ID == c.ID && o.Remote == c.Remote }) {
		t.Errorf("imported clusters = %+v, want the remote one", cfg.Clusters)
	}
}
