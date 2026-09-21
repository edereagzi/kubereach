package bindings

import (
	"context"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// WindowTheme paints the window in the OS colour before the page loads, so dark mode never flashes white.
// The OS theme is readable only once the app runs; service startup is the earliest hook before windows are created.
type WindowTheme struct {
	app    *application.App
	window *application.WebviewWindow
}

func NewWindowTheme(app *application.App, window *application.WebviewWindow) *WindowTheme {
	return &WindowTheme{app: app, window: window}
}

func (t *WindowTheme) ServiceStartup(context.Context, application.ServiceOptions) error {
	if t.app.Env.IsDarkMode() {
		t.window.SetBackgroundColour(application.NewRGBA(19, 22, 28, 255))
	}
	return nil
}
