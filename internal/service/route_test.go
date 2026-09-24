package service_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// testSSH is an in-process SSH server that allows direct-tcpip forwarding and records where to.
type testSSH struct {
	addr     string
	hostKey  ssh.PublicKey
	mu       sync.Mutex
	forwards []string
	conns    []*stallConn
	hold     chan struct{}
}

// stallConn plays a link that died silently.
type stallConn struct {
	net.Conn
	mu      sync.Mutex
	stalled bool
	closed  chan struct{}
}

func (c *stallConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.mu.Lock()
	stalled := c.stalled
	c.mu.Unlock()
	if stalled {
		<-c.closed
		return 0, net.ErrClosed
	}
	return n, err
}

func (c *stallConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return c.Conn.Close()
}

// server describes this test server as an SSH Server; empty keyFile means agent auth.
func (s *testSSH) server(user, keyFile string) service.SSHServer {
	host, port, _ := net.SplitHostPort(s.addr)
	var portNum int
	_, _ = fmt.Sscan(port, &portNum)
	srv := service.SSHServer{Host: host, Port: portNum, User: user, Auth: service.AuthKeyFile, KeyFile: keyFile}
	if keyFile == "" {
		srv.Auth = service.AuthAgent
	}
	return srv
}

// trust appends the server's host key to the fixture's known_hosts, as a previous approval would have.
func (f *routeFixture) trust(t *testing.T, s *testSSH) {
	t.Helper()
	line := knownhosts.Line([]string{s.addr}, s.hostKey) + "\n"
	fh, err := os.OpenFile(f.svc.KnownHostsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString(line); err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()
}

func startSSHServer(t *testing.T, authorized ssh.PublicKey) *testSSH {
	return startSSHServerWithPassword(t, authorized, "")
}

// startSSHServerWithPassword also accepts the given password when it is non-empty.
func startSSHServerWithPassword(t *testing.T, authorized ssh.PublicKey, password string) *testSSH {
	return startSSHServerWith(t, authorized, password)
}

// startSSHServerWith also presents the extra host keys, after its own ed25519 one.
func startSSHServerWith(t *testing.T, authorized ssh.PublicKey, password string, extra ...gliderssh.Signer) *testSSH {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostPriv, hostPub := newKeyPair(t)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	s := &testSSH{addr: l.Addr().String(), hostKey: hostPub}
	srv := &gliderssh.Server{
		HostSigners: append([]gliderssh.Signer{hostSigner}, extra...),
		PublicKeyHandler: func(_ gliderssh.Context, key gliderssh.PublicKey) bool {
			return gliderssh.KeysEqual(key, authorized)
		},
		PasswordHandler: func(_ gliderssh.Context, given string) bool {
			return password != "" && given == password
		},
		LocalPortForwardingCallback: func(_ gliderssh.Context, host string, port uint32) bool {
			s.mu.Lock()
			s.forwards = append(s.forwards, net.JoinHostPort(host, fmt.Sprint(port)))
			hold := s.hold
			s.mu.Unlock()
			if hold != nil {
				<-hold
			}
			return true
		},
		ConnCallback: func(_ gliderssh.Context, conn net.Conn) net.Conn {
			s.mu.Lock()
			defer s.mu.Unlock()
			sc := &stallConn{Conn: conn, closed: make(chan struct{})}
			s.conns = append(s.conns, sc)
			return sc
		},
		ChannelHandlers: map[string]gliderssh.ChannelHandler{"direct-tcpip": gliderssh.DirectTCPIPHandler},
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return s
}

// dropConnections closes every accepted connection, as a network blip would.
func (s *testSSH) dropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.conns = nil
}

func (s *testSSH) stall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.mu.Lock()
		c.stalled = true
		c.mu.Unlock()
	}
	s.conns = nil
}

func (s *testSSH) holdForwards(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	s.mu.Lock()
	s.hold = hold
	s.mu.Unlock()
}

func (s *testSSH) forwardedTo() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.forwards)
}

func newKeyPair(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return priv, sshPub
}

