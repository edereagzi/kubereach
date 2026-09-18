// Package service is the UI-agnostic core of Kubereach. Everything the UI can do
// is a method here; nothing in this package imports Wails.
package service

import (
	"os"
	"path/filepath"
	"sync"
)

type Service struct {
	// Emit publishes an event to the UI; the bindings layer wires it to Wails. Called from any goroutine.
	Emit func(name string, data any)
	// KnownHostsPath is the file SSH host keys are checked against and approved keys are appended to.
	KnownHostsPath string

	configPath string
	clients    ClientFactory
	mu         sync.Mutex
	routes     map[string]*routeConn
	forwards   map[string]*forwardConn
	// secrets holds session-only key passphrases (by key file) and passwords (by user@host:port).
	secrets map[string]string
}

// New creates the service; a nil clients factory loads clientsets from each Cluster's kubeconfig.
func New(configPath string, clients ClientFactory) *Service {
	if clients == nil {
		clients = newKubeconfigClient
	}
	home, _ := os.UserHomeDir()
	return &Service{
		Emit:           func(string, any) {},
		KnownHostsPath: filepath.Join(home, ".ssh", "known_hosts"),
		configPath:     configPath,
		clients:        clients,
		routes:         map[string]*routeConn{},
		forwards:       map[string]*forwardConn{},
		secrets:        map[string]string{},
	}
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
