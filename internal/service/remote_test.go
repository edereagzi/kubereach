package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	gliderssh "github.com/gliderlabs/ssh"
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

// A remote kubeconfig must not make Kubereach run a program, nor read a file here; a file it cannot read on the server
// is named in the reason.
func TestRemoteKubeconfig_RefusesProgramsAndUnreadableFiles(t *testing.T) {
	edits := map[string][3]string{
		"exec":       {"{token: t}", "{exec: {apiVersion: client.authentication.k8s.io/v1, command: /bin/sh}}", "program"},
		"token file": {"{token: t}", "{tokenFile: /home/me/token}", "/home/me/token"},
		"client key": {"{token: t}", "{client-key: /home/me/.ssh/id_ed25519}", "/home/me/.ssh/id_ed25519"},
		"CA file":    {"{server: https://staging.example:6443}", "{server: https://staging.example:6443, certificate-authority: /etc/ca.crt}", "/etc/ca.crt"},
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			f := newRemoteFixture(t, strings.Replace(twoContextKubeconfig, edit[0], edit[1], 1))
			_, err := f.svc.ImportRemoteCluster(f.route.ID, "~/.kube/config", "staging-admin")
			if msg := service.Describe(err).Message; !strings.Contains(msg, edit[2]) {
				t.Errorf("message = %q, want it to name %s", msg, edit[2])
			}
		})
	}
}

