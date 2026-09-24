package main

import (
	"embed"
	"encoding/json"
	"errors"
	"log"

	"github.com/edereagzi/kubereach/internal/bindings"
	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
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

	// Typed errors cross to the frontend as err.cause.code so the UI never parses messages. Wails falls back from a
	// service's marshaler to its default one, not to Options.MarshalError, so every service is given it.
	typedErrors := application.ServiceOptions{MarshalError: func(err error) []byte {
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
	}}
	app := application.New(application.Options{
		Name:        "Kubereach",
		Description: "Access Kubernetes clusters behind SSH bastions, VPNs and closed networks",
		Services: []application.Service{
			application.NewServiceWithOptions(bindings.NewConfigService(svc), typedErrors),
			application.NewServiceWithOptions(bindings.NewClusterService(svc), typedErrors),
			application.NewServiceWithOptions(bindings.NewRouteService(svc), typedErrors),
			application.NewServiceWithOptions(bindings.NewForwardService(svc), typedErrors),
			application.NewServiceWithOptions(bindings.NewLogService(svc), typedErrors),
			application.NewServiceWithOptions(bindings.NewEventService(svc), typedErrors),
			application.NewServiceWithOptions(bindings.NewShellService(svc), typedErrors),
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
		Title:          "Kubereach",
		Width:          1200,
		Height:         760,
		URL:            "/",
		EnableFileDrop: true,
	})
	window.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		app.Event.Emit("files:dropped", e.Context().DroppedFiles())
	})
	bindings.NewTray(app, svc, window)
	app.RegisterService(application.NewService(bindings.NewWindowTheme(app, window)))
	bindings.InstallMenu(app, bindings.NewAppService(app, version, configPath).ShowAbout)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
