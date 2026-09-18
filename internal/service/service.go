// Package service is the UI-agnostic core of Kubereach. Everything the UI can do
// is a method here; nothing in this package imports Wails.
package service

type Service struct {
	configPath string
}

func New(configPath string) *Service {
	return &Service{configPath: configPath}
}

func (s *Service) LoadConfig() (Config, error) {
	return loadConfig(s.configPath)
}

func (s *Service) SaveConfig(cfg Config) error {
	return saveConfig(s.configPath, cfg)
}
