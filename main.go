package main

import (
	"embed"
	"errors"
	"log"

	"github.com/edereagzi/kubereach/internal/bindings"
	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	configPath, err := service.DefaultConfigPath()
	if err != nil {
		log.Fatal(err)
	}
	svc := service.New(configPath, nil)

	app := application.New(application.Options{
		Name:        "Kubereach",
		Description: "Access Kubernetes clusters behind SSH bastions, VPNs and closed networks",
		// Sentinel errors cross to the frontend as err.cause.code so the UI never parses messages.
		MarshalError: func(err error) []byte {
			if errors.Is(err, service.ErrForbidden) {
				return []byte(`{"code":"forbidden"}`)
			}
			return nil
		},
		Services: []application.Service{
			application.NewService(bindings.NewConfigService(svc)),
			application.NewService(bindings.NewClusterService(svc)),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Kubereach",
		Width:  1200,
		Height: 760,
		URL:    "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
