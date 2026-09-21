package service_test

import (
	"testing"

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

	// An idle forward does not count; a failed dial does.
	pf := f.saveForward(t)
	waitForwardState(t, forwards, service.StateIdle)
	assertOverall(service.StateConnected)
	pingThroughPort(t, pf.LocalPort)
	waitForwardState(t, forwards, service.StateConnected)

	f.ssh.dropConnections()
	f.waitState(t, service.StateReconnecting)
	assertOverall(service.StateReconnecting)
	f.waitState(t, service.StateConnected)
	waitForwardState(t, forwards, service.StateIdle)

	if err := f.svc.StopRoute(f.route.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)
	assertOverall(service.StateIdle)
	touchPort(t, pf.LocalPort)
	waitForwardState(t, forwards, service.StateError)
	assertOverall(service.StateError)
	if err := f.svc.SetForwardEnabled(pf.ID, false); err != nil {
		t.Fatal(err)
	}
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
	pf := f.saveForward(t)
	pingThroughPort(t, pf.LocalPort)
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
	if !dialFails(pf.LocalPort) {
		t.Errorf("local port %d still open after shutdown", pf.LocalPort)
	}
	if cfg, _ := f.svc.LoadConfig(); len(cfg.Forwards) != 1 || !cfg.Forwards[0].Enabled {
		t.Errorf("saved forwards after shutdown = %+v, want the forward still on", cfg.Forwards)
	}
}
