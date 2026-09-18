// Package service is the UI-agnostic core of Kubereach. Everything the UI can do
// is a method here; nothing in this package imports Wails.
package service

import "sync"

type Service struct {
	configPath string
	clients    ClientFactory
	mu         sync.Mutex
}

// New creates the service; a nil clients factory loads clientsets from each Cluster's kubeconfig.
func New(configPath string, clients ClientFactory) *Service {
	if clients == nil {
		clients = newKubeconfigClient
	}
	return &Service{configPath: configPath, clients: clients}
}

func (s *Service) LoadConfig() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return loadConfig(s.configPath)
}

func (s *Service) SaveConfig(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return saveConfig(s.configPath, cfg)
}
