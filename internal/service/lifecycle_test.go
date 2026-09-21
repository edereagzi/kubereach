package service_test

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
)

func TestOverallState_AggregatesRoutesAndForwards(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	forwards := f.captureForwards()
	assertOverall := func(want service.State) {
		t.Helper()
		if got := f.svc.OverallState(); got != want {
			t.Fatalf("overall state %q, want %q", got, want)
		}
	}

	assertOverall(service.StateIdle)
	f.connect(t)
	assertOverall(service.StateConnected)

	pf, err := f.svc.StartForward(context.Background(), service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: service.TargetPod, Namespace: "default", Name: "api-0"},
		RemotePort: 8080,
		LocalPort:  freePort(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForwardState(t, forwards, service.StateConnected)
	assertOverall(service.StateConnected)

	// A dropped Route takes the forward down with it: one reconnecting entity outweighs the connected ones.
	f.ssh.dropConnections()
	f.waitState(t, service.StateReconnecting)
	assertOverall(service.StateReconnecting)
	f.waitState(t, service.StateConnected)
	waitForwardState(t, forwards, service.StateConnected)

	if err := f.svc.StopForward(pf.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.StopRoute(f.route.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)
	assertOverall(service.StateIdle)

	// A refused password is fatal for the Route and outweighs everything else.
	pwServer := startSSHServerWithPassword(t, pub, "s3cret")
	f.trust(t, pwServer)
	srv := pwServer.server("me", "")
	srv.Auth = service.AuthPassword
	f.route.Servers = []service.SSHServer{srv}
	if _, err := f.svc.SaveRoute(f.route); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ConnectRoute(f.route.ID, "wrong"); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateError)
	assertOverall(service.StateError)
}

func TestShutdown_ClosesEveryRouteAndForward(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	forwards := f.captureForwards()
	f.connect(t)
	local := freePort(t)
	if _, err := f.svc.StartForward(context.Background(), service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: service.TargetPod, Namespace: "default", Name: "api-0"},
		RemotePort: 8080,
		LocalPort:  local,
	}); err != nil {
		t.Fatal(err)
	}
	waitForwardState(t, forwards, service.StateConnected)

	f.svc.Shutdown()

	waitForwardState(t, forwards, service.StateStopped)
	f.waitState(t, service.StateStopped)
	if got := f.svc.ForwardStatuses(); len(got) != 0 {
		t.Errorf("forwards still registered after shutdown: %v", got)
	}
	if got := f.svc.OverallState(); got != service.StateIdle {
		t.Errorf("overall state %q after shutdown, want idle", got)
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(local)), time.Second); err == nil {
		_ = conn.Close()
		t.Errorf("local port %d still open after shutdown", local)
	}
}
