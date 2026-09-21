package service_test

import (
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
)

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
	if !dialFails(pf.LocalPort) {
		t.Errorf("local port %d still open after shutdown", pf.LocalPort)
	}
	if cfg, _ := f.svc.LoadConfig(); len(cfg.Forwards) != 1 || !cfg.Forwards[0].Enabled {
		t.Errorf("saved forwards after shutdown = %+v, want the forward still on", cfg.Forwards)
	}
}
