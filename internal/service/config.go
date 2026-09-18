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
	Clusters []Cluster `yaml:"clusters,omitempty" json:"clusters"`
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