// minikube's kubeconfig names its certificates by path; they are read on the server, once each, relative ones from the
// kubeconfig's directory.
func TestRemoteKubeconfig_ReadsTheFilesItNamesOnTheServer(t *testing.T) {
	kubeconfig := strings.NewReplacer(
		"{server: https://staging.example:6443}", "{server: https://staging.example:6443, certificate-authority: /home/me/.minikube/ca.crt}",
		"{server: https://prod.example:6443}", "{server: https://prod.example:6443, certificate-authority: /home/me/.minikube/ca.crt}",
		"{token: t}", "{client-certificate: profiles/client.crt, client-key: /home/me/it's.key}",
	).Replace(twoContextKubeconfig)
	f := newRemoteFixture(t, kubeconfig)
	files := []string{"cat '/home/me/.minikube/ca.crt'", "cat ~/'.kube/profiles/client.crt'", `cat '/home/me/it'\''s.key'`}
	f.setOutputs(map[string]string{service.DetectKubeconfig: "~/.kube/config\n", readKubeconfig: kubeconfig, files[0]: "CA", files[1]: "CERT", files[2]: "KEY"})

	if _, err := f.svc.ImportRemoteCluster(f.route.ID, "~/.kube/config", "staging-admin"); err != nil {
		t.Fatal(err)
	}
	ran := f.ssh.ran()
	if diff := cmp.Diff(slices.Sorted(slices.Values(append([]string{readKubeconfig}, files...))), slices.Sorted(slices.Values(ran))); diff != "" {
		t.Errorf("commands (-want +got), want each file read once:\n%s", diff)
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
		service.DetectKubeconfig:                         "/etc/rancher/k3s/k3s.yaml\n~/.kube/config\n",
		"LC_ALL=C sudo -n cat /etc/rancher/k3s/k3s.yaml": twoContextKubeconfig,
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
	wantRan := []string{service.DetectKubeconfig, "cat /etc/rancher/k3s/k3s.yaml", "LC_ALL=C sudo -n cat /etc/rancher/k3s/k3s.yaml"}
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

func TestRemoteKubeconfig_AsksForTheSudoPasswordOnce(t *testing.T) {
	f := newRemoteFixture(t, twoContextKubeconfig)
	const readK3s = "LC_ALL=C sudo -S -p '' cat /etc/rancher/k3s/k3s.yaml"
	// A readable copy in ~/.kube/config does not stand in for the distro's own when sudo only needs its password.
	f.setOutputs(map[string]string{service.DetectKubeconfig: "/etc/rancher/k3s/k3s.yaml\n~/.kube/config\n", readK3s: twoContextKubeconfig, readKubeconfig: twoContextKubeconfig})
	f.ssh.mu.Lock()
	f.ssh.sudoPassword = "s3cret"
	f.ssh.mu.Unlock()
	target := "me@" + f.ssh.addr

	_, err := f.svc.RemoteContexts(f.route.ID)
	if info := service.Describe(err); info.Code != "sudo" || info.Target != target {
		t.Fatalf("error = %+v, want the sudo password of %s asked for", info, target)
	}

	if err := f.svc.SetSudoPassword(f.route.ID, "wrong"); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.RemoteContexts(f.route.ID)
	if info := service.Describe(err); info.Code != "sudo" || !strings.Contains(info.Message, "not accepted") {
		t.Fatalf("error = %+v, want the wrong password reported and asked again", info)
	}

	if err := f.svc.SetSudoPassword(f.route.ID, "s3cret"); err != nil {
		t.Fatal(err)
	}
	found, err := f.svc.RemoteContexts(f.route.ID)
	if err != nil || found.Source != "/etc/rancher/k3s/k3s.yaml" {
		t.Fatalf("found = %+v, %v", found, err)
	}
	c, err := f.svc.ImportRemoteCluster(f.route.ID, found.Source, "staging-admin")
	if err != nil {
		t.Fatal(err)
	}

	// A reconnect reads the kubeconfig again with the password kept for the session, without asking.
	f.ssh.dropConnections()
	f.waitState(t, service.StateReconnecting)
	f.waitState(t, service.StateConnected)
	if _, err := f.svc.CheckReachability(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}

	// The password goes on stdin only: never in a command line, never on disk.
	for _, cmd := range f.ssh.ran() {
		if strings.Contains(cmd, "s3cret") {
			t.Errorf("command %q carries the password", cmd)
		}
	}
	if saved, err := os.ReadFile(f.configPath); err != nil || strings.Contains(string(saved), "s3cret") {
		t.Errorf("configuration holds the password (%v):\n%s", err, saved)
	}
}

// A read that never finishes fails with a clear message, without trying sudo, and does not hold up the next read.
func TestRemoteKubeconfig_HangingReadTimesOut(t *testing.T) {
	f := newRemoteFixture(t, twoContextKubeconfig)
	f.svc.RemoteCommandTimeout = 100 * time.Millisecond
	const readK0s = "/usr/local/bin/k0s kubeconfig admin"
	f.setOutputs(map[string]string{service.DetectKubeconfig: "k0s kubeconfig admin\n"})
	f.ssh.mu.Lock()
	f.ssh.hang = readK0s
	f.ssh.mu.Unlock()

	_, err := f.svc.RemoteContexts(f.route.ID)
	if msg := service.Describe(err).Message; !strings.Contains(msg, "k0s kubeconfig admin") || !strings.Contains(msg, "did not finish") {
		t.Errorf("message = %q, want the source named and that it did not finish", msg)
	}
	if diff := cmp.Diff([]string{service.DetectKubeconfig, readK0s}, f.ssh.ran()); diff != "" {
		t.Errorf("commands (-want +got), want no sudo after a hang:\n%s", diff)
	}
	// The command is stopped, not left running on the server: the app retries a Cluster on its own, and each retry
	// would leave one more behind.
	var signals []gliderssh.Signal
	for deadline := time.Now().Add(time.Second); len(signals) == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		f.ssh.mu.Lock()
		signals = slices.Clone(f.ssh.signals)
		f.ssh.mu.Unlock()
	}
	if diff := cmp.Diff([]gliderssh.Signal{gliderssh.SIGTERM}, signals); diff != "" {
		t.Errorf("signals (-want +got):\n%s", diff)
	}

	f.setOutputs(map[string]string{service.DetectKubeconfig: "k0s kubeconfig admin\n", readK0s: twoContextKubeconfig})
	f.ssh.mu.Lock()
	f.ssh.hang = ""
	f.ssh.mu.Unlock()
	if found, err := f.svc.RemoteContexts(f.route.ID); err != nil || found.Source != "k0s kubeconfig admin" {
		t.Errorf("after the hang: found = %+v, %v", found, err)
	}
}

// Calls that arrive while a read hangs wait for it and share its failure, rather than each running the command in turn.
func TestRemoteKubeconfig_CallsDuringAHangShareIt(t *testing.T) {
	f := newRemoteFixture(t, twoContextKubeconfig)
	f.svc.RemoteCommandTimeout = 300 * time.Millisecond
	c, err := f.svc.ImportRemoteCluster(f.route.ID, "~/.kube/config", "staging-admin")
	if err != nil {
		t.Fatal(err)
	}
	f.ssh.dropConnections()
	f.waitState(t, service.StateReconnecting)
	f.waitState(t, service.StateConnected)
	f.setOutputs(nil)
	f.ssh.mu.Lock()
	f.ssh.hang = readKubeconfig
	f.ssh.mu.Unlock()

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := f.svc.CheckReachability(context.Background(), c.ID); !strings.Contains(service.Describe(err).Message, "did not finish") {
				t.Errorf("err = %v, want the read's timeout", err)
			}
		})
	}
	wg.Wait()
	if got := f.ssh.ran(); len(got) != 1 {
		t.Errorf("commands = %q, want one read", got)
	}
}
