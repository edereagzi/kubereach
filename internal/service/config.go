package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

const ConfigVersion = 1

var ErrUnsupportedVersion = errors.New("unsupported configuration version")

type Config struct {
	Version  int       `yaml:"version" json:"version"`
	Routes   []Route   `yaml:"routes,omitempty" json:"routes"`
	Clusters []Cluster `yaml:"clusters,omitempty" json:"clusters"`
	// Forwards are the Saved Forwards: Port Forward definitions kept across sessions.
	Forwards []PortForward `yaml:"forwards,omitempty" json:"forwards"`
}

// Route is the ordered list of SSH Servers through which a Cluster is reached.
type Route struct {
	ID      string      `yaml:"id" json:"id"`
	Name    string      `yaml:"name" json:"name"`
	Servers []SSHServer `yaml:"servers" json:"servers"`
}

type AuthMethod string

const (
	AuthAgent    AuthMethod = "agent"
	AuthKeyFile  AuthMethod = "key"
	AuthPassword AuthMethod = "password"
)

// SSHServer holds no secrets: key passphrases and passwords live in memory for the session only.
type SSHServer struct {
	Host    string     `yaml:"host" json:"host"`
	Port    int        `yaml:"port" json:"port"`
	User    string     `yaml:"user" json:"user"`
	Auth    AuthMethod `yaml:"auth" json:"auth"`
	KeyFile string     `yaml:"keyFile,omitempty" json:"keyFile"`
}

type Cluster struct {
	ID         string   `yaml:"id" json:"id"`
	Name       string   `yaml:"name" json:"name"`
	Kubeconfig string   `yaml:"kubeconfig" json:"kubeconfig"`
	Context    string   `yaml:"context" json:"context"`
	RouteID    string   `yaml:"route,omitempty" json:"route"`
	Namespaces []string `yaml:"namespaces,omitempty" json:"namespaces"`
}

// DefaultConfigPath is the configuration file inside the OS configuration directory.
func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kubereach", "config.yaml"), nil
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	if len(data) == 0 {
		return Config{Version: ConfigVersion}, nil
	}
	return parseConfig(path, data)
}

func parseConfig(path string, data []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Version != ConfigVersion {
		return Config{}, fmt.Errorf("%w: %d", ErrUnsupportedVersion, cfg.Version)
	}
	return cfg, nil
}

// saveConfig writes to a sibling temp file and renames it so a crash never leaves a half-written file.
func saveConfig(path string, cfg Config) error {
	cfg.Version = ConfigVersion
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}
