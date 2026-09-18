// Package bindings exposes the service layer to the Wails frontend.
package bindings

import "github.com/edereagzi/kubereach/internal/service"

type ConfigService struct {
	svc *service.Service
}

func NewConfigService(svc *service.Service) *ConfigService {
	return &ConfigService{svc: svc}
}

func (c *ConfigService) Load() (service.Config, error) {
	return c.svc.LoadConfig()
}
