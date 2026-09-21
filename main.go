package main

import (
	"embed"
	"encoding/json"
	"errors"
	"log"

	"github.com/edereagzi/kubereach/internal/bindings"
	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

// version is overwritten by the release build via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	configPath, err := service.DefaultConfigPath()
	if err != nil {
		log.Fatal(err)
	}
	svc := service.New(configPath, nil)

	app := application.New(application.Options{
		Name:        "Kubereach",
		Description: "Access Kubernetes clusters behind SSH bastions, VPNs and closed networks",
		// Typed errors cross to the frontend as err.cause.code so the UI never parses messages.
		MarshalError: func(err error) []byte {
			if errors.Is(err, service.ErrForbidden) {
				return []byte(`{"code":"forbidden"}`)
			}
			var need *service.CredentialError
			if errors.As(err, &need) {
				data, _ := json.Marshal(need)
				return data
			}
			var exists *service.ForwardExistsError
			if errors.As(err, &exists) {
				data, _ := json.Marshal(struct {
					Code string `json:"code"`
					*service.ForwardExistsError
				}{"forward-exists", exists})
				return data
			}
			var inUse *service.PortInUseError
			if errors.As(err, &inUse) {
				data, _ := json.Marshal(struct {
					Code string `json:"code"`
					*service.PortInUseError
				}{"port-in-use", inUse})
				return data
			}
			return nil
		},
		Services: []application.Service{
			application.NewService(bindings.NewConfigService(svc)),
			application.NewService(bindings.NewClusterService(svc)),
			application.NewService(bindings.NewRouteService(svc)),
			application.NewService(bindings.NewForwardService(svc)),
			application.NewService(bindings.NewLogService(svc)),
			application.NewService(bindings.NewShellService(svc)),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		OnShutdown: svc.Shutdown,
	})

	svc.Emit = func(name string, data any) { app.Event.Emit(name, data) }
	if err := svc.BindForwards(); err != nil {
		log.Print(err)
	}

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Kubereach",
		Width:  1200,
		Height: 760,
		URL:    "/",
	})
	bindings.NewTray(app, svc, window, version)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
