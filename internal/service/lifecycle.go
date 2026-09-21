package service

import (
	"maps"
	"slices"
)

// OverallState folds every Route and forward into one state for the tray icon:
// error > reconnecting > connecting > connected; stopped entities do not count.
func (s *Service) OverallState() State {
	var states []State
	for _, r := range s.RouteStatuses() {
		states = append(states, r.State)
	}
	for _, f := range s.ForwardStatuses() {
		states = append(states, f.State)
	}
	switch {
	case slices.Contains(states, StateError):
		return StateError
	case slices.Contains(states, StateReconnecting):
		return StateReconnecting
	case slices.Contains(states, StateConnecting):
		return StateConnecting
	case slices.Contains(states, StateConnected):
		return StateConnected
	}
	return StateIdle
}

// Shutdown stops every shell, log stream, forward and Route, returning once all of them are down.
func (s *Service) Shutdown() {
	s.mu.Lock()
	shells := slices.Collect(maps.Keys(s.shells))
	logs := slices.Collect(maps.Keys(s.logs))
	forwards := slices.Collect(maps.Keys(s.forwards))
	routes := slices.Collect(maps.Keys(s.routes))
	s.mu.Unlock()
	for _, id := range shells {
		_ = s.StopShell(id)
	}
	for _, id := range logs {
		_ = s.StopLogs(id)
	}
	for _, id := range forwards {
		_ = s.StopForward(id)
	}
	for _, id := range routes {
		_ = s.StopRoute(id)
	}
}
