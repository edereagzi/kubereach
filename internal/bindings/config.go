// Package bindings exposes the service layer to the Wails frontend.
package bindings

import (
	"os"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type ConfigService struct {
	svc *service.Service
}

func NewConfigService(svc *service.Service) *ConfigService {
	return &ConfigService{svc: svc}
}

func (c *ConfigService) Load() (service.Config, error) {
	return c.svc.LoadConfig()
}

// Export asks where to save and writes the configuration there; "" when cancelled.
func (c *ConfigService) Export() (string, error) {
	path, err := application.Get().Dialog.SaveFile().
		SetMessage("Export configuration").
		SetFilename("kubereach-export.yaml").
		PromptForSingleSelection()
	if err != nil || path == "" {
		return "", err
	}
	return path, c.svc.ExportConfig(path)
}

// InspectImport opens the native file picker and previews the chosen file; nil when cancelled.
func (c *ConfigService) InspectImport() (*service.ImportPreview, error) {
	path, err := application.Get().Dialog.OpenFile().
		SetTitle("Import configuration").
		AddFilter("YAML", "*.yaml;*.yml").
		PromptForSingleSelection()
	if err != nil || path == "" {
		return nil, err
	}
	preview, err := c.svc.InspectImport(path)
	if err != nil {
		return nil, err
	}
	return &preview, nil
}

func (c *ConfigService) InspectPath(path string) (service.ImportPreview, error) {
	return c.svc.InspectImport(path)
}

func (c *ConfigService) Import(path string, remap map[string]string) error {
	return c.svc.ImportConfig(path, remap)
}

// PickPath opens the native file picker in the home directory and returns the chosen path, or "" when cancelled.
func (c *ConfigService) PickPath(title string) (string, error) {
	home, _ := os.UserHomeDir()
	return pickFile(title, home)
}
