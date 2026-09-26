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
	f.setOutputs(map[string]string{service.DetectKubeconfig: "~/.kube/config\n", readKubeconfig: kubeconfig})
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

	found, err := f.svc.RemoteContexts(f.route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(service.RemoteKubeconfig{Source: "~/.kube/config", Contexts: []string{"prod-admin", "staging-admin"}}, found); diff != "" {
		t.Errorf("found (-want +got):\n%s", diff)
	}
	c, err := f.svc.ImportRemoteCluster(f.route.ID, "~/.kube/config", "staging-admin")
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
	if diff := cmp.Diff([]string{service.DetectKubeconfig, readKubeconfig}, f.ssh.ran()); diff != "" {
		t.Errorf("commands (-want +got), want one read for the whole connection:\n%s", diff)
	}
	if _, err := f.svc.ImportRemoteCluster(f.route.ID, "~/.kube/config", "staging-admin"); err == nil {
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
	if got := len(f.ssh.ran()); got != 3 {
		t.Errorf("commands after reconnect = %d, want a second read", got)
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
	f.setOutputs(map[string]string{service.DetectKubeconfig: "~/.kube/config\n"})
	_, err := f.svc.RemoteContexts(f.route.ID)
	// The message the UI shows carries the server's own reason, not just that the read failed.
	if msg := service.Describe(err).Message; !strings.Contains(msg, "~/.kube/config") || !strings.Contains(msg, "No such file or directory") {
		t.Errorf("message = %q, want the path and the server's reason", msg)
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
			if _, err := f.svc.ImportRemoteCluster(f.route.ID, "~/.kube/config", "staging-admin"); err == nil {
				t.Error("import should fail")
			}
		})
	}
}

func TestRemoteKubeconfig_StaysOnItsRouteAndSurvivesExport(t *testing.T) {
	f := newRemoteFixture(t, twoContextKubeconfig)
	c, err := f.svc.ImportRemoteCluster(f.route.ID, "~/.kube/config", "staging-admin")
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

// setOutputs replaces what the SSH server prints, keeping the test API's address in any kubeconfig.
func (f *routeFixture) setOutputs(outputs map[string]string) {
	f.ssh.mu.Lock()
	defer f.ssh.mu.Unlock()
	f.ssh.outputs = map[string]string{}
	for cmd, out := range outputs {
		f.ssh.outputs[cmd] = strings.ReplaceAll(out, "https://staging.example:6443", f.api.URL)
	}
	f.ssh.commands = nil
}

func TestRemoteKubeconfig_K3sWithPasswordlessSudo(t *testing.T) {
	f := newRemoteFixture(t, twoContextKubeconfig)
	f.setOutputs(map[string]string{
		service.DetectKubeconfig:                "/etc/rancher/k3s/k3s.yaml\n~/.kube/config\n",
		"sudo -n cat /etc/rancher/k3s/k3s.yaml": twoContextKubeconfig,
	})
	found, err := f.svc.RemoteContexts(f.route.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := service.RemoteKubeconfig{Source: "/etc/rancher/k3s/k3s.yaml", Contexts: []string{"prod-admin", "staging-admin"}}
	if diff := cmp.Diff(want, found); diff != "" {
		t.Errorf("found (-want +got):\n%s", diff)
	}
	c, err := f.svc.ImportRemoteCluster(f.route.ID, found.Source, "staging-admin")
	if err != nil {
		t.Fatal(err)
	}
	if c.Remote != "/etc/rancher/k3s/k3s.yaml" {
		t.Errorf("remote = %q", c.Remote)
	}
	if _, err := f.svc.CheckReachability(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	wantRan := []string{service.DetectKubeconfig, "cat /etc/rancher/k3s/k3s.yaml", "sudo -n cat /etc/rancher/k3s/k3s.yaml"}
	if diff := cmp.Diff(wantRan, f.ssh.ran()); diff != "" {
		t.Errorf("commands (-want +got):\n%s", diff)
	}
}

// A distro's own kubeconfig wins over ~/.kube/config, whatever order the server lists them in.
func TestRemoteKubeconfig_CandidateOrder(t *testing.T) {
	cases := []struct{ found, read, want string }{
		{"~/.kube/config\n/etc/rancher/k3s/k3s.yaml\n", "cat /etc/rancher/k3s/k3s.yaml", "/etc/rancher/k3s/k3s.yaml"},
		{"~/.kube/config\n/etc/kubernetes/admin.conf\n/etc/rancher/rke2/rke2.yaml\n", "cat /etc/rancher/rke2/rke2.yaml", "/etc/rancher/rke2/rke2.yaml"},
		{"/var/lib/k0s/pki/admin.conf\nk0s kubeconfig admin\n", "/usr/local/bin/k0s kubeconfig admin", "k0s kubeconfig admin"},
		{"/etc/kubernetes/admin.conf\nmicrok8s config -l\n", "/snap/bin/microk8s config -l", "microk8s config -l"},
		{"~/.kube/config\n/etc/kubernetes/admin.conf\n", "cat /etc/kubernetes/admin.conf", "/etc/kubernetes/admin.conf"},
		{"~/.kube/config\n", "cat ~/.kube/config", "~/.kube/config"},
		// A copy in ~/.kube/config is used when the distro's own is denied.
		{"/etc/rancher/k3s/k3s.yaml\n~/.kube/config\n", "cat ~/.kube/config", "~/.kube/config"},
	}
	for _, tc := range cases {
		// A fixture each, as a read is kept for the whole connection.
		f := newRemoteFixture(t, twoContextKubeconfig)
		f.setOutputs(map[string]string{service.DetectKubeconfig: tc.found, tc.read: twoContextKubeconfig})
		found, err := f.svc.RemoteContexts(f.route.ID)
		if err != nil || found.Source != tc.want {
			t.Errorf("found %q: source = %q, %v; want %q", tc.found, found.Source, err, tc.want)
		}
	}

	f := newRemoteFixture(t, twoContextKubeconfig)
	f.setOutputs(map[string]string{service.DetectKubeconfig: ""})
	if _, err := f.svc.RemoteContexts(f.route.ID); !strings.Contains(service.Describe(err).Message, "no kubeconfig") {
		t.Errorf("err = %v, want one saying nothing was found", err)
	}
}

// When neither a plain read nor sudo -n is allowed, the message says what to run on the server.
func TestRemoteKubeconfig_DeniedSaysWhatToRun(t *testing.T) {
	cases := map[string]string{
		"/etc/rancher/k3s/k3s.yaml":   "--write-kubeconfig-mode 644",
		"/etc/rancher/rke2/rke2.yaml": "--write-kubeconfig-mode 644",
		"microk8s config -l":          "usermod -a -G microk8s $USER",
		"/var/lib/k0s/pki/admin.conf": "NOPASSWD: /usr/bin/cat /var/lib/k0s/pki/admin.conf",
		"k0s kubeconfig admin":        "NOPASSWD: /usr/local/bin/k0s kubeconfig admin",
		"/etc/kubernetes/admin.conf":  "NOPASSWD: /usr/bin/cat /etc/kubernetes/admin.conf",
	}
	f := newRemoteFixture(t, twoContextKubeconfig)
	for source, want := range cases {
		f.setOutputs(map[string]string{service.DetectKubeconfig: source + "\n"})
		_, err := f.svc.RemoteContexts(f.route.ID)
		if msg := service.Describe(err).Message; !strings.Contains(msg, source) || !strings.Contains(msg, want) {
			t.Errorf("%s: message = %q, want it to name %q", source, msg, want)
		}
	}
}
