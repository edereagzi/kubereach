// Package service is the UI-agnostic core of Kubereach. Everything the UI can do
// is a method here; nothing in this package imports Wails.
package service

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Service struct {
	// Emit publishes an event to the UI; the bindings layer wires it to Wails. Called from any goroutine.
	Emit func(name string, data any)
	// KnownHostsPath is the file SSH host keys are checked against and approved keys are appended to.
	KnownHostsPath string
	// ForwardIdle is how long a forward's pod connection outlives its last local connection.
	ForwardIdle time.Duration

	configPath string
	clients    ClientFactory
	mu         sync.Mutex
	routes     map[string]*routeConn
	forwards   map[string]*forwardConn
	logs       map[string]*logConn
	events     map[string]*eventConn
	shells     map[string]*shellConn
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
		ForwardIdle:    5 * time.Minute,
		configPath:     configPath,
		clients:        clients,
		routes:         map[string]*routeConn{},
		forwards:       map[string]*forwardConn{},
		logs:           map[string]*logConn{},
		events:         map[string]*eventConn{},
		shells:         map[string]*shellConn{},
		secrets:        map[string]string{},
	}
}

func (s *Service) ConfigPath() string { return s.configPath }

func (s *Service) LoadConfig() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return loadConfig(s.configPath)
}

func (s *Service) SaveConfig(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveConfig(cfg)
}

// EventConfigChanged fires after every successful write of the configuration file.
const EventConfigChanged = "config:changed"

// saveConfig persists cfg and announces the change; callers hold s.mu.
func (s *Service) saveConfig(cfg Config) error {
	if err := saveConfig(s.configPath, cfg); err != nil {
		return err
	}
	s.Emit(EventConfigChanged, nil)
	return nil
}