func writeKeyFile(t *testing.T, priv ed25519.PrivateKey, passphrase string) string {
	t.Helper()
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func startAgent(t *testing.T, priv ed25519.PrivateKey) {
	t.Helper()
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	// Unix socket paths are short-limited, so avoid the long t.TempDir path.
	dir, err := os.MkdirTemp("", "agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { _ = agent.ServeAgent(keyring, c) }()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
}

// startAPIServer serves the version endpoint the reachability check calls and the port-forward subresource.
func startAPIServer(t *testing.T, pods *testAPI) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"gitVersion":"v1.30.0-test"}`))
	})
	mux.HandleFunc("/api/v1/namespaces/", pods.podHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type routeFixture struct {
	svc        *service.Service
	events     chan service.RouteStatus
	hostKeys   chan service.HostKeyPrompt
	ssh        *testSSH
	api        *httptest.Server
	podAPI     *testAPI
	cluster    string
	route      service.Route
	configPath string
}

// newRouteFixture wires a Cluster whose API is reached through a Route with one SSH Server; empty keyFile means agent auth.
// Every test server's host key is trusted up front; host key tests start from an empty known_hosts.
func newRouteFixture(t *testing.T, keyFile string, pub ssh.PublicKey) *routeFixture {
	t.Helper()
	f := &routeFixture{
		ssh:      startSSHServer(t, pub),
		podAPI:   newTestAPI(),
		events:   make(chan service.RouteStatus, 100),
		hostKeys: make(chan service.HostKeyPrompt, 10),
	}
	f.api = startAPIServer(t, f.podAPI)
	dir := t.TempDir()
	f.configPath = filepath.Join(dir, "kubereach.yaml")
	f.svc = service.New(f.configPath, nil)
	f.svc.KnownHostsPath = filepath.Join(dir, "known_hosts")
	f.svc.Emit = func(_ string, data any) {
		switch data := data.(type) {
		case service.RouteStatus:
			f.events <- data
		case service.HostKeyPrompt:
			f.hostKeys <- data
		}
	}
	f.trust(t, f.ssh)

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	content := strings.ReplaceAll(twoContextKubeconfig, "https://staging.example:6443", f.api.URL)
	if err := os.WriteFile(kubeconfig, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	clusters, err := f.svc.ImportKubeconfigs([]string{kubeconfig})
	if err != nil {
		t.Fatal(err)
	}
	f.cluster = clusters[slices.IndexFunc(clusters, func(c service.Cluster) bool { return c.Context == "staging-admin" })].ID

	server := f.ssh.server("me", keyFile)
	f.route, err = f.svc.SaveRoute(service.Route{Name: "staging-ssh", Servers: []service.SSHServer{server}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetClusterRoute(f.cluster, f.route.ID); err != nil {
		t.Fatal(err)
	}
	return f
}

// waitState drains events until the Route reaches want, failing on timeout.
func (f *routeFixture) waitState(t *testing.T, want service.State) service.RouteStatus {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-f.events:
			if ev.State == want {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for state %q", want)
		}
	}
}

func (f *routeFixture) connect(t *testing.T) {
	t.Helper()
	if err := f.svc.ConnectRoute(f.route.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateConnecting)
	f.waitState(t, service.StateConnected)
}

func TestRoute_SaveAndAttach(t *testing.T) {
	svc, _ := newService(t)
	clusters, err := svc.ImportKubeconfigs([]string{writeKubeconfig(t)})
	if err != nil {
		t.Fatal(err)
	}

	route, err := svc.SaveRoute(service.Route{Name: "staging-ssh", Servers: []service.SSHServer{{Host: "b.example", User: "me", Auth: service.AuthAgent}}})
	if err != nil {
		t.Fatal(err)
	}
	if route.ID == "" {
		t.Fatal("saved route has no ID")
	}
	route.Name = "staging-ssh-2"
	if _, err := svc.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
	for _, c := range clusters {
		if err := svc.SetClusterRoute(c.ID, route.ID); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := []service.Route{{ID: route.ID, Name: "staging-ssh-2", Servers: []service.SSHServer{{Host: "b.example", Port: 22, User: "me", Auth: service.AuthAgent}}}}
	if diff := cmp.Diff(want, cfg.Routes); diff != "" {
		t.Errorf("routes mismatch (-want +got):\n%s", diff)
	}
	for _, c := range cfg.Clusters {
		if c.RouteID != route.ID {
			t.Errorf("cluster %s route = %q, want %q", c.Name, c.RouteID, route.ID)
		}
	}

	if err := svc.DeleteRoute(route.ID); err == nil {
		t.Error("deleting a Route in use should fail")
	}
	if err := svc.SetClusterRoute("missing", route.ID); err == nil {
		t.Error("attaching to unknown cluster should fail")
	}
	if err := svc.SetClusterRoute(clusters[0].ID, "missing"); err == nil {
		t.Error("attaching unknown route should fail")
	}
}

func TestRoute_SaveRejectsInvalidServer(t *testing.T) {
	svc, _ := newService(t)
	bad := []service.Route{
		{Name: "no servers"},
		{Name: "no host", Servers: []service.SSHServer{{User: "me", Auth: service.AuthAgent}}},
		{Name: "no user", Servers: []service.SSHServer{{Host: "h", Auth: service.AuthAgent}}},
		{Name: "bad auth", Servers: []service.SSHServer{{Host: "h", User: "me", Auth: "magic"}}},
		{Name: "key without file", Servers: []service.SSHServer{{Host: "h", User: "me", Auth: service.AuthKeyFile}}},
		{Name: "bad port", Servers: []service.SSHServer{{Host: "h", Port: 70000, User: "me", Auth: service.AuthAgent}}},
	}
	for _, r := range bad {
		if _, err := svc.SaveRoute(r); err == nil {
			t.Errorf("%s: expected validation error", r.Name)
		}
	}
}

func TestRoute_KeyFileConnectAndAPIThroughRoute(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	ctx := context.Background()

	if _, err := f.svc.CheckReachability(ctx, f.cluster); !errors.Is(err, service.ErrRouteDown) {
		t.Fatalf("before connect: got %v, want ErrRouteDown", err)
	}

	f.connect(t)
	version, err := f.svc.CheckReachability(ctx, f.cluster)
	if err != nil || version != "v1.30.0-test" {
		t.Fatalf("through route: version=%q err=%v", version, err)
	}
	apiAddr := strings.TrimPrefix(f.api.URL, "http://")
	if !slices.Contains(f.ssh.forwardedTo(), apiAddr) {
		t.Errorf("API traffic was not forwarded through SSH; forwards=%v", f.ssh.forwardedTo())
	}
	statuses := f.svc.RouteStatuses()
	if len(statuses) != 1 || statuses[0].State != service.StateConnected {
		t.Errorf("statuses = %+v, want one connected", statuses)
	}

	if err := f.svc.StopRoute(f.route.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)
	if _, err := f.svc.CheckReachability(ctx, f.cluster); !errors.Is(err, service.ErrRouteDown) {
		t.Fatalf("after stop: got %v, want ErrRouteDown", err)
	}
}

func TestRoute_AgentConnect(t *testing.T) {
	priv, pub := newKeyPair(t)
	startAgent(t, priv)
	f := newRouteFixture(t, "", pub)

	f.connect(t)
	if _, err := f.svc.CheckReachability(context.Background(), f.cluster); err != nil {
		t.Fatal(err)
	}
}

func TestRoute_PassphraseAskedOnceAndNeverPersisted(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, "hunter2"), pub)

	var need *service.CredentialError
	if err := f.svc.ConnectRoute(f.route.ID, ""); !errors.As(err, &need) || need.Code != "passphrase" || need.Target != f.route.Servers[0].KeyFile {
		t.Fatalf("got %v, want passphrase CredentialError for the key file", err)
	}
	if err := f.svc.ConnectRoute(f.route.ID, "wrong"); err == nil || errors.As(err, &need) {
		t.Fatalf("wrong passphrase: got %v", err)
	}
	if err := f.svc.ConnectRoute(f.route.ID, "hunter2"); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateConnected)
	if err := f.svc.StopRoute(f.route.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)

	f.connect(t)

	data, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hunter2") {
		t.Error("passphrase was persisted to the configuration file")
	}
}

func TestRoute_ReconnectsAfterDrop(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	f.connect(t)

	f.ssh.dropConnections()
	ev := f.waitState(t, service.StateReconnecting)
	if ev.Error == "" {
		t.Error("reconnecting event carries no reason")
	}
	f.waitState(t, service.StateConnected)

	if _, err := f.svc.CheckReachability(context.Background(), f.cluster); err != nil {
		t.Fatalf("after reconnect: %v", err)
	}
}

func TestRoute_ReconnectsWhenKeepalivesGoUnanswered(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	f.svc.RouteKeepalive = 50 * time.Millisecond
	f.connect(t)

	f.ssh.stall()
	if ev := f.waitState(t, service.StateReconnecting); !strings.Contains(ev.Error, "keepalive") {
		t.Errorf("reconnecting reason = %q, want the missed keepalives", ev.Error)
	}
	f.waitState(t, service.StateConnected)
	if _, err := f.svc.CheckReachability(context.Background(), f.cluster); err != nil {
		t.Fatalf("after reconnect: %v", err)
	}
}

func TestRoute_DialReturnsWhenItsContextIsCancelled(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	dials := make(chan service.DialFunc, 1)
	svc := service.New(f.configPath, func(_ service.Cluster, dial service.DialFunc) (kubernetes.Interface, *rest.Config, error) {
		dials <- dial
		return fake.NewClientset(), &rest.Config{}, nil
	})
	svc.KnownHostsPath = f.svc.KnownHostsPath
	svc.Emit = f.svc.Emit
	f.svc = svc
	f.connect(t)
	if _, err := svc.CheckReachability(context.Background(), f.cluster); err != nil {
		t.Fatal(err)
	}
	dial := <-dials

	f.ssh.holdForwards(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := dial(ctx, "tcp", strings.TrimPrefix(f.api.URL, "http://"))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("dial = %v, want the context's deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dial through the Route ignored its context")
	}
}

func TestRoute_StopCancelsRetries(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	_, _ = fmt.Sscan(port, &f.route.Servers[0].Port)
	if _, err := f.svc.SaveRoute(f.route); err != nil {
		t.Fatal(err)
	}

	if err := f.svc.ConnectRoute(f.route.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateConnecting)
	f.waitState(t, service.StateReconnecting)
	f.waitState(t, service.StateReconnecting)

	if err := f.svc.StopRoute(f.route.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)
	select {
	case ev := <-f.events:
		t.Fatalf("event after stop: %+v", ev)
	case <-time.After(2500 * time.Millisecond):
	}
}

func TestRoute_StopReturnsWhileHandshakeStalls(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	// A server that accepts TCP but never speaks SSH.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			if _, err := l.Accept(); err != nil {
				return
			}
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_, _ = fmt.Sscan(port, &f.route.Servers[0].Port)
	if _, err := f.svc.SaveRoute(f.route); err != nil {
		t.Fatal(err)
	}

	if err := f.svc.ConnectRoute(f.route.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateConnecting)
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	if err := f.svc.StopRoute(f.route.ID); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("StopRoute took %v while the handshake was stalled", elapsed)
	}
	f.waitState(t, service.StateStopped)
}

func TestRoute_SeveralServersAreDialledThroughEachOther(t *testing.T) {
	priv, pub := newKeyPair(t)
	keyFile := writeKeyFile(t, priv, "")
	f := newRouteFixture(t, keyFile, pub)
	second := startSSHServer(t, pub)
	f.trust(t, second)
	f.route.Servers = append(f.route.Servers, second.server("me", keyFile))
	if _, err := f.svc.SaveRoute(f.route); err != nil {
		t.Fatal(err)
	}

	f.connect(t)
	if _, err := f.svc.CheckReachability(context.Background(), f.cluster); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.ssh.forwardedTo(), second.addr) {
		t.Errorf("first server did not forward to the second; forwards=%v", f.ssh.forwardedTo())
	}
	apiAddr := strings.TrimPrefix(f.api.URL, "http://")
	if !slices.Contains(second.forwardedTo(), apiAddr) {
		t.Errorf("second server did not forward to the API; forwards=%v", second.forwardedTo())
	}
	if slices.Contains(f.ssh.forwardedTo(), apiAddr) {
		t.Error("API traffic bypassed the second server")
	}

	if err := f.svc.StopRoute(f.route.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)
}

func TestRoute_FailureNamesTheServer(t *testing.T) {
	priv, pub := newKeyPair(t)
	keyFile := writeKeyFile(t, priv, "")
	f := newRouteFixture(t, keyFile, pub)
	f.route.Servers = append(f.route.Servers, service.SSHServer{Host: "127.0.0.1", Port: closedPort(t), User: "me", Auth: service.AuthKeyFile, KeyFile: keyFile})
	if _, err := f.svc.SaveRoute(f.route); err != nil {
		t.Fatal(err)
	}

	if err := f.svc.ConnectRoute(f.route.ID, ""); err != nil {
		t.Fatal(err)
	}
	ev := f.waitState(t, service.StateReconnecting)
	want := fmt.Sprintf("127.0.0.1:%d", f.route.Servers[1].Port)
	if !strings.Contains(ev.Error, want) {
		t.Errorf("error %q does not name the failed server %q", ev.Error, want)
	}
	if err := f.svc.StopRoute(f.route.ID); err != nil {
		t.Fatal(err)
	}
}

func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestRoute_UnknownHostKeyApprovedOnceAndPersisted(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	if err := os.Remove(f.svc.KnownHostsPath); err != nil {
		t.Fatal(err)
	}

	if err := f.svc.ConnectRoute(f.route.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateConnecting)
	var prompt service.HostKeyPrompt
	select {
	case prompt = <-f.hostKeys:
	case <-time.After(5 * time.Second):
		t.Fatal("no host key prompt")
	}
	want := service.HostKeyPrompt{RouteID: f.route.ID, Address: f.ssh.addr, KeyType: "ssh-ed25519", Fingerprint: ssh.FingerprintSHA256(f.ssh.hostKey)}
	if diff := cmp.Diff(want, prompt); diff != "" {
		t.Fatalf("prompt mismatch (-want +got):\n%s", diff)
	}
	if err := f.svc.AnswerHostKey(f.route.ID, true); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateConnected)

	data, err := os.ReadFile(f.svc.KnownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), knownhosts.Line([]string{f.ssh.addr}, f.ssh.hostKey)) {
		t.Errorf("known_hosts does not contain the approved key:\n%s", data)
	}

	if err := f.svc.StopRoute(f.route.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)
	f.connect(t)
	select {
	case p := <-f.hostKeys:
		t.Fatalf("prompted again for a known host: %+v", p)
	default:
	}
}

func TestRoute_UnknownHostKeyRejectedAbortsWithoutRetry(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	if err := os.Remove(f.svc.KnownHostsPath); err != nil {
		t.Fatal(err)
	}

	if err := f.svc.ConnectRoute(f.route.ID, ""); err != nil {
		t.Fatal(err)
	}
	<-f.hostKeys
	if err := f.svc.AnswerHostKey(f.route.ID, false); err != nil {
		t.Fatal(err)
	}
	ev := f.waitState(t, service.StateError)
	if !strings.Contains(ev.Error, f.ssh.addr) {
		t.Errorf("error %q does not name the rejected server", ev.Error)
	}
	if _, err := os.Stat(f.svc.KnownHostsPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("rejected key was written to known_hosts")
	}
	select {
	case ev := <-f.events:
		t.Fatalf("event after rejection: %+v", ev)
	case <-time.After(1500 * time.Millisecond):
	}
	if _, err := f.svc.CheckReachability(context.Background(), f.cluster); !errors.Is(err, service.ErrRouteDown) {
		t.Errorf("after rejection: got %v, want ErrRouteDown", err)
	}
}

// OpenSSH often records only a host's ed25519 key; a server that also offers ECDSA must still be recognised.
func TestRoute_KnownKeyTypeIsNegotiated(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaSigner, err := ssh.NewSignerFromKey(ecdsaKey)
	if err != nil {
		t.Fatal(err)
	}
	both := startSSHServerWith(t, pub, "", ecdsaSigner)
	f.trust(t, both)
	f.route.Servers = []service.SSHServer{both.server("me", f.route.Servers[0].KeyFile)}
	if _, err := f.svc.SaveRoute(f.route); err != nil {
		t.Fatal(err)
	}

	f.connect(t)
}

func TestRoute_ChangedHostKeyIsRefused(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	_, otherKey := newKeyPair(t)
	line := knownhosts.Line([]string{f.ssh.addr}, otherKey) + "\n"
	if err := os.WriteFile(f.svc.KnownHostsPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := f.svc.ConnectRoute(f.route.ID, ""); err != nil {
		t.Fatal(err)
	}
	ev := f.waitState(t, service.StateError)
	if !strings.Contains(ev.Error, "does not match") {
		t.Errorf("error %q does not report the mismatch", ev.Error)
	}
	select {
	case p := <-f.hostKeys:
		t.Fatalf("mismatched key was offered for approval: %+v", p)
	default:
	}
}

func TestRoute_PasswordAskedPerSessionAndNeverPersisted(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	pwServer := startSSHServerWithPassword(t, pub, "s3cret")
	f.trust(t, pwServer)
	srv := pwServer.server("me", "")
	srv.Auth = service.AuthPassword
	f.route.Servers = []service.SSHServer{srv}
	if _, err := f.svc.SaveRoute(f.route); err != nil {
		t.Fatal(err)
	}

	var need *service.CredentialError
	if err := f.svc.ConnectRoute(f.route.ID, ""); !errors.As(err, &need) || need.Code != "password" || need.Target != "me@"+pwServer.addr {
		t.Fatalf("got %v, want password CredentialError for me@%s", err, pwServer.addr)
	}
	if err := f.svc.ConnectRoute(f.route.ID, "wrong"); err != nil {
		t.Fatal(err)
	}
	ev := f.waitState(t, service.StateError)
	if !strings.Contains(ev.Error, pwServer.addr) {
		t.Errorf("error %q does not name the server", ev.Error)
	}
	if err := f.svc.ConnectRoute(f.route.ID, ""); !errors.As(err, &need) {
		t.Fatalf("after a refused password: got %v, want CredentialError", err)
	}

	if err := f.svc.ConnectRoute(f.route.ID, "s3cret"); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateConnected)
	if _, err := f.svc.CheckReachability(context.Background(), f.cluster); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.StopRoute(f.route.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)

	f.connect(t)

	data, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "s3cret") {
		t.Error("password was persisted to the configuration file")
	}
}

func TestRoute_EachServerGetsItsOwnSecret(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, "hunter2"), pub)
	pwServer := startSSHServerWithPassword(t, pub, "s3cret")
	f.trust(t, pwServer)
	second := pwServer.server("me", "")
	second.Auth = service.AuthPassword
	f.route.Servers = append(f.route.Servers, second)
	if _, err := f.svc.SaveRoute(f.route); err != nil {
		t.Fatal(err)
	}

	var need *service.CredentialError
	if err := f.svc.ConnectRoute(f.route.ID, ""); !errors.As(err, &need) || need.Code != "passphrase" {
		t.Fatalf("first call: got %v, want passphrase CredentialError", err)
	}
	if err := f.svc.ConnectRoute(f.route.ID, "hunter2"); !errors.As(err, &need) || need.Code != "password" {
		t.Fatalf("after passphrase: got %v, want password CredentialError", err)
	}
	if err := f.svc.ConnectRoute(f.route.ID, "s3cret"); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateConnected)
	if _, err := f.svc.CheckReachability(context.Background(), f.cluster); err != nil {
		t.Fatal(err)
	}
}

func TestRoute_AnswerHostKeyWithoutPromptIsRefused(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	if err := f.svc.AnswerHostKey(f.route.ID, true); err == nil {
		t.Error("answer before connecting was accepted")
	}
	f.connect(t)
	if err := f.svc.AnswerHostKey(f.route.ID, true); err == nil {
		t.Error("answer with no pending prompt was accepted")
	}
}

func TestRoute_ClientOutlivesARouteRestart(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	builds := 0
	svc := service.New(f.configPath, func(_ service.Cluster, dial service.DialFunc) (kubernetes.Interface, *rest.Config, error) {
		builds++
		cfg := &rest.Config{Host: f.api.URL, Dial: dial}
		cs, err := kubernetes.NewForConfig(cfg)
		return cs, cfg, err
	})
	svc.KnownHostsPath = f.svc.KnownHostsPath
	svc.Emit = f.svc.Emit
	f.svc = svc

	if _, err := svc.CheckReachability(context.Background(), f.cluster); !errors.Is(err, service.ErrRouteDown) {
		t.Fatalf("before connecting: %v, want the Route down", err)
	}
	f.connect(t)
	if _, err := svc.CheckReachability(context.Background(), f.cluster); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopRoute(f.route.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckReachability(context.Background(), f.cluster); !errors.Is(err, service.ErrRouteDown) {
		t.Fatalf("after stopping: %v, want the Route down", err)
	}
	f.connect(t)
	if _, err := svc.CheckReachability(context.Background(), f.cluster); err != nil {
		t.Fatalf("after reconnecting: %v", err)
	}
	if builds != 1 {
		t.Errorf("builds = %d, want 1", builds)
	}
}

func TestRoute_DeleteInUseLeavesItConnected(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	f.connect(t)

	if err := f.svc.DeleteRoute(f.route.ID); err == nil {
		t.Fatal("deleting a Route in use should fail")
	}
	if _, err := f.svc.CheckReachability(context.Background(), f.cluster); err != nil {
		t.Errorf("after refused delete: %v, want the Route still connected", err)
	}
}
