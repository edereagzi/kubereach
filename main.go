package main

import (
	"embed"
	"encoding/json"
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

	// Errors cross to the frontend as err.cause: the message the user is shown, and a code the UI acts on. Wails
	// falls back from a service's marshaler to its default one, not to Options.MarshalError, so every service is given it.
	typedErrors := application.ServiceOptions{MarshalError: func(err error) []byte {
		data, _ := json.Marshal(service.Describe(err))
		return data
	}}
	app := application.New(application.Options{
		Name:        "Kubereach",
		Description: "A Kubernetes desktop app that can connect to clusters through SSH jump hosts",
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
		// WebView2 draws classic scrollbars unless asked for Windows 11's thin overlay ones, as macOS and GNOME show.
		Windows: application.WindowsOptions{
			EnabledFeatures: []string{"msOverlayScrollbarWinStyle"},
		},
		OnShutdown: svc.Shutdown,
		// A second launch would bind the same Saved Forward ports; it brings the running window back and exits instead,
		// which is also the way back to a hidden window where there is no tray.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.edereagzi.kubereach",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				for _, w := range application.Get().Window.GetAll() {
					bindings.ShowWindow(w)
				}
			},
		},
	})

	svc.Emit = func(name string, data any) { app.Event.Emit(name, data) }
	if err := svc.BindForwards(); err != nil {
		log.Print(err)
	}

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Kubereach",
		Width:  1280,
		Height: 800,
		// Below this the sidebar and an open detail leave the list too narrow to read names in.
		MinWidth:       960,
		MinHeight:      600,
		URL:            "/",
		EnableFileDrop: true,
		// The sidebar runs to the top edge and the traffic lights sit in it; the frontend marks its own drag regions.
		Mac: application.MacWindow{TitleBar: application.MacTitleBarHiddenInset},
	})
	window.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		app.Event.Emit("files:dropped", e.Context().DroppedFiles())
	})
	bindings.NewTray(app, svc, window)
	app.RegisterService(application.NewService(bindings.NewWindowTheme(app, window)))
	about := bindings.NewAppService(app, version, configPath)
	app.RegisterService(application.NewServiceWithOptions(about, typedErrors))
	bindings.InstallMenu(app, about)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
