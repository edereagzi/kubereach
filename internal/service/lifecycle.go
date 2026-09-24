package service

import (
	"maps"
	"slices"
)

// Shutdown stops every shell, log and event stream, forward and Route, returning once all of them are down.
// Saved Forwards keep their on/off state for the next launch.
func (s *Service) Shutdown() {
	s.mu.Lock()
	shells := slices.Collect(maps.Keys(s.shells))
	logs := slices.Collect(maps.Keys(s.logs))
	events := slices.Collect(maps.Keys(s.events))
	for _, fc := range slices.Collect(maps.Values(s.forwards)) {
		s.unbindForward(fc)
	}
	routes := slices.Collect(maps.Keys(s.routes))
	s.mu.Unlock()
	for _, id := range shells {
		_ = s.StopShell(id)
	}
	for _, id := range logs {
		_ = s.StopLogs(id)
	}
	for _, id := range events {
		_ = s.StopEvents(id)
	}
	for _, id := range routes {
		_ = s.StopRoute(id)
	}
}
