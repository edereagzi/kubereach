package bindings

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const repoURL = "https://github.com/edereagzi/kubereach"

// ShowAbout opens the native About dialog.
func ShowAbout(app *application.App, version, configPath string) {
	d := app.Dialog.Info().SetTitle("Kubereach " + version).SetMessage("Configuration file:\n" + configPath)
	d.AddButton("Reveal").OnClick(func() { reveal(app, configPath) })
	d.AddButton("GitHub").OnClick(func() { _ = app.Browser.OpenURL(repoURL) })
	d.SetDefaultButton(d.AddButton("OK"))
	d.Show()
}

// reveal selects the file in the OS file manager, or opens its directory before the first save.
func reveal(app *application.App, path string) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		_ = app.Env.OpenFileManager(filepath.Dir(path), false)
		return
	}
	_ = app.Env.OpenFileManager(path, true)
}
